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
