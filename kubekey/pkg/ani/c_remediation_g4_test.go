/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kkcorev1 "github.com/kubesphere/kubekey/api/core/v1"
	kkprojectv1 "github.com/kubesphere/kubekey/api/project/v1"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	kkoptions "github.com/kubesphere/kubekey/v4/cmd/kk/app/options"
	_const "github.com/kubesphere/kubekey/v4/pkg/const"
	"github.com/kubesphere/kubekey/v4/pkg/converter"
	"github.com/kubesphere/kubekey/v4/pkg/converter/tmpl"
	"github.com/kubesphere/kubekey/v4/pkg/modules"
	"github.com/kubesphere/kubekey/v4/pkg/variable"
	"github.com/kubesphere/kubekey/v4/pkg/variable/source"
	"k8s.io/apimachinery/pkg/runtime"
)

// ---------------------------------------------------------------------------
// C07 — one run, one execution context, decided where the run is decided and
// read everywhere it is used.
//
// The chain under test is the production one, end to end:
//
//	site YAML -> LoadClusterConfig -> KubeKeyConfig/KubeKeyInventory
//	          -> writeYAML config.yaml + inventory.yaml   (what the runner writes)
//	          -> cmd/kk/app/options CommonOptions.Complete (how kk reads them back)
//	          -> variable.New + GetAllVariable            (the executor's own merge)
//	          -> api/project/v1 parse of the REAL role/task/playbook files
//	          -> converter.MarshalBlock + FindModule      (the executor's own task build)
//	          -> variable.Extension2String                (what the module renders)
//	          -> bash, with a kubectl shim that dials the endpoint its
//	             kubeconfig file names                    (the final target)
//
// Nothing here hand-builds a map: the only inputs are a site file and the
// packaged roles, and the assertions read what the real layers produced.
// ---------------------------------------------------------------------------

const c07SiteTemplate = `profile: full
name: ani-lab
installerNode: node1
ssh:
  user: ubuntu
  port: 22
  password: site-password
nodes:
  - {name: node1, address: 192.0.2.11}
  - {name: node2, address: 192.0.2.12}
  - {name: node3, address: 192.0.2.13}
network:
  stack: kcn
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn:
    managedDevices: [ens35]
    encapNetworks: [192.0.2.0/24]
    intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]
registry:
  port: 5000
storage:
  enabled: false
components:
  certManager: {enabled: %s}
  postgresql: {enabled: %s, storageClass: pre-existing-class, storageSize: 10Gi}
  valkey: {enabled: false, storageClass: pre-existing-class, storageSize: 2Gi}
  nats: {enabled: %s, storageClass: pre-existing-class, storageSize: 5Gi}
  metrics: {enabled: %s, storageClass: pre-existing-class, prometheusStorageSize: 5Gi, alertmanagerStorageSize: 1Gi, prometheusRetention: 24h}
  logging: {backend: %s, storageClass: pre-existing-class, storageSize: 5Gi, retentionDays: 3}
`

func c07SiteYAML(certManager, postgresql, nats, metrics bool, backend string) string {
	yes := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf(c07SiteTemplate, yes(certManager), yes(postgresql), yes(nats), yes(metrics), backend)
}

// c07Chain is one run of the production chain, up to the executor's variable
// engine. runScope is nil for a first install (the generator supplies its own).
type c07Chain struct {
	root     string
	spec     map[string]any
	hostVars map[string]any
	vars     variable.Variable
	scope    RunScope
	cluster  ClusterConfig
}

