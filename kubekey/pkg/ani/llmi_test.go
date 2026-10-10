package ani

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKubeflowLLMCombinationDerivesLWSWithoutExtraSwitches(t *testing.T) {
	c := kubeflowTestConfig()
	c.Kubeflow.Release = "26.10-kubeflow-llm-v1"
	if err := Validate(c); err != nil {
		t.Fatalf("the new Standard plus LLMI combination must validate: %v", err)
	}
	selected := map[string]bool{}
	for _, row := range c.EffectiveComponents().Selection() {
		selected[row.Name] = row.Enabled
	}
	for _, name := range []string{"cert-manager", "lws", "kserve"} {
		if !selected[name] {
			t.Fatalf("Kubeflow combination omitted derived %s", name)
		}
	}
	if c.Components.LWS.Enabled || c.Components.CertManager.Enabled {
		t.Fatal("dependency resolution mutated the user's declared switches")
	}
	manifest, err := BuildRunManifest(c)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]bool{}
	for _, name := range manifest.Components {
		actual[name] = true
	}
	for _, name := range []string{"lws", "kserve", "kubeflow"} {
		if !actual[name] {
			t.Fatalf("formal run identity omits %s", name)
		}
	}
	c.Kubeflow.Release = KubeflowRelease
	for _, row := range c.EffectiveComponents().Selection() {
		if (row.Name == "lws" || row.Name == "kserve") && row.Enabled {
			t.Fatal("old release meaning silently acquired the new LLM combination")
		}
	}
}

func TestLLMIValidateDisplaysResolvedDependencySources(t *testing.T) {
	c := kubeflowTestConfig()
	c.Kubeflow.Release = KubeflowLLMRelease
	raw, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunValidate(context.Background(), ValidateInput{ConfigFile: path}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lws,kserve,kubeflow", "dependency sources:", `"kserve":["kubeflow.release=26.10-kubeflow-llm-v1"]`, `"lws":["kserve"]`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("validation omitted resolved combination/dependency %s: %s", want, out.String())
		}
	}
}

func llmiRendered(t *testing.T, root string) map[string][]byte {
	t.Helper()
	c := kubeflowTestConfig()
	c.Kubeflow.Release = KubeflowLLMRelease
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, root, r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, file := range files {
		out[file.Rel] = file.Rendered
	}
	return out
}

