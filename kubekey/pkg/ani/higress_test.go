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

func TestHigressSmokeWritesEvidenceInAFreshPrivateDirectory(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := r13Prepare(t)
	c := kubeflowTestConfig()
	c.Kubeflow = nil
	c.Components.CertManager.Enabled = true
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
	files, err := RenderSite("../../builtin/core/roles/ani", c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	scripts := filepath.Join(base, "scripts", "higress")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Rel == "higress/templates/verify.sh" {
			if err := os.WriteFile(filepath.Join(scripts, "verify.sh"), file.Rendered, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The checker process is the external boundary here. Its reporter writes
	// a real file, like the production CLI, and fails when its parent is absent.
	// Gateway/auth/plugin behavior is covered separately by the real live probe.
	reporter := "#!" + python + `
import json,pathlib,sys
assert sys.argv[1:4]==['/etc/kubernetes/ani/higress/manage.py','check','--output-dir']
out=pathlib.Path(sys.argv[4])
assert out.stat().st_mode & 0o777 == 0o700, 'evidence directory must be private'
(out/'higress-behavior.json').write_text(json.dumps({'status':'pass'}))
`
	if err := os.WriteFile(filepath.Join(base, "bin", "python3"), []byte(reporter), 0o700); err != nil {
		t.Fatal(err)
	}
	run, state := r13RunRecord(t, base, true, PhaseSucceeded, "higress")
	out := filepath.Join(base, "fresh", "verification")
	if err := RunVerify(context.Background(), VerifyInput{RunFile: run, StateFile: state,
		Level: VerifyLevelSmoke, ScriptDir: filepath.Dir(scripts), Output: out,
		Kubeconfig: filepath.Join(base, "kubeconfig")}, io.Discard); err != nil {
		t.Fatalf("formal Higress smoke must allow its checker to write fresh private evidence: %v", err)
	}
	evidence := filepath.Join(out, "smoke-ani-ani-lab-20260924-130000-higress", "higress-behavior.json")
	if _, err := os.Stat(evidence); err != nil {
		t.Fatal("Higress checker evidence was not persisted", err)
	}
}

func TestHigressGatewayClassMatchesOfficialControllerReconciliation(t *testing.T) {
	c := kubeflowTestConfig()
	c.Kubeflow = nil
	c.Components.CertManager.Enabled = true
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "manage.py")
	for _, file := range files {
		if file.Rel == "higress/templates/manage.py" {
			if err := os.WriteFile(driver, file.Rendered, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// This is the actual spec reconciled by the fixed 2.2.4 controller, not
	// a relaxed subset comparison. Its classInfo supplies the description.
	program := `import importlib.util,sys
s=importlib.util.spec_from_file_location('higress',sys.argv[1])
m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
actual={'controllerName':'higress.io/gateway-controller','description':'The default Higress GatewayClass'}
expected=m.gateway_objects()[0]['spec']
assert expected==actual, ('official reconciliation differs',expected,actual)
m.GATEWAY_CLASS='site-edge'
actual['controllerName']='higress.io/gateway-controller-site-edge'
assert m.gateway_objects()[0]['spec']==actual, 'configured class must retain official classInfo description'
`
	cmd := exec.Command("python3", "-c", program, driver)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("official reconciled GatewayClass must match the declared identity: %v\n%s", err, raw)
	}
}

func TestHigressOCIPluginChecksumMatchesOfficialLoader(t *testing.T) {
	c := kubeflowTestConfig()
	c.Kubeflow = nil
	c.Components.CertManager.Enabled = true
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "manage.py")
	for _, file := range files {
		if file.Rel == "higress/templates/manage.py" {
			if err := os.WriteFile(driver, file.Rendered, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The pinned upstream OCI loader checks the manifest digest before it
	// fetches the binary. The loaded file must independently match Wasm bytes.
	program := `import importlib.util,json,sys
s=importlib.util.spec_from_file_location('higress',sys.argv[1])
m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
plugins=json.load(open(sys.argv[2]))['plugins']
for p,o in zip(plugins,m.plugin_defaults(plugins)):
 assert p['manifest']['digest']!=p['wasmDigest'], 'fixture must distinguish OCI identity from module bytes'
 assert o['spec']['sha256']==p['manifest']['digest'].split(':')[1], ('official OCI loader rejects checksum',p['name'],o['spec']['sha256'])
 assert o['spec']['url'].endswith('@'+p['manifest']['digest'])
m.gateway_pods=lambda:[{'metadata':{'name':'gateway-a'}},{'metadata':{'name':'gateway-b'}}]
m.config_dump=lambda name:json.dumps({'modules':[{'filename':'/var/lib/wasm/'+p['manifest']['digest'].split(':')[1]+'.wasm'} for p in plugins]})
content={p['manifest']['digest'].split(':')[1]:p['wasmDigest'].split(':')[1] for p in plugins}
m.kube=lambda *args:content[args[-1].split('/')[-1].removesuffix('.wasm')]+'  '+args[-1]
loaded=m.loaded_modules(plugins)
assert loaded and all(len(v)==4 for v in loaded.values()), 'content-pinned modules must load on both replicas'
m.kube=lambda *args:args[-1].split('/')[-1].removesuffix('.wasm')+'  '+args[-1]
assert m.loaded_modules(plugins) is False, 'manifest checksum cannot replace actual module byte verification'
`
	cmd := exec.Command("python3", "-c", program, driver, filepath.Join("..", "..", "ani", "higress", "wasm.lock.json"))
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("official OCI and loaded module identity: %v\n%s", err, raw)
	}
}

func TestHigressOfficialChartKeepsOnlySelectedGatewayAndPinnedImages(t *testing.T) {
	helm := os.Getenv("ANI_LLMI_HELM_BIN")
	if helm == "" {
		t.Skip("requires the packaged Helm and fixed official Higress chart")
	}
	c := kubeflowTestConfig()
	c.Kubeflow = nil
	c.Components.CertManager.Enabled = true
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "harbor"}
	c.Components.Harbor = HarborComponent{Enabled: true, ExternalAddress: c.Nodes[0].Address, StorageClass: "ani-block", StorageSize: "10Gi"}
	c.Components = c.EffectiveComponents()
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, "/offline", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range files {
		if strings.HasPrefix(file.Rel, "higress/") && strings.HasSuffix(file.Rel, ".py") {
			compile := exec.Command("python3", "-c", "import sys; compile(sys.stdin.read(), sys.argv[1], 'exec')", file.Rel)
			compile.Stdin = bytes.NewReader(file.Rendered)
			if raw, err := compile.CombinedOutput(); err != nil {
				t.Fatalf("production Higress driver: %v\n%s", err, raw)
			}
		}
		if file.Rel == "higress/templates/values.yaml" || file.Rel == "higress/templates/filter.py" {
			if err := os.WriteFile(filepath.Join(dir, filepath.Base(file.Rel)), file.Rendered, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "filter.py")); err != nil {
		t.Fatal("independent Higress render omitted its bounded official chart filter", err)
	}
	chart := filepath.Join("..", "..", "ani", "charts", "higress", "higress-2.2.4.tgz")
	cmd := exec.Command(helm, "template", "ani-higress", chart, "--namespace", "higress-system", "--values", filepath.Join(dir, "values.yaml"), "--post-renderer", filepath.Join(dir, "filter.py"))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("official Higress filter: %v\n%s", err, raw)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	deployments, images, caVolumes := 0, map[string]bool{}, 0
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
		kind := obj["kind"].(string)
		metadata := obj["metadata"].(map[string]interface{})
		name := metadata["name"].(string)
		if strings.Contains(name, "console") || strings.Contains(name, "redis") || strings.Contains(name, "plugin-server") || kind == "CustomResourceDefinition" || kind == "IngressClass" || kind == "EnvoyFilter" {
			t.Fatalf("unselected or shared object in independent gateway render: %s/%s", kind, name)
		}
		if kind != "Deployment" {
			continue
		}
		deployments++
		spec := obj["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		for _, value := range spec["containers"].([]interface{}) {
			container := value.(map[string]interface{})
			images[container["image"].(string)] = true
			if container["name"] == "higress-gateway" {
				for _, e := range container["env"].([]interface{}) {
					env := e.(map[string]interface{})
					if env["name"] == "SSL_CERT_FILE" && env["value"] == "/etc/ani-wasm-trust/ca.crt" {
						caVolumes++
					}
					if env["name"] == "WASM_INSECURE_REGISTRIES" {
						t.Fatal("CA-backed gateway introduced insecure registry trust")
					}
				}
			}
		}
	}
	if deployments != 2 || len(images) != 3 || caVolumes != 1 {
		t.Fatalf("gateway boundary: deployments=%d images=%v CA=%d", deployments, images, caVolumes)
	}
	for _, digest := range []string{"ab9012ff6ff08135d44696cf8b44cb9b7070d22067ec7d0d9be2a5410ab2e885", "47f3897770e6ee65d27826233efac970b9b5cce53f11d178d062129d3efbb921", "679841c12ba34efa15f47402156195c2abbd4a58327a28c2367ea4a219ff6b2a"} {
		found := false
		for image := range images {
			found = found || strings.HasSuffix(image, "@sha256:"+digest)
		}
		if !found {
			t.Fatal("missing source-approved target-platform image", digest)
		}
	}
}

func TestHigressTypedIndependentSelectionAndPortConflicts(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		kubeflow    bool
		http, https int
		want        string
	}{
		{"independent", false, 30445, 30446, ""},
		{"coexists-with-kubeflow", true, 30445, 30446, ""},
		{"http-conflicts-with-kubeflow", true, 30443, 30446, "conflicts with Kubeflow"},
		{"reserved-https-conflicts-with-kubeflow", true, 30445, 30444, "conflicts with Kubeflow"},
		{"listeners-reuse-nodeport", false, 30445, 30445, "distinct NodePorts"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c := kubeflowTestConfig()
			c.Components.CertManager.Enabled = true
			if !scenario.kubeflow {
				c.Kubeflow = nil
			}
			raw, err := yaml.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var site map[string]interface{}
			if err := yaml.Unmarshal(raw, &site); err != nil {
				t.Fatal(err)
			}
			site["components"].(map[string]interface{})["higress"] = map[string]interface{}{
				"enabled": true, "httpPort": scenario.http, "httpsPort": scenario.https,
				"gatewayClass": "higress", "gatewayName": "ani-higress",
				"allowedNamespaces": []string{"ani-system"}, "wasmRegistry": "material",
			}
			raw, err = yaml.Marshal(site)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "site.yaml")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = RunValidate(context.Background(), ValidateInput{ConfigFile: path}, &out)
			if scenario.want != "" {
				if err == nil || !strings.Contains(err.Error(), scenario.want) {
					t.Fatalf("want %q, got %v", scenario.want, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("independent official Higress selection must validate: %v", err)
			}
			if !strings.Contains(out.String(), "higress") {
				t.Fatalf("resolved selection omits Higress: %s", out.String())
			}
			if !scenario.kubeflow && (strings.Contains(out.String(), "lws,") || strings.Contains(out.String(), "kserve,") || strings.Contains(out.String(), "kubeflow\n")) {
				t.Fatalf("Higress derived the Kubeflow combination: %s", out.String())
			}
		})
	}
}

func TestHigressRefusesForeignOrIncompatibleSharedScopeBeforeWriting(t *testing.T) {
	upstream := os.Getenv("ANI_HIGRESS_WASM_SOURCE_ROOT")
	if upstream == "" {
		t.Skip("requires fixed official OCI Wasm sources")
	}
	root := t.TempDir()
	sources := []string{}
	for _, name := range []string{"model-router", "key-auth", "key-rate-limit", "ai-statistics"} {
		sources = append(sources, filepath.Join(upstream, "higress-oci-"+name))
	}
	if err := RunMaterialsPlaceWasm(MaterialsWasmInput{LockPath: filepath.Join("..", "..", "ani", "higress", "wasm.lock.json"), SourceStores: sources, ArtifactRoot: root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	chart, err := os.ReadFile(filepath.Join("..", "..", "ani", "charts", "higress", "higress-2.2.4.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "charts/higress"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "charts/higress/2.2.4.tgz"), chart, 0o600); err != nil {
		t.Fatal(err)
	}
	c := kubeflowTestConfig()
	c.Kubeflow = nil
	c.Components.CertManager.Enabled = true
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, root, r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(root, "manage.py")
	for _, file := range files {
		if file.Rel == "higress/templates/manage.py" {
			if err := os.WriteFile(driver, file.Rendered, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, scenario := range []struct{ name, want string }{{"foreign-namespace", "foreign Higress namespace"}, {"incompatible-shared-api", "shared Gateway API is not compatible"}} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := `#!/usr/bin/env python3
import json,os,pathlib,sys
a=sys.argv[1:]
if a[0]=='--kubeconfig':a=a[2:]
with open(os.environ['HIGRESS_CALLS'],'a') as f:f.write(json.dumps(a)+'\n')
if a[0]!='get':
 pathlib.Path(os.environ['HIGRESS_WRITES']).write_text('write')
 sys.exit(90)
if os.environ['HIGRESS_CASE']=='incompatible-shared-api' and a[1:3]==['crd','gatewayclasses.gateway.networking.k8s.io']:
 print(json.dumps({'metadata':{'uid':'foreign-shared-api'},'spec':{'versions':[{'name':'v1beta1','served':True}]},'status':{'conditions':[{'type':'Established','status':'True'}]}}))
elif os.environ['HIGRESS_CASE']=='foreign-namespace' and a[1:3]==['namespace','higress-system']:
 print(json.dumps({'metadata':{'uid':'foreign-gateway-ns','labels':{'ani.io/managed-by':'another-owner'}}}))
elif a[1] in ['deployments','secrets']:print(json.dumps({'items':[]}))
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("python3", driver, "install")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "ANI_RUN_KUBECONFIG="+filepath.Join(dir, "kubeconfig"), "HIGRESS_CASE="+scenario.name, "HIGRESS_CALLS="+filepath.Join(dir, "calls.jsonl"), "HIGRESS_WRITES="+filepath.Join(dir, "writes"))
			raw, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(raw), scenario.want) {
				t.Fatalf("expected protected-scope refusal %q, got %v\n%s", scenario.want, err, raw)
			}
			if _, err := os.Stat(filepath.Join(dir, "writes")); !os.IsNotExist(err) {
				t.Fatal("shared-scope rejection performed an API mutation")
			}
		})
	}
}

func TestHigressFirstInstallRejectsMissingOCIClosureAtMaterialPreflight(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join("..", "..", "ani", "components.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config/components.lock.yaml"), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	c := validConfig()
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
	report, err := RunPreflight(PreflightInput{ArtifactRoot: root, Cluster: &c, RunID: "missing-higress-wasm", ReportBaseDir: t.TempDir()})
	if err == nil || report.ChangesStarted || report.FailedCheck != "Higress OCI Wasm source binding" {
		t.Fatalf("missing Wasm must block before base import or API writes, report=%+v err=%v", report, err)
	}
}

func TestHigressHTTPKeepsProtectedHeadersWithinDeclaredEndpoint(t *testing.T) {
	c := kubeflowTestConfig()
	c.Kubeflow = nil
	c.Components.CertManager.Enabled = true
	c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
	dir := t.TempDir()
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), c, dir, r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "manage.py")
	for _, file := range files {
		if file.Rel == "higress/templates/manage.py" {
			if err := os.WriteFile(driver, file.Rendered, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Real local HTTP endpoints stand in only for network boundaries. The
	// shipped driver's client decides whether credentials follow a redirect
	// or leave the site through an ambient proxy.
	probe := `import http.server,os,runpy,sys,threading,urllib.error,urllib.request
client=runpy.run_path(sys.argv[1])['http_open']
hits={'destination':0,'proxy':0}
class Destination(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  hits['destination']+=1
  self.send_response(200);self.end_headers();self.wfile.write(b'wrong-authority')
 def log_message(self,*a):pass
class Proxy(Destination):
 def do_GET(self):
  hits['proxy']+=1
  self.send_response(200);self.end_headers();self.wfile.write(b'ambient-proxy')
class Origin(http.server.BaseHTTPRequestHandler):
 def do_POST(self):
  self.send_response(302);self.send_header('Location',destination+'/foreign');self.end_headers()
 def do_GET(self):
  self.send_response(200);self.end_headers();self.wfile.write(b'declared-boundary')
 def log_message(self,*a):pass
servers=[]
def serve(handler):
 s=http.server.ThreadingHTTPServer(('127.0.0.1',0),handler);servers.append(s)
 threading.Thread(target=s.serve_forever,daemon=True).start()
 return 'http://127.0.0.1:'+str(s.server_port)
destination=serve(Destination);origin=serve(Origin);proxy=serve(Proxy)
try:
 for name in ['HTTP_PROXY','http_proxy','HTTPS_PROXY','https_proxy','ALL_PROXY','all_proxy']:os.environ.pop(name,None)
 req=urllib.request.Request(origin+'/redirect',data=b'{}',headers={'x-api-key':'isolated-test-credential'})
 try:
  with client(req,timeout=5):pass
  raise AssertionError('driver followed a redirect carrying a protected header')
 except urllib.error.HTTPError as e:
  assert e.code==302,e.code
 assert hits['destination']==0,hits
 os.environ['http_proxy']=proxy;os.environ['HTTP_PROXY']=proxy
 os.environ['no_proxy']='';os.environ['NO_PROXY']=''
 with client(urllib.request.Request(origin+'/direct',headers={'x-api-key':'isolated-test-credential'}),timeout=5) as r:
  assert r.read()==b'declared-boundary'
 assert hits['proxy']==0,hits
finally:
 for s in servers:s.shutdown();s.server_close()
`
	cmd := exec.Command("python3", "-c", probe, driver)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("protected HTTP authority: %v\n%s", err, raw)
	}
}

func TestHigressFirstInstallRejectsUnapprovedDriverBeforeChanges(t *testing.T) {
	upstream := os.Getenv("ANI_HIGRESS_WASM_SOURCE_ROOT")
	if upstream == "" {
		t.Skip("requires fixed official OCI Wasm sources")
	}
	for _, scenario := range []struct{ name, want string }{
		{"approved-assets", "required artifact files"},
		{"missing-approval", "Higress asset source binding"},
		{"modified-driver", "Higress asset source binding"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			sources := []string{}
			for _, name := range []string{"model-router", "key-auth", "key-rate-limit", "ai-statistics"} {
				sources = append(sources, filepath.Join(upstream, "higress-oci-"+name))
			}
			if err := RunMaterialsPlaceWasm(MaterialsWasmInput{LockPath: filepath.Join("..", "..", "ani", "higress", "wasm.lock.json"), SourceStores: sources, ArtifactRoot: root}, io.Discard); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{"config", "manifests/higress/2.2.4"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			copyFile := func(source, target string) {
				raw, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, target), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			copyFile(filepath.Join("..", "..", "ani", "components.lock.yaml"), "config/components.lock.yaml")
			for _, name := range []string{"connection.md", "filter.py", "gateway-api.yaml", "manage.py", "values.yaml", "verify.sh"} {
				copyFile(filepath.Join("..", "..", "builtin/core/roles/ani/higress/templates", name), "manifests/higress/2.2.4/"+name)
			}
			copyFile(filepath.Join("..", "..", "builtin/core/roles/ani/higress/tasks/main.yaml"), "manifests/higress/2.2.4/tasks.yaml")
			if scenario.name != "missing-approval" {
				copyFile("higress-assets.lock.json", "manifests/higress/2.2.4/assets.lock.json")
			}
			if scenario.name == "modified-driver" {
				if err := os.WriteFile(filepath.Join(root, "manifests/higress/2.2.4/manage.py"), []byte("print('unapproved replacement')\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c := validConfig()
			c.Components.Higress = HigressComponent{Enabled: true, WasmRegistry: "material"}
			report, err := RunPreflight(PreflightInput{ArtifactRoot: root, Cluster: &c, RunID: "higress-assets-" + scenario.name, ReportBaseDir: t.TempDir()})
			if err == nil || report.ChangesStarted || report.FailedCheck != scenario.want {
				t.Fatalf("source-bound preflight: report=%+v err=%v", report, err)
			}
		})
	}
}
