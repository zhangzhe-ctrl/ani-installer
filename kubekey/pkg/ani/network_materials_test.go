package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKCNSourceIndexCannotMasqueradeAsRuntimePlatform(t *testing.T) {
	table := testImageTable()
	image := table[KCNImageReference]
	image.Digest = strings.Split(KCNImageReference, "@")[1]
	table[KCNImageReference] = image
	if _, err := KubeKeyConfig(validConfig(), "/offline/packages/kubekey-artifact.tgz", "/offline", table); err == nil {
		t.Fatal("source OCI index was accepted as the offline amd64 manifest")
	}
}

func TestControlledNetworkMaterialsMatchSourceLock(t *testing.T) {
	for _, role := range []string{"kcn", "envoy"} {
		var lock struct {
			Files               map[string]string `json:"files"`
			SourceImage         string            `json:"sourceImage"`
			AMD64ManifestDigest string            `json:"amd64ManifestDigest"`
		}
		data, err := os.ReadFile(filepath.Join("..", "..", "ani", role, "materials.lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &lock); err != nil {
			t.Fatal(err)
		}
		if len(lock.Files) < 2 {
			t.Fatal("network material has no complete static file index")
		}
		if role == "kcn" && lock.SourceImage != KCNImageReference {
			t.Fatal("KCN source image lock differs")
		}
		if role == "kcn" && lock.AMD64ManifestDigest != KCNAMD64ManifestDigest {
			t.Fatal("KCN supplied index/platform correspondence differs")
		}
		for name, expected := range lock.Files {
			data, err := os.ReadFile(filepath.Join("..", "..", "builtin", "core", "roles", "ani", role, "templates", name))
			if err != nil {
				t.Fatal(err)
			}
			actual := sha256.Sum256(data)
			if hex.EncodeToString(actual[:]) != expected {
				t.Fatalf("%s/%s differs from controlled upstream material generation", role, name)
			}
		}
	}
}