func c07NewChain(t *testing.T, site string, runScope *RunScope, componentsScope map[string]any) *c07Chain {
	t.Helper()
	root := t.TempDir()
	siteFile := filepath.Join(root, "site.yaml")
	if err := os.WriteFile(siteFile, []byte(site), 0o600); err != nil {
		t.Fatal(err)
	}
	cluster, err := LoadClusterConfig(siteFile)
	if err != nil {
		t.Fatalf("the shipped generator refused the site file: %v", err)
	}
	if runScope != nil && runScope.Kubeconfig != "" {
		// A components run names its own kubeconfig. Give it real bytes only if
		// the caller has not supplied the file itself: a test that points the run
		// at a dummy config with a server in it must not be overwritten.
		if err := os.MkdirAll(filepath.Dir(runScope.Kubeconfig), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(runScope.Kubeconfig); err != nil {
			if err := os.WriteFile(runScope.Kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	spec, err := KubeKeyConfig(cluster, filepath.Join(root, "packages", "kubekey-artifact.tgz"), root, testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig: %v", err)
	}
	scope := InstallRunScope(cluster.Name)
	if runScope != nil {
		aniSpec := spec["ani"].(map[string]any)
		aniSpec["components_run"] = map[string]any{"scope": componentsScope}
		if err := runScope.Replace(spec); err != nil {
			t.Fatalf("the run refused to publish its own execution context: %v", err)
		}
		scope = *runScope
	}
	configFile := filepath.Join(root, "config.yaml")
	if err := writeYAML(configFile, map[string]any{
		"apiVersion": "kubekey.kubesphere.io/v1", "kind": "Config", "spec": spec,
	}); err != nil {
		t.Fatal(err)
	}
	inventorySpec, err := KubeKeyInventory(cluster)
	if err != nil {
		t.Fatalf("KubeKeyInventory: %v", err)
	}
	inventoryFile := filepath.Join(root, "inventory.yaml")
	if err := writeYAML(inventoryFile, map[string]any{
		"apiVersion": "kubekey.kubesphere.io/v1", "kind": "Inventory",
		"metadata": map[string]any{"name": "default"}, "spec": inventorySpec,
	}); err != nil {
		t.Fatal(err)
	}

	// Read both files back the way `kk create cluster` / `kk run` does.
	o := kkoptions.NewCommonOptions()
	o.Workdir = root
	o.ConfigFile = configFile
	o.InventoryFile = inventoryFile
	playbook := &kkcorev1.Playbook{
		ObjectMeta: metav1.ObjectMeta{Name: "ani-c07", Namespace: metav1.NamespaceDefault},
		Spec:       kkcorev1.PlaybookSpec{Playbook: "ani_components"},
	}
	if err := o.Complete(playbook); err != nil {
		t.Fatalf("the real kk loader refused the files the installer writes: %v", err)
	}
	client := fakeclient.NewClientBuilder().WithScheme(_const.Scheme).WithObjects(o.Inventory).Build()
	vars, err := variable.New(context.Background(), client, *playbook, source.MemorySource)
	if err != nil {
		t.Fatalf("variable.New: %v", err)
	}
	// The installer node is where these tasks run (local connector in the
	// generated inventory); node2/node3 are reached over SSH.
	hostVarsValue, err := vars.Get(variable.GetAllVariable("node1"))
	if err != nil {
		t.Fatalf("GetAllVariable: %v", err)
	}
	hostVars, ok := hostVarsValue.(map[string]any)
	if !ok {
		t.Fatalf("the merged host context is %T, not a map", hostVarsValue)
	}
	if len(hostVars) == 0 {
		t.Fatal("the merged host context is empty: the generated spec reached no executor variable at all")
	}
	// Smoke reads a registered result; seed it through the same merge the
	// executor's own dealRegister uses, so rendering is not special-cased here.
	node, err := converter.ConvertMap2Node(map[string]any{
		"ani_smoke_backend_ip": map[string]any{"stdout": "10.16.0.9", "stderr": "", "error": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := vars.Merge(variable.MergeRuntimeVariable([]yaml.Node{node}, "node1")); err != nil {
		t.Fatal(err)
	}
	reloaded, err := vars.Get(variable.GetAllVariable("node1"))
	if err != nil {
		t.Fatal(err)
	}
	hostVars, _ = reloaded.(map[string]any)

	ch := &c07Chain{root: root, spec: spec, hostVars: hostVars, vars: vars, scope: scope, cluster: cluster}
	// Fail loudly if the generator's own scope did not survive the YAML round trip.
	// FromSpec's Validate() is deliberately not used here: the first install pins
	// admin.conf, a file the install itself creates and that cannot exist yet on
	// the machine rendering this config.
	var readBack RunScope
	if err := c07ReadScopeFrom(o.Config.Value(), &readBack); err != nil {
		t.Fatalf("the scope the generator published cannot be read back: %v", err)
	}
	if readBack != scope {
		t.Fatalf("the scope that reached the spec is %+v, the run decided %+v", readBack, scope)
	}
	return ch
}

// c07ReadScopeFrom reads .ani.run out of the loaded config value (so this is the
// scope after the YAML round trip) without FromSpec's on-disk check.
func c07ReadScopeFrom(loaded map[string]any, into *RunScope) error {
	ani, _ := loaded["ani"].(map[string]any)
	run, _ := ani[specKey].(map[string]any)
	if len(run) == 0 {
		return fmt.Errorf("the loaded config carries no .ani.run block")
	}
	read := func(key string) string { value, _ := run[key].(string); return value }
	*into = RunScope{Kubeconfig: read("kubeconfig"), LogsDir: read("logs_dir"),
		ConnectionsDir: read("connections_dir"), RunID: read("run_id"), KKBinary: read("kk_bin")}
	return nil
}

// c07Task is one task of a packaged role, after the executor's own conversion
// and the module's own argument rendering.
type c07Task struct {
	name   string
	module string
	args   map[string]any
	text   string
}

// c07RenderRole parses the real tasks/main.yaml and produces what the executor
// would hand each module: the module name from the unknown-field completion in
// blockExecutor.dealTask, and the rendered argument text from the module itself.
func c07RenderRole(t *testing.T, ch *c07Chain, role string) []c07Task {
	t.Helper()
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", role, "tasks", "main.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the packaged role %s: %v", role, err)
	}
	var blocks []kkprojectv1.Block
	if err := yaml.Unmarshal(data, &blocks); err != nil {
		t.Fatalf("the real project parser refused %s: %v", path, err)
	}
	if len(blocks) == 0 {
		t.Fatalf("%s parsed into zero tasks, so this test would prove nothing", path)
	}
	var out []c07Task
	for _, block := range blocks {
		task := converter.MarshalBlock([]string{"node1"}, nil, block)
		for name, arg := range block.UnknownField {
			if modules.FindModule(name) == nil {
				continue
			}
			task.Spec.Module.Name = name
			raw, err := json.Marshal(arg)
			if err != nil {
				t.Fatalf("%s: %v", block.Name, err)
			}
			task.Spec.Module.Args = runtime.RawExtension{Raw: raw}
			break
		}
		if task.Spec.Module.Name == "" {
			t.Fatalf("role %s task %q names no module; the packaged role is not executable", role, block.Name)
		}
		rendered, err := variable.Extension2String(ch.hostVars, task.Spec.Module.Args)
		if err != nil {
			t.Fatalf("role %s task %q failed to render with the generated context: %v", role, block.Name, err)
		}
		text := string(rendered)
		for _, bad := range []string{"{{", "<no value>", "%!(EXTRA"} {
			if strings.Contains(text, bad) {
				t.Fatalf("role %s task %q rendered %q still in it:\n%s", role, block.Name, bad, text)
			}
		}
		args := map[string]any{}
		if err := json.Unmarshal([]byte(text), &args); err == nil {
			// A module whose arguments are a mapping (template:, copy:): the
			// interesting field is where it writes.
			if dest, ok := args["dest"].(string); ok {
				text = dest
			}
		}
		out = append(out, c07Task{name: block.Name, module: task.Spec.Module.Name, args: args, text: text})
	}
	return out
}

// ---------------------------------------------------------------------------
// T-C07-4: the three states a component switch can be in, decided by the real
// condition engine over the real playbooks.
// ---------------------------------------------------------------------------

// c07PlaybookGates reads the role `when:` conditions straight out of the two
// packaged playbooks that decide whether a component role runs.
func c07PlaybookGates(t *testing.T, playbookRel string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "builtin", "core", "playbooks", playbookRel))
	if err != nil {
		t.Fatalf("read %s: %v", playbookRel, err)
	}
	var plays []kkprojectv1.Play
	if err := yaml.Unmarshal(data, &plays); err != nil {
		t.Fatalf("the real project parser refused %s: %v", playbookRel, err)
	}
	gates := map[string][]string{}
	for _, play := range plays {
		for _, role := range play.Roles {
			if role.Role == "" {
				continue
			}
			gates[role.Role] = role.When.Data
		}
	}
	if len(gates) == 0 {
		t.Fatalf("%s yielded no gated roles", playbookRel)
	}
	return gates
}

func TestC07_ComponentSwitchHasExactlyTheThreeStatesThePlaybooksDescribe(t *testing.T) {
	const role = "ani/postgresql"
	installGates := c07PlaybookGates(t, "create_cluster.yaml")
	componentsGates := c07PlaybookGates(t, "ani_components.yaml")
	for _, gates := range []map[string][]string{installGates, componentsGates} {
		if len(gates[role]) == 0 {
			t.Fatalf("no packaged playbook gates %s; this table would be vacuous", role)
		}
	}

	// ON: the site enables it and the run's scope asks for it.
	on := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"),
		&RunScope{Kubeconfig: filepath.Join(t.TempDir(), "components-A.conf"),
			LogsDir: "/r/logs", ConnectionsDir: "/r/work/connections.d", RunID: "run-A"},
		map[string]any{"postgresql": true})
	// OFF: the site does not enable it.
	off := c07NewChain(t, c07SiteYAML(true, false, true, true, "loki"),
		&RunScope{Kubeconfig: filepath.Join(t.TempDir(), "components-B.conf"),
			LogsDir: "/r/logs", ConnectionsDir: "/r/work/connections.d", RunID: "run-B"},
		map[string]any{"postgresql": true})
	// NOT IN SCOPE: enabled by the site, but this run was not asked to add it.
	outOfScope := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"),
		&RunScope{Kubeconfig: filepath.Join(t.TempDir(), "components-C.conf"),
			LogsDir: "/r/logs", ConnectionsDir: "/r/work/connections.d", RunID: "run-C"},
		map[string]any{"nats": true})
	// The first install has no scope block at all.
	install := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"), nil, nil)

	type want struct {
		name    string
		vars    map[string]any
		install bool
		run     bool
		runErrs bool
	}
	cases := []want{
		{"enabled and in scope", on.hostVars, true, true, false},
		{"enabled but not in this run's scope", outOfScope.hostVars, true, false, false},
		{"disabled by the site", off.hostVars, false, false, false},
		// A spec with no components_run block at all is not a "skip": the real
		// engine reports `index of untyped nil`. Recorded as measured — an
		// ani_components.yaml run always sets the scope, so this combination does
		// not occur in production, but the playbook's comment claimed it degraded
		// to false and that claim is wrong.
		{"first install (no components_run block)", install.hostVars, true, false, true},
	}
	for _, tc := range cases {
		installed, err := tmpl.ParseBool(tc.vars, installGates[role]...)
		if err != nil {
			t.Fatalf("%s: the real condition engine errored on the install gate %v: %v", tc.name, installGates[role], err)
		}
		if installed != tc.install {
			t.Fatalf("%s: create_cluster.yaml decided %v, want %v (gate %v)", tc.name, installed, tc.install, installGates[role])
		}
		added, err := tmpl.ParseBool(tc.vars, componentsGates[role]...)
		if tc.runErrs {
			if err == nil {
				t.Fatalf("%s: the components gate evaluated to %v with no error, which contradicts what this table measured", tc.name, added)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: the real condition engine errored on the components gate %v: %v", tc.name, componentsGates[role], err)
		}
		if added != tc.run {
			t.Fatalf("%s: ani_components.yaml decided %v, want %v (gate %v)", tc.name, added, tc.run, componentsGates[role])
		}
	}

	// The claim this table replaces is that every `.enabled` reads as false, so a
	// component could never pass its gate. Case 1 above already contradicts it
	// through the real engine. The two shapes a missing entry can take are
	// measured here rather than assumed, because they differ:
	//   - a key whose value is nil evaluates to false with no error;
	//   - a key that is absent from the map does the same.
	// Neither can turn a true switch into false, and neither is a silent skip of
	// something that should have run: the value the gate reads is the value the
	// generator wrote. The condition itself is therefore correct and unchanged.
	for _, shape := range []string{"nil value", "absent key"} {
		broken := c07CloneVars(on.hostVars)
		components := broken["ani"].(map[string]any)["components"].(map[string]any)
		if shape == "nil value" {
			components["postgresql"] = nil
		} else {
			delete(components, "postgresql")
		}
		got, err := tmpl.ParseBool(broken, componentsGates[role]...)
		if err != nil {
			t.Fatalf("%s: the gate errored instead of evaluating: %v", shape, err)
		}
		if got {
			t.Fatalf("%s: the gate passed a component the spec does not describe", shape)
		}
	}
	// And the switch really is read, not hardcoded: flipping only the site YAML
	// from true to false changed the gate's answer, which is what the earlier
	// "the tag is missing so it is always false" reading denied.
	if enabledGate, err := tmpl.ParseBool(on.hostVars, installGates[role]...); err != nil || !enabledGate {
		t.Fatalf("an enabled component did not pass the install gate (%v, %v)", enabledGate, err)
	}
	if disabledGate, err := tmpl.ParseBool(off.hostVars, installGates[role]...); err != nil || disabledGate {
		t.Fatalf("a disabled component passed the install gate (%v, %v)", disabledGate, err)
	}
}

// c07CloneVars copies the two levels a gate reads, so a test can damage a
// derived context without changing the chain it was cloned from.
func c07CloneVars(vars map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range vars {
		out[k] = v
	}
	ani := map[string]any{}
	for k, v := range out["ani"].(map[string]any) {
		ani[k] = v
	}
	components := map[string]any{}
	for k, v := range ani["components"].(map[string]any) {
		components[k] = v
	}
	ani["components"] = components
	out["ani"] = ani
	return out
}

// ---------------------------------------------------------------------------
// T-C07-5: every cluster call in a packaged role names this run's context.
// ---------------------------------------------------------------------------

// c07ClusterCall reports whether a rendered line really invokes the cluster.
// `test -x ".../bin/helm"` names the binary as a path being checked, not a
// command being run, so it must not be counted as a call.
func c07ClusterCall(line string) bool {
	return strings.Contains(line, "kubectl ") || strings.Contains(line, "helm ")
}

func c07CallCount(line string) int {
	return strings.Count(line, "kubectl ") + strings.Count(line, "helm ")
}

func TestC07_RoleTasksRenderTheRunsKubeconfigIntoEveryClusterCall(t *testing.T) {
	componentsKubeconfig := filepath.Join(t.TempDir(), "cluster-components.conf")
	ch := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"),
		&RunScope{Kubeconfig: componentsKubeconfig,
			LogsDir:        filepath.Join(t.TempDir(), "run-logs"),
			ConnectionsDir: filepath.Join(t.TempDir(), "run-work", "connections.d"),
			RunID:          "c07run"},
		map[string]any{"nats": true, "postgresql": true})

	roles := []string{"cert-manager", "postgresql", "valkey", "nats", "metrics", "loki", "opensearch", "fluent-bit", "smoke"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			tasks := c07RenderRole(t, ch, role)
			calls := 0
			for _, task := range tasks {
				for _, line := range strings.Split(task.text, "\n") {
					trimmed := strings.TrimSpace(line)
					if !c07ClusterCall(trimmed) {
						continue
					}
					if strings.HasSuffix(trimmed, "\\") {
						continue // a continuation; the pin is asserted on the whole task below
					}
					calls++
					if !strings.Contains(trimmed, fmt.Sprintf("KUBECONFIG=%q", componentsKubeconfig)) {
						t.Fatalf("role %s task %q calls the cluster without this run's pinned kubeconfig:\n%s", role, task.name, trimmed)
					}
					if strings.Contains(trimmed, DefaultKubeconfigPath) && componentsKubeconfig != DefaultKubeconfigPath {
						t.Fatalf("role %s task %q still reaches for the installer default: %s", role, task.name, trimmed)
					}
				}
				if strings.Contains(task.text, "ANI_VERIFY_KUBECONFIG") {
					if !strings.Contains(task.text, fmt.Sprintf("ANI_VERIFY_KUBECONFIG=%q", componentsKubeconfig)) {
						t.Fatalf("role %s task %q pins a different kubeconfig for the checker than the run uses:\n%s", role, task.name, task.text)
					}
				}
				if strings.Contains(task.text, "KUBECONFIG_FILE") && !strings.Contains(task.text, fmt.Sprintf("KUBECONFIG_FILE=%q", componentsKubeconfig)) {
					t.Fatalf("role %s task %q hands the probe a different context: %s", role, task.name, task.text)
				}
			}
			if calls == 0 {
				t.Fatalf("role %s rendered no cluster call at all, so this test would prove nothing", role)
			}
			// The run's log and fragment directories, not the install's.
			for _, task := range tasks {
				if strings.Contains(task.text, "ANI_VERIFY_OUTPUT_DIR") && !strings.Contains(task.text, ch.scope.LogsDir) {
					t.Fatalf("role %s task %q sends checker logs outside this run's directory: %s", role, task.name, task.text)
				}
				if task.module == "template" && strings.Contains(task.name, "connection facts") && !strings.HasPrefix(task.text, ch.scope.ConnectionsDir) {
					t.Fatalf("role %s writes its fragment to %q, not this run's %q", role, task.text, ch.scope.ConnectionsDir)
				}
			}
		})
	}
}

