package ani

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The running kk carries the reviewed source approval. Regenerating the
// artifact's SHA256SUMS or its own asset lock cannot approve different scripts.
//
//go:embed kubeflow-assets.lock.json
var kubeflowAssetsApproval []byte

func verifyKubeflowAssets(artifactRoot string) error {
	var approved struct {
		Schema  string            `json:"schema"`
		Release string            `json:"release"`
		Files   map[string]string `json:"files"`
	}
	if err := json.Unmarshal(kubeflowAssetsApproval, &approved); err != nil {
		return fmt.Errorf("decode embedded Kubeflow asset approval: %w", err)
	}
	if approved.Schema != "ani.kubeflow.assets.v1" || approved.Release != KubeflowRelease || len(approved.Files) < 7 {
		return fmt.Errorf("Kubeflow assets lack a complete source-bound approval")
	}
	root := filepath.Join(artifactRoot, "manifests", "kubeflow", KubeflowRelease)
	lock, err := os.ReadFile(filepath.Join(root, "assets.lock.json"))
	if err != nil {
		return fmt.Errorf("required Kubeflow first-install assets: %w", err)
	}
	if !bytes.Equal(lock, kubeflowAssetsApproval) {
		return fmt.Errorf("Kubeflow asset lock differs from the approval embedded in this kk")
	}
	for name, digest := range approved.Files {
		if filepath.Base(name) != name || strings.Contains(name, "..") || !isHex64(digest) {
			return fmt.Errorf("unsafe embedded Kubeflow asset binding %q", name)
		}
		if err := VerifyFileMaterialDigest(filepath.Join(root, name), digest); err != nil {
			return fmt.Errorf("source-bound Kubeflow asset: %w", err)
		}
	}
	return nil
}

// The wheel approval was already bound to this kk by verifyKubeflowAssets.
// Physical wheel bytes are part of the closed first-install delivery, even
// though deployed steps consume the already-built SDK image without pip.
func verifyKubeflowWheels(artifactRoot string) error {
	root := filepath.Join(artifactRoot, "manifests", "kubeflow", KubeflowRelease)
	if err := verifyKubeflowWheelSet(root, "execution-image.lock.json", "wheels", "ani.kubeflow.execution-image.v1"); err != nil {
		return err
	}
	return verifyKubeflowWheelSet(root, "workspace-image.lock.json", "workspace-wheels", "ani.kubeflow.workspace-image.v1")
}

func verifyKubeflowWheelSet(root, lockFile, directory, schema string) error {
	data, err := os.ReadFile(filepath.Join(root, lockFile))
	if err != nil {
		return fmt.Errorf("required SDK wheel approval: %w", err)
	}
	var approval struct {
		Schema string `json:"schema"`
		Wheels []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"wheels"`
	}
	if err := json.Unmarshal(data, &approval); err != nil {
		return fmt.Errorf("decode SDK wheel approval: %w", err)
	}
	if approval.Schema != schema || len(approval.Wheels) == 0 {
		return fmt.Errorf("SDK wheel approval is empty or unsupported")
	}
	entries, err := os.ReadDir(filepath.Join(root, directory))
	if err != nil || len(entries) != len(approval.Wheels) {
		return fmt.Errorf("physical SDK wheel set differs from source approval: %v", err)
	}
	seen := map[string]bool{}
	for _, wheel := range approval.Wheels {
		if filepath.Base(wheel.File) != wheel.File || strings.Contains(wheel.File, "..") || !isHex64(wheel.SHA256) || seen[wheel.File] {
			return fmt.Errorf("unsafe or duplicate wheel binding %q", wheel.File)
		}
		seen[wheel.File] = true
		path := filepath.Join(root, directory, wheel.File)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("required wheel is not a regular file: %s", wheel.File)
		}
		if err := VerifyFileMaterialDigest(path, wheel.SHA256); err != nil {
			return fmt.Errorf("source-bound SDK wheel: %w", err)
		}
	}
	return nil
}
