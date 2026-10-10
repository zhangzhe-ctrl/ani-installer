package ani

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const HigressWasmLockSHA256 = "cc4b51b5719a38302792e0f85daca9f09c373d8f9bb32ccbc2887ccc705d3d1d"

// The install and addition paths consume these exact OCI bytes without writing
// a replacement lock or relying on a Pod image table for Wasm approval.
func verifyHigressWasmArtifact(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "wasm/wasm.lock.json"))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != HigressWasmLockSHA256 {
		return fmt.Errorf("Higress OCI Wasm lock differs from embedded source approval")
	}
	var lock struct {
		Kind    string `json:"kind"`
		Plugins []struct {
			Name         string          `json:"name"`
			ArtifactPath string          `json:"artifactPath"`
			Manifest     ociDescriptor   `json:"manifest"`
			Config       ociDescriptor   `json:"config"`
			Layers       []ociDescriptor `json:"layers"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return err
	}
	if lock.Kind != "OCIWasmMaterialLock" || len(lock.Plugins) != 4 {
		return fmt.Errorf("invalid approved OCI Wasm selection")
	}
	for _, p := range lock.Plugins {
		if p.ArtifactPath != "wasm/"+p.Name {
			return fmt.Errorf("unsafe OCI Wasm material path")
		}
		store := filepath.Join(root, p.ArtifactPath)
		index, err := readStoreIndex(store)
		if err != nil || len(index.Manifests) != 1 || index.Manifests[0].Digest != p.Manifest.Digest {
			return fmt.Errorf("%s OCI index differs from approval: %v", p.Name, err)
		}
		for _, d := range append([]ociDescriptor{p.Manifest, p.Config}, p.Layers...) {
			path, err := storeBlobPath(store, d.Digest)
			if err != nil {
				return err
			}
			stat, err := os.Lstat(path)
			if err != nil || !stat.Mode().IsRegular() || stat.Size() != d.Size || !blobPresent(path, d.Digest) {
				return fmt.Errorf("%s required OCI Wasm blob is missing or corrupt: %s", p.Name, d.Digest)
			}
		}
	}
	return nil
}

type MaterialsWasmInput struct {
	LockPath     string
	SourceStores []string
	ArtifactRoot string
}

func RunMaterialsPlaceWasm(input MaterialsWasmInput, stdout io.Writer) error {
	if input.ArtifactRoot == "" || len(input.SourceStores) == 0 {
		return fmt.Errorf("OCI Wasm placement requires artifact-root and source-store")
	}
	raw, err := os.ReadFile(input.LockPath)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != HigressWasmLockSHA256 {
		return fmt.Errorf("OCI Wasm lock differs from the source approval")
	}
	var lock struct {
		APIVersion     string `json:"apiVersion"`
		Kind           string `json:"kind"`
		HigressVersion string `json:"higressVersion"`
		SourceCommit   string `json:"sourceCommit"`
		Plugins        []struct {
			Name               string          `json:"name"`
			Version            string          `json:"version"`
			Type               string          `json:"type"`
			Source             string          `json:"source"`
			Manifest           ociDescriptor   `json:"manifest"`
			Config             ociDescriptor   `json:"config"`
			Layers             []ociDescriptor `json:"layers"`
			WasmDigest         string          `json:"wasmDigest"`
			ArtifactPath       string          `json:"artifactPath"`
			RegistryRepository string          `json:"registryRepository"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return err
	}
	if lock.Kind != "OCIWasmMaterialLock" || len(lock.Plugins) != 4 {
		return fmt.Errorf("invalid fixed OCI Wasm material type/selection")
	}
	output := filepath.Join(input.ArtifactRoot, "wasm")
	if err := checkOutStore(output, append(append([]string{}, input.SourceStores...), filepath.Dir(input.LockPath))); err != nil {
		return err
	}
	type plan struct {
		name, version string
		manifest      ociDescriptor
		files         map[string]string
	}
	plans := []plan{}
	// Resolve and verify the complete closed set before creating output files.
	for _, plugin := range lock.Plugins {
		if plugin.Type != "oci-wasm" || plugin.ArtifactPath != "wasm/"+plugin.Name || plugin.Config.MediaType != "application/vnd.oci.empty.v1+json" {
			return fmt.Errorf("%s is not an approved OCI Wasm artifact", plugin.Name)
		}
		manifestPath, err := findBlob(plugin.Manifest.Digest, input.SourceStores)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var manifest struct {
			SchemaVersion int             `json:"schemaVersion"`
			MediaType     string          `json:"mediaType"`
			Config        ociDescriptor   `json:"config"`
			Layers        []ociDescriptor `json:"layers"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			return err
		}
		if int64(len(body)) != plugin.Manifest.Size || manifest.SchemaVersion != 2 || manifest.MediaType != "application/vnd.oci.image.manifest.v1+json" || !reflect.DeepEqual(manifest.Config, plugin.Config) || !reflect.DeepEqual(manifest.Layers, plugin.Layers) {
			return fmt.Errorf("%s manifest type/descriptor closure differs from approval", plugin.Name)
		}
		p := plan{name: plugin.Name, version: plugin.Version, manifest: plugin.Manifest, files: map[string]string{plugin.Manifest.Digest: manifestPath}}
		wasmLayers := 0
		for _, descriptor := range append([]ociDescriptor{plugin.Config}, plugin.Layers...) {
			path, err := findBlob(descriptor.Digest, input.SourceStores)
			if err != nil {
				return err
			}
			stat, err := os.Stat(path)
			if err != nil {
				return err
			}
			if stat.Size() != descriptor.Size {
				return fmt.Errorf("%s blob size differs from descriptor", plugin.Name)
			}
			if descriptor.MediaType == "application/vnd.module.wasm.content.layer.v1+wasm" {
				wasmLayers++
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				magic := make([]byte, 4)
				_, err = io.ReadFull(f, magic)
				f.Close()
				if err != nil || !bytes.Equal(magic, []byte{0, 'a', 's', 'm'}) || descriptor.Digest != plugin.WasmDigest {
					return fmt.Errorf("%s content is not the approved Wasm module", plugin.Name)
				}
			}
			p.files[descriptor.Digest] = path
		}
		if wasmLayers != 1 {
			return fmt.Errorf("%s needs exactly one Wasm content layer", plugin.Name)
		}
		plans = append(plans, p)
	}
	for _, p := range plans {
		store := filepath.Join(output, p.name)
		if err := os.MkdirAll(filepath.Join(store, "blobs", "sha256"), 0o755); err != nil {
			return err
		}
		for digest, source := range p.files {
			target, err := storeBlobPath(store, digest)
			if err != nil {
				return err
			}
			src, err := os.Open(source)
			if err != nil {
				return err
			}
			dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				src.Close()
				return err
			}
			h := sha256.New()
			_, copyErr := io.Copy(io.MultiWriter(dst, h), src)
			src.Close()
			closeErr := dst.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
				return fmt.Errorf("%s source blob changed during placement", p.name)
			}
		}
		descriptor := p.manifest
		descriptor.Annotations = map[string]string{ociRefNameAnnotation: p.name + ":" + p.version}
		index := ociIndex{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []ociDescriptor{descriptor}}
		body, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(store, "index.json"), append(body, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(store, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(materialWriter(stdout), "OCI Wasm %s:%s placed with original manifest %s and verified content closure\n", p.name, p.version, strings.TrimPrefix(p.manifest.Digest, "sha256:"))
	}
	return os.WriteFile(filepath.Join(output, "wasm.lock.json"), raw, 0o644)
}