// Use the shipped Helm and both official archives; no Kubernetes API is
// replaced or called by this resource-boundary check.
func TestLLMIOfficialChartsPreserveStandardAndExcludeGIE(t *testing.T) {
	helm := os.Getenv("ANI_LLMI_HELM_BIN")
	if helm == "" {
		t.Skip("official LLMI chart check requires the packaged ANI_LLMI_HELM_BIN")
	}
	c := kubeflowTestConfig()
	c.Kubeflow.Release = KubeflowLLMRelease
	rendered, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	withoutFilter := make([]RenderedFile, 0, len(rendered))
	for _, file := range rendered {
		if file.Rel != "kserve/templates/filter.py" {
			withoutFilter = append(withoutFilter, file)
		}
	}
	err = RunChartExpansion(context.Background(), ChartExpansionInput{HelmBin: helm, ChartsRoot: t.TempDir(), LockPath: filepath.Join("..", "..", "ani", "components.lock.yaml")}, c, withoutFilter, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no bounded LLMI post-renderer") {
		t.Fatalf("a new combination with no LLMI filter must fail before chart expansion: %v", err)
	}
	dir := t.TempDir()
	files := llmiRendered(t, dir)
	values := filepath.Join(dir, "values.yaml")
	filter := filepath.Join(dir, "filter.py")
	for path, data := range map[string][]byte{values: files["kserve/templates/values.yaml"], filter: files["kserve/templates/filter.py"]} {
		if len(data) == 0 {
			t.Fatal("production LLMI role omitted its values/filter")
		}
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	chart := filepath.Join("..", "..", "ani", "charts", "kserve-llmisvc-resources", "kserve-llmisvc-resources-v0.17.0.tgz")
	cmd := exec.Command(helm, "template", "ani-kserve-llmi", chart, "--namespace", "kserve", "--values", values, "--post-renderer", filter)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("official LLMI resources/filter: %v\n%s", err, raw)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	count, deployments := 0, 0
	for {
		var obj map[string]interface{}
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(obj) == 0 {
			continue
		}
		count++
		kind, _ := obj["kind"].(string)
		metadata := obj["metadata"].(map[string]interface{})
		name := metadata["name"].(string)
		if kind == "CustomResourceDefinition" || kind == "ConfigMap" || name == "kserve-controller-manager" {
			t.Fatalf("LLMI resources would install a shared/Standard/GIE object: %s/%s", kind, name)
		}
		if kind == "Certificate" {
			issuer := obj["spec"].(map[string]interface{})["issuerRef"].(map[string]interface{})
			if issuer["name"] != "ani-ca" || issuer["kind"] != "ClusterIssuer" {
				t.Fatalf("LLMI webhook must reuse the installed CA without an excluded namespaced Issuer: %v", issuer)
			}
		}
		if kind == "Deployment" {
			deployments++
			if name != "llmisvc-controller-manager" {
				t.Fatalf("unexpected LLMI manager %s", name)
			}
			yamlBytes, err := yaml.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(yamlBytes), "@"+KServeLLMAMD64ManifestDigest) {
				t.Fatal("official LLMI chart lost the approved target-platform image digest")
			}
		}
	}
	if count != 11 || deployments != 1 {
		t.Fatalf("bounded LLMI resources: objects=%d managers=%d", count, deployments)
	}
	for name, data := range files {
		if strings.HasPrefix(name, "kserve/") && strings.HasSuffix(name, ".py") {
			cmd := exec.Command("python3", "-c", "import sys; compile(sys.stdin.read(), sys.argv[1], 'exec')", name)
			cmd.Stdin = bytes.NewReader(data)
			if raw, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("production %s: %v\n%s", name, err, raw)
			}
		}
	}
}

func TestLLMIStopsBeforeMutatingForeignStandardNamespace(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		code    int
		message string
	}{
		{"foreign-namespace", 1, "Standard base/namespace is absent or foreign"},
		{"api-failure", 67, "preserved external API error"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			files := llmiRendered(t, dir)
			driver := filepath.Join(dir, "manage.py")
			if err := os.WriteFile(driver, files["kserve/templates/manage.py"], 0o700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			fake := `#!/usr/bin/env python3
import json,os,pathlib,sys
if 'get' not in sys.argv:
 pathlib.Path(os.environ['LLMI_WRITES']).write_text('mutation occurred')
 sys.exit(90)
if os.environ['LLMI_SCENARIO']=='api-failure':
 sys.stderr.write('preserved external API error\n')
 sys.exit(67)
print(json.dumps({'metadata':{'uid':'external-fixture','labels':{'ani.io/managed-by':'foreign'}}}))
`
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("python3", driver, "install")
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ANI_RUN_KUBECONFIG="+filepath.Join(dir, "kubeconfig"), "LLMI_SCENARIO="+scenario.name, "LLMI_WRITES="+filepath.Join(dir, "writes"))
			raw, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != scenario.code || !strings.Contains(string(raw), scenario.message) {
				t.Fatalf("preflight exit=%v, want %d/%s: %s", err, scenario.code, scenario.message, raw)
			}
			if _, err := os.Stat(filepath.Join(dir, "writes")); !os.IsNotExist(err) {
				t.Fatal("failed shared-object preflight mutated Kubernetes")
			}
		})
	}
}

