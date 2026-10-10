package ani

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const lwsStandaloneSite = `name: ani-lab
installerNode: node1
ssh: {user: ubuntu, port: 22, password: test-only}
nodes:
  - {name: node1, address: 192.0.2.11}
  - {name: node2, address: 192.0.2.12}
  - {name: node3, address: 192.0.2.13}
network:
  stack: kcn
  managementInterface: ens34
  podCIDR: 10.16.0.0/16
  serviceCIDR: 10.96.0.0/16
  kcn: {managedDevices: [], encapNetworks: [192.0.2.0/24], intranetNetworks: [192.0.2.0/24, 10.96.0.0/16]}
registry: {port: 5000}
components:
  lws: {enabled: true}
`

func TestLWSStandaloneSiteSelection(t *testing.T) {
	c, err := ParseClusterConfig([]byte(lwsStandaloneSite))
	if err != nil {
		t.Fatalf("standalone LWS site must parse: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("LWS must work without Kubeflow or persistent storage: %v", err)
	}
	selected := []string{}
	for _, row := range c.EffectiveComponents().Selection() {
		if row.Enabled {
			selected = append(selected, row.Name)
		}
	}
	if strings.Join(selected, ",") != "lws" {
		t.Fatalf("standalone selection = %v, want only lws", selected)
	}
}

func TestLWSProductionRenderRequiresLockedController(t *testing.T) {
	c, err := ParseClusterConfig([]byte(lwsStandaloneSite))
	if err != nil {
		t.Fatal(err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		if file.Role == "lws" && file.Rel == "lws/tasks/main.yaml" {
			found = true
		}
	}
	if !found {
		t.Fatal("selected LWS has no production install role")
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
	table := r08FullTable(t)
	delete(table, "registry.k8s.io/lws/lws:v0.10.0")
	if _, err := KubeKeyConfig(c, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "lws:v0.10.0") {
		t.Fatalf("missing approved LWS image must fail before install: %v", err)
	}
}

// Only external Helm/Kubernetes processes are replaced. The production
// renderer, material approval and install/preflight/exit handling all run.
func TestLWSInstallStopsBeforeMutatingSharedObjects(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		code    int
		message string
	}{{"foreign-crd", 1, "pre-existing LWS object"}, {"api-failure", 67, "preserved external API error"}, {"absent-namespace", 1, "pre-existing LWS object"}} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			c, err := ParseClusterConfig([]byte(lwsStandaloneSite))
			if err != nil {
				t.Fatal(err)
			}
			files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, root, r08FullTable(t))
			if err != nil {
				t.Fatal(err)
			}
			var driver []byte
			for _, file := range files {
				if file.Rel == "lws/templates/manage.py" {
					driver = file.Rendered
				}
			}
			if len(driver) == 0 {
				t.Fatal("production LWS driver missing")
			}
			if err := os.MkdirAll(filepath.Join(root, "charts", "lws"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			chart, err := os.ReadFile(filepath.Join("..", "..", "ani", "charts", "lws", "lws-v0.10.0.tgz"))
			if err != nil {
				t.Fatal(err)
			}
			write := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, path), []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			write("charts/lws/v0.10.0.tgz", string(chart), 0o600)
			write("driver.py", string(driver), 0o700)
			write("bin/helm", `#!/usr/bin/env python3
import json
print(json.dumps({'kind':'CustomResourceDefinition','metadata':{'name':'leaderworkersets.leaderworkerset.x-k8s.io'},'spec':{'versions':[{'name':'v1','served':True}]}}))
print('---')
print(json.dumps({'kind':'Deployment','metadata':{'name':'lws-controller-manager'},'spec':{'template':{'spec':{'containers':[{'image':'192.0.2.11:5000/lws/lws:v0.10.0@sha256:1008a2a66ef8cacc22e5ec1393eb267d7d131dcfd936e5fe7c5969339d5d05c8'}]}}}}))
`, 0o700)
			write("bin/kubectl", `#!/usr/bin/env python3
import json,os,pathlib,sys
args=sys.argv[1:]
if 'get' not in args:
 pathlib.Path(os.environ['LWS_WRITES']).write_text('mutation occurred')
 sys.exit(90)
if os.environ['LWS_SCENARIO']=='api-failure':
 sys.stderr.write('preserved external API error\n')
 sys.exit(67)
if 'CustomResourceDefinition' in args:
 print(json.dumps({'metadata':{'name':'leaderworkersets.leaderworkerset.x-k8s.io','labels':{'ani.io/managed-by':'foreign'}}}))
elif 'secret' in args:
 if os.environ['LWS_SCENARIO']!='absent-namespace':
  print(json.dumps({'items':[]}))
`, 0o700)
			cmd := exec.Command("python3", filepath.Join(root, "driver.py"), "install")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "ANI_RUN_KUBECONFIG="+filepath.Join(root, "kubeconfig"), "LWS_SCENARIO="+scenario.name, "LWS_WRITES="+filepath.Join(root, "writes"))
			output, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != scenario.code {
				t.Fatalf("install exit=%v, want %d: %s", err, scenario.code, output)
			}
			if !strings.Contains(string(output), scenario.message) {
				t.Fatalf("install did not reach the expected ownership/API decision: %s", output)
			}
			if _, err := os.Stat(filepath.Join(root, "writes")); !os.IsNotExist(err) {
				t.Fatal("failed preflight mutated the cluster")
			}
		})
	}
}

// kubectl --ignore-not-found can emit no JSON for an empty collection on
// Kubernetes 1.35. The lifecycle check must treat that as an empty list while
// preserving a missing named object and real API failures as distinct results.
func TestLWSKubectlEmptyListsAndAPIFailure(t *testing.T) {
	root := t.TempDir()
	c, err := ParseClusterConfig([]byte(lwsStandaloneSite))
	if err != nil {
		t.Fatal(err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, root, r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Rel == "lws/templates/manage.py" {
			if err := os.WriteFile(filepath.Join(root, "driver.py"), file.Rendered, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nif [ \"$LWS_EMPTY_API_FAILURE\" = yes ]; then echo 'preserved external API error' >&2; exit 67; fi\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "kubectl"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	script := `import importlib.util,os,subprocess,sys
s=importlib.util.spec_from_file_location('driver',sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
assert m.get('pods',namespace='owned-probe')['items']==[]
assert m.get('statefulsets',namespace='owned-probe')['items']==[]
assert m.get('namespace','absent') is None
os.environ['LWS_EMPTY_API_FAILURE']='yes'
try: m.get('pods',namespace='owned-probe')
except subprocess.CalledProcessError as e: assert e.returncode==67
else: raise AssertionError('API failure was converted to an empty list')
`
	cmd := exec.Command("python3", "-c", script, filepath.Join(root, "driver.py"))
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "ANI_RUN_KUBECONFIG="+filepath.Join(root, "kubeconfig"))
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("empty-list lifecycle protocol: %v\n%s", err, raw)
	}
}