// T-C07-5b: the first install still gets admin.conf — the default is chosen by
// the generator, not by a role.
func TestC07_InstallScopeIsTheAdminConfigTheInstallCreates(t *testing.T) {
	ch := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"), nil, nil)
	if ch.scope.Kubeconfig != DefaultKubeconfigPath {
		t.Fatalf("the first install scope is %q", ch.scope.Kubeconfig)
	}
	if ch.scope.ConnectionsDir != filepath.Join(runtimeBaseDir, "ani-lab", "work", connectionsDirName) {
		t.Fatalf("the first install aggregates fragments from %q", ch.scope.ConnectionsDir)
	}
	tasks := c07RenderRole(t, ch, "nats")
	for _, task := range tasks {
		if strings.Contains(task.text, "KUBECONFIG=") && !strings.Contains(task.text, fmt.Sprintf("KUBECONFIG=%q", DefaultKubeconfigPath)) {
			t.Fatalf("install-scope task %q pins something other than the admin config: %s", task.name, task.text)
		}
	}
}

// ---------------------------------------------------------------------------
// T-C07-6: the pinned context is the endpoint that gets used, and the other one
// is never touched.
// ---------------------------------------------------------------------------

// c07Endpoint is an isolated API stand-in that records every request it gets.
type c07Endpoint struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
}

