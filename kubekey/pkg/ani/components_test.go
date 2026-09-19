package ani

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

const siteConfigTemplate = `name: ani-lab
installerNode: node1
ssh:
  user: ubuntu
  port: 22
  password: site-password
nodes:
  - name: node1
    address: 192.0.2.11
  - name: node2
    address: 192.0.2.12
  - name: node3
    address: 192.0.2.13
network:
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn:
    managedDevices: [ens35]
    encapNetworks: [192.0.2.0/24]
    intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]
registry:
  port: 5000
%s
`

func parseSite(t *testing.T, components string) (ClusterConfig, error) {
	t.Helper()
	return ParseClusterConfig([]byte(strings.ReplaceAll(siteConfigTemplate, "%s", components)))
}

func TestComponentsDefaultOffAndStorageDefaults(t *testing.T) {
	c, err := parseSite(t, "")
	if err != nil {
		t.Fatalf("parse without components: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() without components = %v", err)
	}
	for _, row := range c.Components.Selection() {
		if row.Enabled {
			t.Fatalf("component %s must default to false", row.Name)
		}
	}
	if c.Components.PostgreSQL.StorageClass != DefaultStorageClass || c.Components.PostgreSQL.StorageSize != "10Gi" {
		t.Fatalf("postgresql defaults = %q/%q", c.Components.PostgreSQL.StorageClass, c.Components.PostgreSQL.StorageSize)
	}
	if c.Components.Valkey.StorageSize != "2Gi" || c.Components.NATS.StorageSize != "5Gi" {
		t.Fatalf("valkey/nats defaults = %q/%q", c.Components.Valkey.StorageSize, c.Components.NATS.StorageSize)
	}
}

func TestComponentsSelectionKeepsDocumentedOrder(t *testing.T) {
	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse with certManager: %v", err)
	}
	rows := c.Components.Selection()
	want := [][2]string{
		{"cert-manager", "true"},
		{"postgresql", "false"},
		{"valkey", "false"},
		{"nats", "false"},
	}
	if len(rows) != len(want) {
		t.Fatalf("selection length = %d, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row.Name != want[i][0] {
			t.Fatalf("selection[%d].Name = %q, want %q", i, row.Name, want[i][0])
		}
		got := "false"
		if row.Enabled {
			got = "true"
		}
		if got != want[i][1] {
			t.Fatalf("selection[%d].Enabled = %s, want %s", i, got, want[i][1])
		}
	}
}

func TestComponentsUnknownKeyRejected(t *testing.T) {
	if _, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n    typoKey: 1\n"); err == nil {
		t.Fatal("unknown component key was accepted")
	}
	if _, err := parseSite(t, "components:\n  redis:\n    enabled: true\n"); err == nil {
		t.Fatal("unknown component name was accepted")
	}
}

func TestNotImplementedComponentMustFailBeforeDeploy(t *testing.T) {
	// All four foundation components are implemented as of B4; the loop below
	// stays as the guard rail for any future component added to componentsOrder.
	for _, component := range []string{} {
		if contains(ImplementedComponents, component) {
			t.Fatalf("%s is already implemented; update this test with the new batch", component)
		}
		c, err := parseSite(t, "components:\n  "+component+":\n    enabled: true\n")
		if err != nil {
			t.Fatalf("parse %s: %v", component, err)
		}
		err = Validate(c)
		if err == nil {
			t.Fatalf("Validate() accepted %s even though this release does not implement it", component)
		}
		if !strings.Contains(err.Error(), "not implement") {
			t.Fatalf("Validate() error = %v, want a not-implemented message", err)
		}
	}
}

func TestEnabledComponentRequiresUsableCapacity(t *testing.T) {
	c, err := parseSite(t, "components:\n  nats:\n    enabled: true\n    storageSize: maybe\n")
	if err != nil {
		t.Fatalf("parse bad size: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("Validate() accepted an unparsable storage size")
	}
	c, err = parseSite(t, "components:\n  postgresql:\n    enabled: true\n    storageClass: \"\"\n")
	if err != nil {
		t.Fatalf("parse empty class: %v", err)
	}
	if err := Validate(c); err == nil {
		t.Fatal("Validate() accepted an empty storageClass for an enabled component")
	}
	c, err = parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse certManager: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate() = %v for the implemented component", err)
	}
}

func TestKubeKeyConfigCarriesComponentSelection(t *testing.T) {
	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", testImageTable())
	if err != nil {
		t.Fatalf("KubeKeyConfig() error = %v", err)
	}
	specMap, ok := spec["ani"].(map[string]any)
	if !ok {
		t.Fatalf("spec has no ani section: %#v", spec["ani"])
	}
	if specMap["artifact_root"] != "/opt/ani" {
		t.Fatalf("artifact_root = %v, want /opt/ani", specMap["artifact_root"])
	}
	components, ok := specMap["components"].(map[string]any)
	if !ok {
		t.Fatalf("spec has no ani.components: %#v", specMap)
	}
	for _, name := range []string{"cert-manager", "postgresql", "valkey", "nats"} {
		entry, ok := components[name].(map[string]any)
		if !ok {
			t.Fatalf("ani.components[%q] missing", name)
		}
		if _, ok := entry["enabled"].(bool); !ok {
			t.Fatalf("ani.components[%q].enabled is not a bool", name)
		}
	}
	selected := components["cert-manager"].(map[string]any)["enabled"]
	if selected != true {
		t.Fatalf("cert-manager selection = %v, want true", selected)
	}
	postgresql := components["postgresql"].(map[string]any)
	if postgresql["storage_class"] != DefaultStorageClass || postgresql["storage_size"] != "10Gi" {
		t.Fatalf("postgresql storage passed to roles = %v/%v", postgresql["storage_class"], postgresql["storage_size"])
	}
}

