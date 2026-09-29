package ani

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestB03MetricsServerServingTLSIsSelectedAndRendered(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("metrics-server", off) {
		t.Fatal("B03 role enabled when switch is off")
	}
	offConfig, err := KubeKeyConfig(off, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	offKubernetes := offConfig["kubernetes"].(map[string]any)
	if _, ok := offKubernetes["kubelet"]; ok {
		t.Fatal("disabled B03 changed kubelet configuration")
	}

	onText := strings.Replace(r06SiteA, "  certManager: {enabled: true}", "  certManager: {enabled: true}\n  metricsServer: {enabled: true}", 1)
	on, err := ParseClusterConfig([]byte(onText))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(on); err != nil {
		t.Fatal(err)
	}
	if !aniRoleEnabled("metrics-server", on) {
		t.Fatal("selected B03 role absent")
	}
	config, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	kubernetes := config["kubernetes"].(map[string]any)
	if _, ok := kubernetes["kubelet"]; ok {
		t.Fatal("selected B03 changed initial kubelet configuration before B01/B02 checks")
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"metrics-server/tasks/main.yaml":                {"Refuse a foreign metrics APIService before certificate changes", "Enable kubelet serving TLS bootstrap in kubeadm configuration", "Enable serving CSR flow on every declared node", "Approve only selected node serving CSRs", "Reject untrusted kubelet certificates"},
		"metrics-server/templates/serving-configmap.sh": {"serverTLSBootstrap: true", "resourceVersion", "kubeadm-kubelet-config-before.yaml"},
		"metrics-server/templates/serving-node.sh":      {"serverTLSBootstrap: true", "kubelet-restart-required", "node-kubelet-config-before.yaml"},
		"metrics-server/templates/serving-approve.sh":   {"kubernetes.io/kubelet-serving", "system:nodes", "Subject Alternative Name", "certificate', 'approve"},
		"metrics-server/templates/prereq.sh":            {"-CAfile", "-verify_ip", "-verify_return_error"},
		"metrics-server/templates/values.yaml":          {"insecureSkipTLSVerify: false", "--kubelet-certificate-authority="},
	}
	for _, file := range files {
		expected, ok := want[file.Rel]
		if !ok {
			continue
		}
		for _, fragment := range expected {
			if !strings.Contains(string(file.Rendered), fragment) {
				t.Errorf("%s missing %q", file.Rel, fragment)
			}
		}
		delete(want, file.Rel)
	}
	if len(want) != 0 {
		t.Fatalf("selected B03 templates absent: %v", want)
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}
