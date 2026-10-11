package ani

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestKubeflowLifecyclePatchPreflightConcurrency(t *testing.T) {
	contracts, err := filepath.Abs(filepath.Join("..", "..", "ani", "kubeflow", "stage2", "probe-contracts.py"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-I", filepath.Join("testdata", "kubeflow_patch_feedback.py"), contracts)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native lifecycle patch feedback: %v\n%s", err, output)
	}
}