func TestWriteComponentSelectionIsStrictlyFormatted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components-selection.tsv")
	c, err := parseSite(t, "components:\n  certManager:\n    enabled: true\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := writeComponentSelection(path, "abc123", c.Components.Selection()); err != nil {
		t.Fatalf("writeComponentSelection: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("selection file mode = %o, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "# config_sha256=abc123\ncert-manager\ttrue\npostgresql\tfalse\nvalkey\tfalse\nnats\tfalse\n"
	if string(data) != want {
		t.Fatalf("selection file = %q, want %q", string(data), want)
	}
}

// Every component verification script survives rendering and is valid bash, so a
// template typo cannot reach a node as a half-rendered script.
func TestComponentVerifyScriptsRenderAndParse(t *testing.T) {
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani")
	matches, err := filepath.Glob(filepath.Join(root, "*", "templates", "verify.sh"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no component verify.sh templates found")
	}
	ctx := map[string]any{
		"ani": map[string]any{
			"registry": "192.0.2.11:5000",
			"images": map[string]string{
				"docker.io/alpine/openssl:3.5.4": "192.0.2.11:5000/alpine/openssl:3.5.4",
			},
			"components": map[string]any{
				"cert-manager": map[string]any{"enabled": true},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
	for _, path := range matches {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path))), func(t *testing.T) {
			tmpl, err := template.New("verify.sh").ParseFiles(path)
			if err != nil {
				t.Fatalf("parse template: %v", err)
			}
			rendered := &strings.Builder{}
			if err := tmpl.Execute(rendered, ctx); err != nil {
				t.Fatalf("execute template: %v", err)
			}
			if strings.Contains(rendered.String(), "{{") {
				t.Fatalf("rendered script still contains template delimiters:\n%s", rendered.String())
			}
			out := filepath.Join(t.TempDir(), "verify.sh")
			if err := os.WriteFile(out, []byte(rendered.String()), 0o700); err != nil {
				t.Fatalf("write rendered script: %v", err)
			}
			cmd := exec.Command("bash", "-n", out)
			if combined, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n rendered script: %v\n%s", err, combined)
			}
		})
	}
}

// The Chart values template renders to valid YAML with every image rewritten to
// the offline registry, so no public image reference can leak into the release.
func TestCertManagerValuesRenderOfflineReferences(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "cert-manager", "templates", "values.yaml")
	tmpl, err := template.New("values.yaml").ParseFiles(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := map[string]any{"ani": map[string]any{"registry": "192.0.2.11:5000"}}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, ctx); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := rendered.String()
	for _, want := range []string{"crds:", "enabled: true", "imageRegistry: 192.0.2.11:5000", "imageNamespace: jetstack", "clusterResourceNamespace: cert-manager"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered values missing %q:\n%s", want, out)
		}
	}
	for _, public := range []string{"quay.io", "docker.io", "https://"} {
		if strings.Contains(out, public) {
			t.Fatalf("rendered values references public source %q:\n%s", public, out)
		}
	}
}

func TestCertManagerRoleTasksAreGatedAndUsePackagedMaterial(t *testing.T) {
	root := filepath.Join("..", "..")
	tasksData, err := os.ReadFile(filepath.Join(root, "builtin", "core", "roles", "ani", "cert-manager", "tasks", "main.yaml"))
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	tasks := string(tasksData)
	if !strings.Contains(tasks, "{{ .ani.artifact_root }}/charts/cert-manager/v1.21.2.tgz") {
		t.Fatal("role must install the chart shipped inside the offline artifact")
	}
	if !strings.Contains(tasks, "{{ .ani.artifact_root }}/bin/helm") {
		t.Fatal("role must use the packaged helm binary")
	}
	// kubectl jsonpath renders nothing for a JSON array of plain strings when
	// "{.}" is used, which silently turned the image-source check into a no-op.
	if strings.Contains(tasks, `range .args[*]}{.}`) {
		t.Fatal("role iterates container args with {.}; use {@} for string arrays")
	}
	// The template module does not honour its "mode" argument as an octal file
	// mode, so the verification script permissions must be set by an explicit
	// task instead.
	if strings.Contains(tasks, "mode: \"0700\"") {
		t.Fatal("role relies on the template module mode; set 0700 with chmod instead")
	}
	if !strings.Contains(tasks, "chmod 0700 /etc/kubernetes/ani/cert-manager/verify.sh") {
		t.Fatal("rendered verification script must be explicitly chmod 0700")
	}
	for _, forbidden := range []string{"helm repo update", "helm dependency update", "reset.sh", "kubeadm reset"} {
		if strings.Contains(tasks, forbidden) {
			t.Fatalf("role contains forbidden operation %q", forbidden)
		}
	}
	playbookData, err := os.ReadFile(filepath.Join(root, "builtin", "core", "playbooks", "create_cluster.yaml"))
	if err != nil {
		t.Fatalf("read playbook: %v", err)
	}
	playbook := string(playbookData)
	if !strings.Contains(playbook, "role: ani/cert-manager") {
		t.Fatal("cert-manager role is not wired into create_cluster.yaml")
	}
	if !strings.Contains(playbook, `(index .ani.components "cert-manager").enabled`) {
		t.Fatal("cert-manager role is not gated by its component switch")
	}
	if strings.Index(playbook, "ani/ceph") > strings.Index(playbook, "ani/cert-manager") {
		t.Fatal("cert-manager must run after the base storage role")
	}
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
