package ani

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHigressOCIWasmMaterialPreservesManifestAndContent(t *testing.T) {
	root := os.Getenv("ANI_HIGRESS_WASM_SOURCE_ROOT")
	if root == "" {
		t.Skip("fixed official OCI artifacts require ANI_HIGRESS_WASM_SOURCE_ROOT")
	}
	names := []string{"model-router", "key-auth", "key-rate-limit", "ai-statistics"}
	expected := []string{
		"e8b99fe5e302a59ab53e6884109b0c4f4a86db1c62ec681c48fd0da86192229f",
		"c9cddda9b7dc36b8260afd10d12a089556a070dba28b0b17691e18efc0ca545c",
		"c59da1ee175ff04aa3ce18ca465265a51cc4f2bbc901c8f45cc8f8bbaa6ad357",
		"3b6beecc9945290c17ed81fa02a2c592df8c5c4c7c7ac22c0abd85196a0c9da2",
	}
	sources := []string{}
	for _, name := range names {
		sources = append(sources, filepath.Join(root, "higress-oci-"+name))
	}
	out := filepath.Join(t.TempDir(), "artifact")
	err := RunMaterialsPlaceWasm(MaterialsWasmInput{LockPath: filepath.Join("..", "..", "ani", "higress", "wasm.lock.json"), SourceStores: sources, ArtifactRoot: out}, io.Discard)
	if err != nil {
		t.Fatalf("approved OCI Wasm placement failed: %v", err)
	}
	for i, name := range names {
		store := filepath.Join(out, "wasm", name)
		if !blobPresent(filepath.Join(store, "blobs", "sha256", expected[i]), "sha256:"+expected[i]) {
			t.Fatalf("%s manifest bytes differ from the official artifact", name)
		}
		index, err := readStoreIndex(store)
		if err != nil {
			t.Fatal(err)
		}
		if len(index.Manifests) != 1 || index.Manifests[0].Digest != "sha256:"+expected[i] {
			t.Fatalf("%s OCI index lost the original manifest", name)
		}
		manifestBytes, err := os.ReadFile(filepath.Join(store, "blobs", "sha256", expected[i]))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Config ociDescriptor   `json:"config"`
			Layers []ociDescriptor `json:"layers"`
		}
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.Config.MediaType != "application/vnd.oci.empty.v1+json" {
			t.Fatalf("%s was converted to a Pod image", name)
		}
		for _, blob := range append([]ociDescriptor{manifest.Config}, manifest.Layers...) {
			path, err := storeBlobPath(store, blob.Digest)
			if err != nil || !blobPresent(path, blob.Digest) {
				t.Fatalf("%s lacks its exact approved blob %s: %v", name, blob.Digest, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(out, "images")); !os.IsNotExist(err) {
		t.Fatalf("OCI Wasm was placed in Pod image materials: %v", err)
	}
}

func TestHigressOCIWasmRejectsUnapprovedLockBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "changed.lock.json")
	if err := os.WriteFile(lock, []byte(`{"kind":"OCIWasmMaterialLock","plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "artifact")
	if err := RunMaterialsPlaceWasm(MaterialsWasmInput{LockPath: lock, SourceStores: []string{dir}, ArtifactRoot: out}, io.Discard); err == nil {
		t.Fatal("an operator-rehashed unapproved lock was accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("unapproved Wasm material created output before refusal: %v", err)
	}
}

func TestHigressOCIWasmRequiresTheOfflineBlobClosureBeforeWriting(t *testing.T) {
	source := t.TempDir()
	out := filepath.Join(t.TempDir(), "artifact")
	err := RunMaterialsPlaceWasm(MaterialsWasmInput{LockPath: filepath.Join("..", "..", "ani", "higress", "wasm.lock.json"), SourceStores: []string{source}, ArtifactRoot: out}, io.Discard)
	if err == nil {
		t.Fatal("a lock without its offline OCI blobs was accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("incomplete Wasm input created output before refusal: %v", err)
	}
}