func (e *c07Endpoint) hits() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.paths...)
}

func newC07Endpoint(t *testing.T, name string) *c07Endpoint {
	t.Helper()
	e := &c07Endpoint{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.paths = append(e.paths, r.Method+" "+r.URL.Path)
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
	}))
	t.Cleanup(e.server.Close)
	return e
}

// c07Kubeconfig writes a dummy config whose only server is the given endpoint.
func c07Kubeconfig(t *testing.T, dir, name, server string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: %s\n  cluster:\n    server: %s\ncontexts:\n- name: %s\n  context:\n    cluster: %s\n    user: %s\ncurrent-context: %s\nusers:\n- name: %s\n  user:\n    token: dummy\n",
		name, server, name, name, name, name, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// c07ShimDir builds a kubectl that does one real HTTP request against whatever
// server the kubeconfig it was handed names. It cannot invent a target: with no
// KUBECONFIG it fails loudly instead of resolving $HOME.
func c07Shim(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	shim := `#!/usr/bin/env bash
set -euo pipefail
if [ -z "${KUBECONFIG:-}" ]; then echo "SHIM: no KUBECONFIG in the environment" >&2; exit 9; fi
if [ ! -f "$KUBECONFIG" ]; then echo "SHIM: kubeconfig $KUBECONFIG does not exist" >&2; exit 8; fi
server=$(sed -n 's/^[[:space:]]*server:[[:space:]]*\(.*\)$/\1/p' "$KUBECONFIG" | head -1)
if [ -z "$server" ]; then echo "SHIM: kubeconfig $KUBECONFIG names no server" >&2; exit 7; fi
python3 - "$server" "$KUBECONFIG" "$@" <<'PY'
import sys, urllib.request, urllib.parse
server, kubeconfig = sys.argv[1], sys.argv[2]
path = "/probe?kc=" + kubeconfig.split("/")[-1] + "&argv=" + urllib.parse.quote(" ".join(sys.argv[3:]))
urllib.request.urlopen(server + path, timeout=10).read()
PY
echo '{}'
`
	if err := os.WriteFile(path, []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
}

func c07ShimDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bin")
	c07Shim(t, filepath.Join(dir, "kubectl"))
	return dir
}

func TestC07_ThePinnedContextDecidesWhichEndpointIsContactedAtAll(t *testing.T) {
	// Two runs of the same role text, two mutually exclusive contexts. Each gets
	// its own endpoints: "the other cluster was never contacted" is only evidence
	// if nothing else wrote to that endpoint beforehand.
	for _, pinFirst := range []bool{true, false} {
		name, label := "components run pinned to B", "B"
		if pinFirst {
			name, label = "components run pinned to A", "A"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			a := newC07Endpoint(t, "A")
			b := newC07Endpoint(t, "B")
			confA := c07Kubeconfig(t, dir, "cluster-A.conf", a.server.URL)
			confB := c07Kubeconfig(t, dir, "cluster-B.conf", b.server.URL)
			shim := c07ShimDir(t)
			tc := struct {
				pinned    string
				other     *c07Endpoint
				pinnedEnd *c07Endpoint
			}{confB, a, b}
			if pinFirst {
				tc = struct {
					pinned    string
					other     *c07Endpoint
					pinnedEnd *c07Endpoint
				}{confA, b, a}
			}
			ch := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"),
				&RunScope{Kubeconfig: tc.pinned,
					LogsDir:        filepath.Join(dir, "logs"),
					ConnectionsDir: filepath.Join(dir, "work", "connections.d"),
					RunID:          "run"},
				map[string]any{"nats": true})
			if ch.scope.Kubeconfig != tc.pinned {
				t.Fatalf("the run did not carry the pinned config")
			}
			// The roles call Helm by its absolute artifact path
			// ({{ .ani.artifact_root }}/bin/helm), so the stand-in belongs where
			// the rendered text says the binary is — the same target selection
			// kubectl gets from PATH.
			c07Shim(t, filepath.Join(ch.root, "bin", "helm"))
			ran := 0
			for _, task := range c07RenderRole(t, ch, "nats") {
				for _, line := range strings.Split(task.text, "\n") {
					trimmed := strings.TrimSpace(line)
					if !strings.HasPrefix(trimmed, "KUBECONFIG=") || !c07ClusterCall(trimmed) {
						continue
					}
					cmd := exec.Command("bash", "-c", trimmed)
					cmd.Env = []string{"PATH=" + shim + ":/usr/bin:/bin", "HOME=" + dir}
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("the rendered call %q did not run: %v\n%s", trimmed, err, out)
					}
					// A pipe is two invocations of the same pinned context.
					ran += c07CallCount(trimmed)
				}
			}
			if ran == 0 {
				t.Fatal("the nats role rendered no directly executable kubectl call")
			}
			if got := len(tc.pinnedEnd.hits()); got != ran {
				t.Fatalf("the endpoint pinned for run %s saw %d requests for %d rendered cluster calls (%v)",
					label, got, ran, tc.pinnedEnd.hits())
			}
			if hits := tc.other.hits(); len(hits) != 0 {
				t.Fatalf("the other endpoint was contacted %d times without being asked to: %v", len(hits), hits)
			}
		})
	}
}

