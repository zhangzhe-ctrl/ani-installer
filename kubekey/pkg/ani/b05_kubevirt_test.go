package ani

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestB05KubeVirtSelectionAndProductionRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("kubevirt", off) {
		t.Fatal("KubeVirt defaulted on")
	}
	onText := strings.Replace(r06SiteA, "  certManager: {enabled: true}",
		"  certManager: {enabled: true}\n  kubevirt: {enabled: true, vmNode: node2, storageClass: ani-block, scratchStorageClass: ani-block, storageSize: 2Gi}", 1)
	on, err := ParseClusterConfig([]byte(onText))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(on); err != nil {
		t.Fatal(err)
	}
	if !aniRoleEnabled("kubevirt", on) {
		t.Fatal("KubeVirt selected but role disabled")
	}
	table := r08FullTable(t)
	delete(table, "quay.io/kubevirt/virt-launcher:v1.9.0")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "virt-launcher") {
		t.Fatalf("missing dynamic launcher image accepted: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, f := range files {
		if f.Role != "kubevirt" {
			continue
		}
		if strings.Contains(string(f.Rendered), "<no value>") {
			t.Fatalf("unbound B05 template %s", f.Rel)
		}
		seen[f.Rel] = string(f.Rendered)
	}
	for _, rel := range []string{"tasks/main.yaml", "templates/prereq.sh", "templates/verify.sh", "templates/kubevirt-operator.yaml", "templates/cdi-operator.yaml", "templates/kubevirt-cr.yaml", "templates/cdi-cr.yaml", "templates/guest-source.yaml"} {
		if seen["kubevirt/"+rel] == "" {
			t.Errorf("missing rendered %s", rel)
		}
	}
	for rel, wants := range map[string][]string{
		"tasks/main.yaml":                  {"test -c /dev/kvm", "delegate_to: 'node2'"},
		"templates/prereq.sh":              {"Immediate", "sha256sum -c"},
		"templates/verify.sh":              {"kind: DataVolume", "kind: VirtualMachine", "stop vm/ani-b05-guest", "start vm/ani-b05-guest", "ani-b05-marker", "StrictHostKeyChecking=accept-new"},
		"templates/kubevirt-operator.yaml": {"/kubevirt/virt-launcher:v1.9.0", "ani.io/managed-by: 'ani-lab'"},
		"templates/cdi-operator.yaml":      {"/kubevirt/cdi-importer:v1.66.1"},
	} {
		for _, want := range wants {
			if !strings.Contains(seen["kubevirt/"+rel], want) {
				t.Errorf("%s omits %q", rel, want)
			}
		}
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}

func TestB05RejectsUndeclaredVMNode(t *testing.T) {
	bad := strings.Replace(r06SiteA, "  certManager: {enabled: true}",
		"  certManager: {enabled: true}\n  kubevirt: {enabled: true, vmNode: absent}", 1)
	c, err := ParseClusterConfig([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "not a declared node") {
		t.Fatalf("vmNode gate: %v", err)
	}
}
