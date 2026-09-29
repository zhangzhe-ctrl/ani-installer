package ani

import (
	"path/filepath"
	"strings"
	"testing"
)

// This exercises the selected production role through the same site renderer
// and image table that the packaged install uses, including the upstream CRDs.
func TestB04SnapshotProductionSelectionAndRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("snapshot-controller", off) {
		t.Fatal("snapshot controller defaulted on")
	}
	onText := strings.Replace(r06SiteA, "  certManager: {enabled: true}", "  certManager: {enabled: true}\n  snapshotController: {enabled: true}", 1)
	if onText == r06SiteA {
		t.Fatal("site fixture insertion failed")
	}
	on, err := ParseClusterConfig([]byte(onText))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(on); err != nil {
		t.Fatal(err)
	}
	if !aniRoleEnabled("snapshot-controller", on) {
		t.Fatal("selected snapshot role disabled")
	}
	table := r08FullTable(t)
	delete(table, "registry.k8s.io/sig-storage/snapshot-controller:v8.5.0")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "snapshot-controller:v8.5.0") {
		t.Fatalf("missing controller image did not stop preflight: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		if file.Role != "snapshot-controller" {
			continue
		}
		seen[file.Rel] = true
		if strings.Contains(string(file.Rendered), "<no value>") {
			t.Fatalf("unbound snapshot template %s", file.Rel)
		}
		switch file.Rel {
		case "snapshot-controller/templates/controller.yaml":
			text := string(file.Rendered)
			for _, want := range []string{"replicas: 2", "ani-snapshot-controller", "/sig-storage/snapshot-controller:v8.5.0"} {
				if !strings.Contains(text, want) {
					t.Errorf("controller omitted %q", want)
				}
			}
			if strings.Contains(text, "groupsnapshot.storage.k8s.io") {
				t.Error("unselected group snapshot RBAC rendered")
			}
		case "snapshot-controller/templates/classes.yaml":
			text := string(file.Rendered)
			for _, want := range []string{"ani-rbd-retain", "ani-cephfs-retain", "deletionPolicy: Retain", "rook-ceph.rbd.csi.ceph.com", "rook-ceph.cephfs.csi.ceph.com"} {
				if !strings.Contains(text, want) {
					t.Errorf("classes omitted %q", want)
				}
			}
		}
	}
	for _, rel := range []string{"tasks/main.yaml", "templates/prereq.sh", "templates/controller.yaml", "templates/classes.yaml", "templates/crd-volumesnapshotclasses.yaml", "templates/crd-volumesnapshotcontents.yaml", "templates/crd-volumesnapshots.yaml", "templates/verify.sh"} {
		if !seen["snapshot-controller/"+rel] {
			t.Errorf("missing rendered %s", rel)
		}
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}
