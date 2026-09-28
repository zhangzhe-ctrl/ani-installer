package ani

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestB07HarborProductionSelectionAndRender(t *testing.T) {
	off, err := ParseClusterConfig([]byte(r06SiteA))
	if err != nil {
		t.Fatal(err)
	}
	if aniRoleEnabled("harbor", off) {
		t.Fatal("Harbor defaulted on")
	}
	if strings.Contains(strings.Join(requiredChartPaths(off), " "), "harbor") {
		t.Fatal("disabled Harbor requires Chart")
	}
	onText := strings.Replace(r06SiteA, "  certManager: {enabled: true}",
		"  certManager: {enabled: true}\n  harbor: {enabled: true, externalAddress: 192.0.2.11, storageClass: ani-block, storageSize: 10Gi}", 1)
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
	if !aniRoleEnabled("harbor", on) {
		t.Fatal("selected Harbor role disabled")
	}
	if !strings.Contains(strings.Join(requiredChartPaths(on), " "), "charts/harbor/1.19.2.tgz") {
		t.Fatal("selected Harbor Chart missing")
	}
	table := r08FullTable(t)
	delete(table, "docker.io/goharbor/trivy-adapter-photon:v2.15.2")
	if _, err := KubeKeyConfig(on, "/opt/ani/packages/kubekey-artifact.tgz", "/opt/ani", table); err == nil || !strings.Contains(err.Error(), "trivy-adapter") {
		t.Fatalf("missing scanner image should stop preflight: %v", err)
	}
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"), on, "/opt/ani", r08FullTable(t))
	if err != nil {
		t.Fatal(err)
	}
	rendered := map[string]string{}
	for _, f := range files {
		if f.Role != "harbor" {
			continue
		}
		if f.Rel != "harbor/tasks/main.yaml" && strings.Contains(string(f.Rendered), "<no value>") {
			t.Fatalf("unbound Harbor template %s: %s", f.Rel, string(f.Rendered))
		}
		rendered[f.Rel] = string(f.Rendered)
	}
	for _, rel := range []string{"tasks/main.yaml", "templates/values.yaml", "templates/prereq.sh", "templates/cert-setup.sh", "templates/seed.sh", "templates/verify.sh", "templates/runtime-pods.yaml", "templates/render-check.sh", "templates/connection.md"} {
		if rendered["harbor/"+rel] == "" {
			t.Errorf("missing rendered %s", rel)
		}
	}
	for rel, wants := range map[string][]string{
		"templates/cert-setup.sh":     {"openssl rand -hex 32", "registry-password"},
		"templates/render-check.sh":   {"Harbor Chart did not render a bcrypt registry htpasswd", "Harbor Jobservice must render Recreate"},
		"templates/values.yaml":       {"https://192.0.2.11:30003", "skipJavaDBUpdate: true", "offlineScan: true", "ani-harbor-trivy-cache"},
		"templates/prereq.sh":         {"sha256sum -c", "pre-existing ani-harbor namespace", "30002|30003"},
		"templates/verify.sh":         {"/api/v2.0", "--hosts-dir", "scan_overview", "report_id", "ani-b07-runtime"},
		"tasks/main.yaml":             {"registry.credentials.password", "ani/harbor", "systemctl restart containerd", "src: runtime-pods.yaml", "dest: /etc/kubernetes/ani/harbor/runtime-pods.yaml"},
		"templates/runtime-pods.yaml": {"ani-b07-runtime", "imagePullPolicy: Always", "ani-harbor-pull"},
	} {
		for _, want := range wants {
			if !strings.Contains(rendered["harbor/"+rel], want) {
				t.Errorf("%s omits %q", rel, want)
			}
		}
	}
	tasks := rendered["harbor/tasks/main.yaml"]
	if render, verifyTask := strings.Index(tasks, "src: runtime-pods.yaml"), strings.Index(tasks, "command: bash /etc/kubernetes/ani/harbor/verify.sh"); render < 0 || verifyTask < 0 || render > verifyTask {
		t.Error("Harbor runtime pull manifest must be rendered before functional verification")
	}
	verify := rendered["harbor/templates/verify.sh"]
	if scan, pull := strings.Index(verify, "$API/projects/$PROJECT/repositories/busybox/artifacts/1.37.0/scan"), strings.Index(verify, `--user "$PULL_USER:$PULL_SECRET" "$TARGET"`); scan < 0 || pull < 0 || scan > pull {
		t.Error("strict project policy requires real scan completion before authenticated Harbor pull")
	}
	if err := ValidateRenderedArtifacts(files); err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join("..", "..", "ani", "charts", "harbor", "harbor-1.19.2.tgz")
	if data, err := os.ReadFile(chart); err != nil || len(data) == 0 {
		t.Fatalf("locked Harbor Chart unavailable: %v", err)
	}
	if helm := os.Getenv("ANI_B07_HELM_BIN"); helm != "" {
		dir := t.TempDir()
		values := filepath.Join(dir, "values.yaml")
		db := filepath.Join(dir, "db-password")
		registry := filepath.Join(dir, "registry-password")
		for path, content := range map[string]string{values: rendered["harbor/templates/values.yaml"], db: "test-db-secret", registry: strings.Repeat("r", 64)} {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(helm, "template", "ani-harbor", chart, "--namespace", "ani-harbor", "--kube-version", "1.35.8", "--values", values,
			"--set-file", "database.internal.password="+db, "--set-file", "registry.credentials.password="+registry, "--include-crds")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("official Harbor Chart render: %v\n%s", err, output)
		}
		manifest := string(output)
		match := regexp.MustCompile(`(?m)^\s+REGISTRY_HTPASSWD:\s*"([A-Za-z0-9+/=]+)"\s*$`).FindStringSubmatch(manifest)
		if len(match) != 2 {
			t.Fatal("official Harbor Chart omitted registry htpasswd Secret")
		}
		htpasswd, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(htpasswd), ":", 2)
		if len(parts) != 2 || parts[0] != "harbor_registry_user" || bcrypt.CompareHashAndPassword([]byte(parts[1]), []byte(strings.Repeat("r", 64))) != nil {
			t.Fatal("official Harbor Chart rendered an unusable internal registry htpasswd")
		}
		for _, want := range []string{
			"name: ani-harbor-trivy", "name: \"ani-harbor-database\"", "name: ani-harbor-redis",
			"claimName: ani-harbor-trivy-cache", "SCANNER_TRIVY_SKIP_UPDATE", "SCANNER_TRIVY_SKIP_JAVA_DB_UPDATE",
			"SCANNER_TRIVY_OFFLINE_SCAN", "verify-offline-db", "goharbor/trivy-adapter-photon:v2.15.2",
			"library/busybox:1.37.0",
		} {
			if !strings.Contains(manifest, want) {
				t.Errorf("Chart render omits %q", want)
			}
		}
		if !strings.Contains(manifest, "strategy:\n    type: Recreate") {
			t.Error("Harbor chart did not render Recreate for the singleton RWO workload")
		}
		if strings.Contains(manifest, "harbor_registry_password") || strings.Contains(manifest, "Harbor12345") {
			t.Error("Chart defaults leaked into rendered manifest")
		}
	}
}

func TestB07HarborRejectsUnownedAddress(t *testing.T) {
	text := strings.Replace(r06SiteA, "  certManager: {enabled: true}",
		"  certManager: {enabled: true}\n  harbor: {enabled: true, externalAddress: 198.51.100.1}", 1)
	c, err := ParseClusterConfig([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "declared node") {
		t.Fatalf("address gate: %v", err)
	}
}