// T-C07-7: a context that is not on the host doing the writing fails before the
// first write, and the run does not fall back to the installer's admin config.
func TestC07_AKubeconfigMissingOnTheWritingHostFailsBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.conf")
	ch := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"),
		&RunScope{Kubeconfig: missing,
			LogsDir:        filepath.Join(dir, "logs"),
			ConnectionsDir: filepath.Join(dir, "work", "connections.d"),
			RunID:          "run"},
		map[string]any{"nats": true})
	// The chain created the file so the run could decide it; now it disappears —
	// which is the SSH-host case: the installer node could read the file, the
	// host about to write cannot. The role guard, not the entry point, must stop it.
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the test expected the pinned config to be absent")
	}
	tasks := c07RenderRole(t, ch, "nats")
	guard := tasks[0]
	if !strings.Contains(guard.name, "execution context") {
		t.Fatalf("the first task of the role is %q, not the context guard; the guard is no longer first", guard.name)
	}
	cmd := exec.Command("bash", "-c", guard.text)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "KUBECONFIG=" + filepath.Join(dir, "inherited.conf")}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the guard accepted a kubeconfig that is not on this host:\n%s", out)
	}
	if !strings.Contains(string(out), "does not exist on this host") {
		t.Fatalf("the guard failed for a different reason than the missing context:\n%s", out)
	}
	if entries, _ := os.ReadDir(ch.scope.ConnectionsDir); len(entries) != 0 {
		t.Fatalf("the failed guard still left %d files in the run's fragment directory", len(entries))
	}
	// And the failure must not be "resolved" by guessing the installer default.
	if strings.Contains(string(out), DefaultKubeconfigPath) {
		t.Fatalf("the guard fell back to %s: %s", DefaultKubeconfigPath, out)
	}
}

