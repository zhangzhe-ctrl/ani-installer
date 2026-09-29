package ani

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestB01MultusProductionSelectionAndRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("multus", off) {
		t.Fatal("Multus defaulted on")
	}
	onText := strings.Replace(r06SiteA, "  stack: kcn", "  stack: kcn\n  multus: {enabled: true, testCIDR: 10.250.0.0/24}", 1)
	on, err := ParseClusterConfig([]byte(onText))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(on); err != nil {
		t.Fatal(err)
	}
	if !aniRoleEnabled("multus", on) {
		t.Fatal("selected Multus role disabled")
	}
	table := r08FullTable(t)
	delete(table, "ghcr.io/k8snetworkplumbingwg/multus-cni:v4.3.1-thick")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "multus-cni:v4.3.1-thick") {
		t.Fatalf("missing Multus image accepted: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f.Role != "multus" {
			continue
		}
		seen[f.Rel] = true
		if f.Rel != "multus/tasks/main.yaml" && strings.Contains(string(f.Rendered), "<no value>") {
			t.Errorf("unbound %s", f.Rel)
		}
		if f.Rel == "multus/templates/resources.yaml" {
			for _, want := range []string{"ani-multus", "v4.3.1-thick", "/hostroot", "/host/run/multus/"} {
				if !strings.Contains(string(f.Rendered), want) {
					t.Errorf("missing %s", want)
				}
			}
			if strings.Contains(string(f.Rendered), "snapshot-thick") {
				t.Error("quickstart image leaked")
			}
		}
	}
	for _, rel := range []string{"tasks/main.yaml", "templates/preflight-node.sh", "templates/preflight-global.sh", "templates/nad-crd.yaml", "templates/resources.yaml", "templates/test-nad.yaml", "templates/verify.sh"} {
		if !seen["multus/"+rel] {
			t.Errorf("missing %s", rel)
		}
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}

func TestB01MultusRejectsMissingOrConflictingTestNetwork(t *testing.T) {
	for _, cidr := range []string{"", "10.16.0.0/24", "10.96.0.0/16", "192.0.2.0/24", "10.250.0.0/31", "bad"} {
		site := strings.Replace(r06SiteA, "  stack: kcn", "  stack: kcn\n  multus: {enabled: true, testCIDR: '"+cidr+"'}", 1)
		c, err := ParseClusterConfig([]byte(site))
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(c); err == nil {
			t.Errorf("accepted testCIDR=%q", cidr)
		}
	}
}

func TestB01MultusCannotBeSilentlyAddedByComponentsRun(t *testing.T) {
	base, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	changedText := strings.Replace(r06SiteA, "  stack: kcn", "  stack: kcn\n  multus: {enabled: true, testCIDR: 10.250.0.0/24}", 1)
	changed, err := ParseClusterConfig([]byte(changedText))
	if err != nil {
		t.Fatal(err)
	}
	before, err := BuildRunManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	after, err := BuildRunManifest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compareBaseInvariants(before, after); err == nil || !strings.Contains(err.Error(), "networkMultus") {
		t.Fatalf("network CNI wrapper drift accepted: %v", err)
	}
}
