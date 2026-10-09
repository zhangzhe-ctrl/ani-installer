package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestControlledNetworkMaterialsMatchSourceLock(t *testing.T) {
	for _, role := range []string{"kcn", "envoy"} {
		var lock struct {
			Files       map[string]string `json:"files"`
			SourceImage string            `json:"sourceImage"`
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
