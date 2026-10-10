package ani

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// The same source-bound approval pattern used by Kubeflow: package checksums
// alone cannot approve replacement component drivers or shared CRD material.
//
//go:embed higress-assets.lock.json
var higressAssetsApproval []byte

func verifyHigressAssets(root string) error {
	var approval struct {
		Schema  string            `json:"schema"`
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if err := json.Unmarshal(higressAssetsApproval, &approval); err != nil {
		return err
	}
	if approval.Schema != "ani.higress.assets.v1" || approval.Version != HigressVersion || len(approval.Files) != 7 {
		return fmt.Errorf("invalid embedded Higress asset approval")
	}
	dir := filepath.Join(root, "manifests/higress", HigressVersion)
	lock, err := os.ReadFile(filepath.Join(dir, "assets.lock.json"))
	if err != nil || !bytes.Equal(lock, higressAssetsApproval) {
		return fmt.Errorf("Higress asset lock differs from this binary source approval: %v", err)
	}
	for name, hash := range approval.Files {
		if filepath.Base(name) != name || !isHex64(hash) {
			return fmt.Errorf("unsafe Higress asset approval")
		}
		if err := VerifyFileMaterialDigest(filepath.Join(dir, name), hash); err != nil {
			return fmt.Errorf("Higress source-bound asset: %w", err)
		}
	}
	return nil
}