func TestLLMIRecognizesSourceApprovedStandardCAWithoutExcludedIssuer(t *testing.T) {
	dir := t.TempDir()
	files := llmiRendered(t, dir)
	driver, ca := filepath.Join(dir, "manage.py"), filepath.Join(dir, "internal-ca.yaml")
	for path, data := range map[string][]byte{driver: files["kserve/templates/manage.py"], ca: files["cert-manager/templates/internal-ca.yaml"]} {
		if len(data) == 0 {
			t.Fatal("missing production driver/CA recipe")
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Reuse the real fixed Standard overlay and CA recipe. Only the API read
	// boundary supplies object UIDs, mapped image names and Ready observations.
	script := `import copy,json,runpy,sys,yaml
driver=runpy.run_path(sys.argv[1]);g=driver['standard'].__globals__
objects=json.load(open(sys.argv[2]))+list(yaml.safe_load_all(open(sys.argv[3])))
fixtures={}
for value in objects:
 if not value:continue
 o=copy.deepcopy(value);m=o['metadata'];m['uid']='isolated-api-boundary-'+m['name']
 m.setdefault('labels',{})['ani.io/managed-by']=g['OWNER']
 if o['kind']=='Deployment' and m['name']=='kserve-controller-manager':
  for c in o['spec']['template']['spec']['containers']:
   if c['name']=='manager':c['image']=g['STANDARD_IMAGE']
 if o['kind'] in ['ClusterIssuer','Certificate']:o['status']={'conditions':[{'type':'Ready','status':'True'}]}
 fixtures[(o['kind'].lower(),m['name'],m.get('namespace'))]=o
calls=[]
def external_api(kind,name=None,namespace=None):
 calls.append((kind,name,namespace))
 return fixtures.get(({'crd':'customresourcedefinition'}.get(kind,kind),name,namespace))
g['get']=external_api
assert not external_api('issuer','selfsigned-issuer','kserve')
result=driver['standard']()
assert len(result)==64
assert ('clusterissuer','ani-ca',None) in calls
fixtures[('clusterissuer','ani-ca',None)]['status']['conditions'][0]['status']='False'
try:
 driver['standard']()
 raise AssertionError('unready shared CA passed Standard protection')
except ValueError as e:assert 'not ready' in str(e),str(e)
`
	cmd := exec.Command("python3", "-c", script, driver, filepath.Join("..", "..", "ani/kubeflow/stage2/overlay/resources.json"), ca)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual Standard/CA prerequisite: %v\n%s", err, raw)
	}
}

func TestLLMIServiceRequiresActualLeaderUIDAddressAndPort(t *testing.T) {
	dir := t.TempDir()
	driver := filepath.Join(dir, "manage.py")
	if err := os.WriteFile(driver, llmiRendered(t, dir)["kserve/templates/manage.py"], 0o600); err != nil {
		t.Fatal(err)
	}
	script := `import copy,runpy,sys
driver=runpy.run_path(sys.argv[1]);check=driver['leader_endpoints'];g=check.__globals__
leader={'metadata':{'name':'actual-leader','uid':'actual-leader-uid'},'status':{'podIP':'10.16.0.17'}}
source={'metadata':{'name':'actual-leader-slice','uid':'slice-uid','labels':{'kubernetes.io/service-name':'probe-http'}},'ports':[{'port':8000,'protocol':'TCP'}],
 'endpoints':[{'conditions':{'ready':True},'addresses':['10.16.0.17'],'targetRef':{'kind':'Pod','name':'actual-leader','uid':'actual-leader-uid'}}]}
for scenario in ['actual','wrong-pod-uid','wrong-address','wrong-port','no-ready-endpoint']:
 o=copy.deepcopy(source)
 if scenario=='wrong-pod-uid':o['endpoints'][0]['targetRef']['uid']='different-pod-uid'
 if scenario=='wrong-address':o['endpoints'][0]['addresses']=['10.16.0.18']
 if scenario=='wrong-port':o['ports'][0]['port']=8001
 if scenario=='no-ready-endpoint':o['endpoints'][0]['conditions']['ready']=False
 g['listing']=lambda kind,namespace:[o]
 result=check('isolated-api-boundary',leader)
 assert bool(result)==(scenario=='actual'),scenario
`
	cmd := exec.Command("python3", "-c", script, driver)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real leader endpoint identity: %v\n%s", err, raw)
	}
}