// ---------------------------------------------------------------------------
// T-C07-8: write and aggregate use one directory, and it is this run's.
// ---------------------------------------------------------------------------

func TestC07_AComponentsRunWritesAndReadsOnlyItsOwnFragments(t *testing.T) {
	base := t.TempDir()
	// The base install's real tree: its record, its aggregated document, and one
	// fragment from the install that must stay untouched and unused.
	baseScope := InstallRunScope("ani-lab")
	baseScope = RunScope{Kubeconfig: baseScope.Kubeconfig,
		LogsDir:        filepath.Join(base, "base-logs"),
		ConnectionsDir: filepath.Join(base, "base-work", "connections.d")}
	if err := os.MkdirAll(baseScope.ConnectionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := "# nats facts from the base install\nold endpoint\n"
	stalePath := filepath.Join(baseScope.ConnectionsDir, "nats.md")
	if err := os.WriteFile(stalePath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	baseDoc := filepath.Join(base, "connections.md")
	if err := writeConnections(baseDoc, baseScope.ConnectionsDir, []ComponentRow{{Name: "nats", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	baseRecord := filepath.Join(base, "run.json")
	if err := os.WriteFile(baseRecord, []byte(`{"runId":"base-install"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeStale, beforeDoc, beforeRecord := c07Digest(t, stalePath), c07Digest(t, baseDoc), c07Digest(t, baseRecord)

	runDir := filepath.Join(t.TempDir(), "components-run-1")
	conf := filepath.Join(runDir, "cluster.conf")
	ch := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"),
		&RunScope{Kubeconfig: conf, LogsDir: filepath.Join(runDir, "logs"),
			ConnectionsDir: filepath.Join(runDir, "work", connectionsDirName), RunID: "run-1"},
		map[string]any{"nats": true})
	if err := os.MkdirAll(ch.scope.ConnectionsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Where does the packaged role actually put its fragment? Read it out of the
	// rendered task, not out of a path this test chose.
	var fragmentDest string
	for _, task := range c07RenderRole(t, ch, "nats") {
		if task.module == "template" && strings.Contains(task.name, "connection facts") {
			fragmentDest = task.text
		}
	}
	if fragmentDest == "" {
		t.Fatal("the nats role has no rendered connection-facts fragment task")
	}
	if filepath.Dir(fragmentDest) != ch.scope.ConnectionsDir {
		t.Fatalf("the role writes %q while the run aggregates from %q", fragmentDest, ch.scope.ConnectionsDir)
	}
	if filepath.Dir(fragmentDest) == baseScope.ConnectionsDir {
		t.Fatal("the components run writes into the base install's fragment directory")
	}
	if filepath.Dir(fragmentDest) == filepath.Join(runtimeBaseDir, "ani-lab", "work", connectionsDirName) {
		t.Fatal("the components run still targets the canonical install directory")
	}

	fresh := "# nats facts from components run-1\nnew endpoint\n"
	if err := os.WriteFile(fragmentDest, []byte(fresh), 0o600); err != nil {
		t.Fatal(err)
	}
	runDoc := filepath.Join(runDir, "connections.md")
	if err := writeConnections(runDoc, ch.scope.ConnectionsDir, []ComponentRow{{Name: "nats", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(runDoc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document), "new endpoint") || strings.Contains(string(document), "old endpoint") {
		t.Fatalf("the run's document is not assembled from this run's fragments:\n%s", document)
	}
	for path, before := range map[string]string{stalePath: beforeStale, baseDoc: beforeDoc, baseRecord: beforeRecord} {
		if got := c07Digest(t, path); got != before {
			t.Fatalf("the components run changed the base install's %s (%s -> %s)", filepath.Base(path), before, got)
		}
	}

	// The missing-fragment rule: with this run's fragment gone, aggregation must
	// fail — the older base fragment is not a substitute.
	if err := os.Remove(fragmentDest); err != nil {
		t.Fatal(err)
	}
	if err := writeConnections(filepath.Join(runDir, "second.md"), ch.scope.ConnectionsDir, []ComponentRow{{Name: "nats", Enabled: true}}); err == nil {
		t.Fatal("aggregation succeeded without this run's fragment")
	}
	if c07Digest(t, stalePath) != beforeStale {
		t.Fatal("the refused aggregation still touched the base fragment")
	}
}

func c07Digest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable:" + err.Error()
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// T-C07-5c: the checker's release path is part of the same run context, so the
// role has to hand it the run's own executable rather than letting the checker
// resolve `kk` from a PATH that differs between the installer node and an SSH one.
func TestC07_TheCheckerIsGivenTheRunsOwnExecutable(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "cluster.conf")
	scope := RunScope{Kubeconfig: conf, LogsDir: filepath.Join(dir, "logs"),
		ConnectionsDir: filepath.Join(dir, "work", connectionsDirName), RunID: "run-1",
		KKBinary: filepath.Join(dir, "bin", "kk")}
	ch := c07NewChain(t, c07SiteYAML(true, true, true, true, "loki"), &scope, map[string]any{"fluent-bit": true})
	var checker string
	for _, task := range c07RenderRole(t, ch, "fluent-bit") {
		if strings.Contains(task.text, "verify.sh") && strings.Contains(task.text, "bash") {
			checker = task.text
		}
	}
	if checker == "" {
		t.Fatal("the fluent-bit role never runs the packaged checker")
	}
	for _, want := range []string{
		fmt.Sprintf("ANI_KK_BIN=%q", scope.KKBinary),
		fmt.Sprintf("ANI_VERIFY_KUBECONFIG=%q", scope.Kubeconfig),
		fmt.Sprintf("ANI_VERIFY_OUTPUT_DIR=%q", scope.LogsDir),
	} {
		if !strings.Contains(checker, want) {
			t.Fatalf("the checker invocation does not carry %s, so the run's context does not reach it:\n%s", want, checker)
		}
	}
	if strings.Contains(checker, "kk ani") && !strings.Contains(checker, "ANI_KK_BIN") {
		t.Fatal("the role calls kk by bare name instead of the run's own path")
	}
}

// T-C07-9: the scope itself refuses to be undecided twice.
// ---------------------------------------------------------------------------

func TestC07_OneRunCannotDecideItsContextTwice(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "A.conf")
	if err := os.WriteFile(conf, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := RunScope{Kubeconfig: conf, LogsDir: filepath.Join(dir, "logs"), ConnectionsDir: filepath.Join(dir, "work", "connections.d")}
	siteFile := c07WriteSite(t, dir, c07SiteYAML(true, true, true, true, "loki"))
	cluster, err := LoadClusterConfig(siteFile)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := KubeKeyConfig(cluster, filepath.Join(dir, "artifact.tgz"), dir, testImageTable())
	if err != nil {
		t.Fatal(err)
	}
	// The generator already published one scope.
	ani, _ := spec["ani"].(map[string]any)
	if existing, ok := ani[specKey].(map[string]any); !ok || len(existing) == 0 {
		t.Fatal("KubeKeyConfig published no run scope; Apply/Replace would be testing nothing")
	}
	if err := good.Apply(spec); err == nil {
		t.Fatal("a second Apply was accepted: one run would have two contexts")
	}
	// A components run legitimately re-decides it, and only that.
	if err := good.Replace(spec); err != nil {
		t.Fatalf("Replace of a generator-supplied scope failed: %v", err)
	}
	if err := (RunScope{}).Replace(map[string]any{"ani": map[string]any{}}); err == nil {
		t.Fatal("Replace succeeded on a spec with no scope, hiding a generator that never built one")
	}
	// Missing, relative and directory kubeconfigs are refused before a write.
	for name, bad := range map[string]RunScope{
		"empty":     {Kubeconfig: "", LogsDir: "/l", ConnectionsDir: "/c"},
		"relative":  {Kubeconfig: "relative.conf", LogsDir: "/l", ConnectionsDir: "/c"},
		"directory": {Kubeconfig: dir, LogsDir: "/l", ConnectionsDir: "/c"},
		"missing":   {Kubeconfig: filepath.Join(dir, "gone.conf"), LogsDir: "/l", ConnectionsDir: "/c"},
		"no-logs":   {Kubeconfig: conf, LogsDir: "", ConnectionsDir: "/c"},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("Validate accepted the %s kubeconfig scope", name)
		}
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate refused a usable scope: %v", err)
	}
}

func c07WriteSite(t *testing.T, dir, site string) string {
	t.Helper()
	path := filepath.Join(dir, "site.yaml")
	if err := os.WriteFile(path, []byte(site), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
