package ani

import (
	"path/filepath"
	"strings"
	"testing"
)

// B06 is selected by the production site, image table, playbook and role.
func TestB06VolcanoProductionSelectionAndRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("volcano", off) {
		t.Fatal("Volcano defaulted on")
	}
	if strings.Contains(strings.Join(requiredChartPaths(off), " "), "volcano") {
		t.Fatal("disabled Volcano requires chart")
	}
	onText := strings.Replace(r06SiteA, "  certManager: {enabled: true}", "  certManager: {enabled: true}\n  volcano: {enabled: true}", 1)
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
	if !aniRoleEnabled("volcano", on) {
		t.Fatal("selected Volcano role disabled")
	}
	if !strings.Contains(strings.Join(requiredChartPaths(on), " "), "charts/volcano/1.15.2.tgz") {
		t.Fatal("selected Volcano chart missing")
	}
	table := r08FullTable(t)
	delete(table, "docker.io/volcanosh/vc-scheduler:v1.15.2")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "vc-scheduler:v1.15.2") {
		t.Fatalf("missing scheduler image should stop preflight: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f.Role != "volcano" {
			continue
		}
		seen[f.Rel] = true
		if strings.Contains(string(f.Rendered), "<no value>") {
			t.Fatalf("unbound Volcano template %s", f.Rel)
		}
		if f.Rel == "volcano/templates/verify.sh" {
			for _, want := range []string{"minMember: 2", "schedulerName: volcano", "B06_CPU_DONE_A", "B06_CPU_DONE_B", "b06-default-scheduler-after.json"} {
				if !strings.Contains(string(f.Rendered), want) {
					t.Errorf("functional checker omitted %q", want)
				}
			}
		}
	}
	for _, rel := range []string{"tasks/main.yaml", "templates/values.yaml", "templates/prereq.sh", "templates/verify.sh"} {
		if !seen["volcano/"+rel] {
			t.Errorf("missing rendered %s", rel)
		}
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
}
