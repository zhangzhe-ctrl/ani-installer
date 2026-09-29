package ani

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		"tasks/main.yaml":                  {"test -c /dev/kvm", "delegate_to: 'node2'", "wait_for_resource crd/datavolumes.cdi.kubevirt.io", "wait_for_resource deployment/cdi-deployment", "CDI did not create $resource within 180s"},
		"templates/prereq.sh":              {"Immediate", "sha256sum -c"},
		"templates/verify.sh":              {"kind: DataVolume", "kind: VirtualMachine", "stop ani-b05-guest", "start ani-b05-guest", "wait_vmi_ready", "VMI did not appear after start", "#!/bin/sh", "authorized_keys", "ani-b05-marker", "StrictHostKeyChecking=accept-new"},
		"templates/kubevirt-operator.yaml": {"/kubevirt/virt-launcher:v1.9.0", "ani.io/managed-by: 'ani-lab'"},
		"templates/cdi-operator.yaml":      {"/kubevirt/cdi-importer:v1.66.1"},
	} {
		for _, want := range wants {
			if !strings.Contains(seen["kubevirt/"+rel], want) {
				t.Errorf("%s omits %q", rel, want)
			}
		}
	}
	// Kube-OVN treats a KubeVirt launcher's VM identity as its network
	// identity. A guest image source Pod with that same name can take the
	// launcher's logical port and IP during a clean install.
	source := yaml.NewDecoder(strings.NewReader(seen["kubevirt/templates/guest-source.yaml"]))
	names := map[string]string{}
	for {
		var resource struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		err := source.Decode(&resource)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode B05 guest source: %v", err)
		}
		names[resource.Kind] = resource.Metadata.Name
	}
	if names["Pod"] == "" || names["Pod"] == "ani-b05-guest" || names["Service"] != names["Pod"] {
		t.Fatalf("B05 source must have its own matching Pod/Service identity, distinct from the VM: %+v", names)
	}
	if !strings.Contains(seen["kubevirt/templates/verify.sh"], "pod/"+names["Pod"]) ||
		!strings.Contains(seen["kubevirt/templates/verify.sh"], "http://"+names["Service"]+".ani-platform.svc.cluster.local") ||
		!strings.Contains(seen["kubevirt/templates/prereq.sh"], "pod/"+names["Pod"]) {
		t.Fatal("B05 importer does not use or protect the distinct source Pod/Service")
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
