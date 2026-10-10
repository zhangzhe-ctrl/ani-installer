package ani

import (
	"context"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A component addition must bind both the original approval and the exact new
// approval. The original package is read only; changing the new lock after the
// plan must reject even a no-op before the child can run.
func TestComponentsPlanBindsAdditiveMaterials(t *testing.T) {
	previousRuntime := runtimeBaseDir
	runtimeBaseDir = t.TempDir()
	t.Cleanup(func() { runtimeBaseDir = previousRuntime })
	input, out := r15PlanFixture(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "nats", false, "ours")
	baseDir := t.TempDir()
	baseLock := filepath.Join(baseDir, "config", "components.lock.yaml")
	if err := os.MkdirAll(filepath.Dir(baseLock), 0700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(input.PackageRoot, "config", "components.lock.yaml")
	original, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var sections map[string]any
	if err := yaml.Unmarshal(original, &sections); err != nil {
		t.Fatal(err)
	}
	delete(sections, "batchLWS")
	baseBytes, err := yaml.Marshal(sections)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseLock, baseBytes, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(input.BaseRunFile)
	if err != nil {
		t.Fatal(err)
	}
	var base RunManifest
	if err := json.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}
	base.PackageRoot = baseDir
	base.Identity.MaterialsLockDigest = sha256FileHex(baseLock)
	if err := WriteInstallSuccessRecord(filepath.Dir(input.BaseRunFile), base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(original, []byte("\n# additive candidate approval\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatal(err)
	}
	plan := r15ReadPlan(t, out)
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var identity map[string]any
	if err := json.Unmarshal(encoded, &identity); err != nil {
		t.Fatal(err)
	}
	if identity["materialsLockDigest"] != sha256FileHex(lockPath) || identity["baseMaterialsLockDigest"] != sha256FileHex(baseLock) {
		t.Fatalf("plan does not bind original and additive approvals: %v", identity)
	}
	execute := ComponentsExecuteInput{PlanFile: r15OnlyPlanFile(t, out), ConfigFile: input.ConfigFile, PackageRoot: input.PackageRoot, Kubeconfig: input.Kubeconfig, Output: filepath.Join(out, "runtime")}
	t.Setenv("ANI_INSTALL_LOCK", filepath.Join(t.TempDir(), "install.lock"))
	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err != nil {
		t.Fatalf("additive approval must support real read-only noop: %v", err)
	}
	if err := os.WriteFile(lockPath, append(original, []byte("\n# drift after planning\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err == nil || !strings.Contains(err.Error(), "materials changed after planning") {
		t.Fatalf("changed candidate lock must reject before execution: %v", err)
	}
	if err := requireAdditiveMaterials([]byte("batch:\n  image: old\n"), []byte("batch:\n  image: replacement\n")); err == nil {
		t.Fatal("candidate replaced base material")
	}
}
