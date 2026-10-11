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
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := func() string {
		probe := exec.Command("bash", "-c", `source "$1"; ani_tree_fingerprint "$2"`, "lifecycle-source",
			filepath.Join(root, "scripts", "release-guard.sh"), root)
		out, err := probe.CombinedOutput()
		if err != nil {
			t.Fatalf("lifecycle source fingerprint: %v\n%s", err, out)
		}
		return string(out)
	}
	before := fingerprint()
	cmd := exec.Command("python3", "-I", "-B", filepath.Join("testdata", "kubeflow_patch_feedback.py"), contracts)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native lifecycle patch feedback: %v\n%s", err, output)
	}
	if after := fingerprint(); after != before {
		t.Fatalf("native lifecycle feedback changed the approved source tree: before=%s after=%s", before, after)
	}
}

func TestKubeflowStoppedRunSparseHistory(t *testing.T) {
	probe, err := filepath.Abs(filepath.Join("..", "..", "ani", "kubeflow", "probes", "run.py"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-I", "-B", filepath.Join("testdata", "kubeflow_stop_history_feedback.py"), probe)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native stop history feedback: %v\n%s", err, output)
	}
}
