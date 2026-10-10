package ani

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// The base success record remains immutable. An additive candidate may append
// top-level material batches, but every original batch, tool, pin and provenance
// must still be identical. The original bytes are checked against the digest
// that the first install attested, not against a newly generated checksum file.
func bindComponentsMaterials(base RunManifest, packageRoot string) (string, string, error) {
	path := filepath.Join(packageRoot, "config", "components.lock.yaml")
	current := sha256FileHex(path)
	expected := strings.TrimPrefix(base.Identity.MaterialsLockDigest, "sha256:")
	if current == "" {
		return "", "", fmt.Errorf("cannot read candidate materials lock %s", path)
	}
	if expected == "" || current == expected {
		return current, expected, nil
	}
	if strings.TrimSpace(base.PackageRoot) == "" {
		return "", "", fmt.Errorf("additive materials require the original packageRoot from the install-success record")
	}
	originalPath := filepath.Join(base.PackageRoot, "config", "components.lock.yaml")
	if sha256FileHex(originalPath) != expected {
		return "", "", fmt.Errorf("original materials lock %s does not match install-success identity %s", originalPath, expected)
	}
	original, err := os.ReadFile(originalPath)
	if err != nil {
		return "", "", err
	}
	candidate, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if err := requireAdditiveMaterials(original, candidate); err != nil {
		return "", "", err
	}
	return current, expected, nil
}

func requireAdditiveMaterials(original, candidate []byte) error {
	var before, after map[string]any
	if err := yaml.Unmarshal(original, &before); err != nil {
		return err
	}
	if err := yaml.Unmarshal(candidate, &after); err != nil {
		return err
	}
	for name, value := range before {
		if name == "lockedAt" || name == "lockBatch" {
			continue
		}
		if !reflect.DeepEqual(value, after[name]) {
			return fmt.Errorf("candidate changes original material section %q; components addition cannot upgrade or replace base materials", name)
		}
	}
	return nil
}
