package ani

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestKCNManifestTemplateUsesSiteInputs(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "kcn", "templates", "install.yaml")
	data := map[string]any{
		"ani": map[string]any{
			"images": map[string]string{
				"docker.changqingyun.cn/kubercloud/kc-networking:v0.6.2": "192.0.2.11:5000/kubercloud/kc-networking:v0.6.2",
			},
			"node_addresses": []string{"192.0.2.11", "192.0.2.12", "192.0.2.13"},
			"network": map[string]any{
				"service_cidr": "10.96.0.0/16",
				"kcn": map[string]any{
					"managedDevices":   []string{"ens35"},
					"encapNetworks":    []string{"192.0.2.0/24"},
					"intranetNetworks": []string{"10.96.0.0/16"},
				},
			},
		},
	}

	tmpl, err := template.New("install.yaml").Funcs(template.FuncMap{
		"join": func(sep string, values []string) string { return strings.Join(values, sep) },
	}).ParseFiles(path)
	if err != nil {
		t.Fatalf("parse kcn template: %v", err)
	}
	builder := &strings.Builder{}
	if err := tmpl.Execute(builder, data); err != nil {
		t.Fatalf("execute kcn template: %v", err)
	}
	out := builder.String()

	for _, want := range []string{
		"--service-cluster-ip-range=10.96.0.0/16",
		"value: 192.0.2.11,192.0.2.12,192.0.2.13",
		"managedDevices: ens35",
		"encapNetworks: 192.0.2.0/24",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered manifest missing %q", want)
		}
	}
	for _, stale := range []string{"33.3.1.201", "33.3.1.202", "33.3.1.203", "33.3.64.0/19"} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale site value %q", stale)
		}
	}
	for _, want := range []string{
		"name: ovn-nb",
		"name: ovn-northd",
		"name: ovn-sb",
		"/kc-networking/start-db.sh &",
		"/kc-networking/kc-networking-leader-checker --probeInterval=\"${OVN_LEADER_PROBE_INTERVAL}\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered manifest missing KCN leader fix %q", want)
		}
	}
	for _, stale := range []string{"name: kcn-ovn-nb", "name: kcn-ovn-northd", "name: kcn-ovn-sb"} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale OVN Service name %q", stale)
		}
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "kcn-install.yaml"), []byte(out), 0o600); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
}

func TestANIEnvoyCleansOrphanKCNPodPorts(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "envoy", "templates", "cleanup-orphan-lsp.py")
	tmpl, err := template.New("cleanup-orphan-lsp.py").Funcs(template.FuncMap{
		"join": func(sep string, values []string) string { return strings.Join(values, sep) },
	}).ParseFiles(scriptPath)
	if err != nil {
		t.Fatalf("parse cleanup template: %v", err)
	}
	builder := &strings.Builder{}
	if err := tmpl.Execute(builder, map[string]any{
		"ani": map[string]any{
			"node_addresses": []string{"192.0.2.11", "192.0.2.12", "192.0.2.13"},
		},
	}); err != nil {
		t.Fatalf("execute cleanup template: %v", err)
	}
	cleanup := builder.String()
	for _, want := range []string{
		"NODE_ADDRESSES = '192.0.2.11,192.0.2.12,192.0.2.13'",
		"tcp:[{address}]:6641",
		"vnics.networking.kubercloud.com",
		"Logical_Switch_Port",
		"auto-.*-vnic-",
		"lsp-del",
	} {
		if !strings.Contains(cleanup, want) {
			t.Fatalf("cleanup template missing %q", want)
		}
	}

	tasksPath := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "envoy", "tasks", "main.yaml")
	tasks, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatalf("read Envoy tasks: %v", err)
	}
	got := string(tasks)
	last := -1
	for _, want := range []string{
		"ANI Envoy | Remove orphan LSPs before base install",
		"ANI Envoy | Apply base install manifest",
		"ANI Envoy | Remove orphan LSPs after base install",
		"ANI Envoy | Restart controller",
		"ANI Envoy | Wait for controller",
	} {
		index := strings.Index(got, want)
		if index < 0 {
			t.Fatalf("Envoy tasks missing %q", want)
		}
		if index < last {
			t.Fatalf("Envoy task %q is out of order", want)
		}
		last = index
	}

	buildScript, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build-offline.sh"))
	if err != nil {
		t.Fatalf("read offline build script: %v", err)
	}
	if !strings.Contains(string(buildScript), "builtin/core/roles/ani/envoy/templates/cleanup-orphan-lsp.py") {
		t.Fatal("offline build script does not require the orphan LSP cleanup template")
	}
	if !strings.Contains(string(buildScript), `find "$OUTPUT/manifests/ani" -type d -name __pycache__ -prune -exec rm -rf {} +`) {
		t.Fatal("offline build script does not exclude Python bytecode caches")
	}
}
