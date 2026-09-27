/*
Copyright 2026 The KubeSphere Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/errors"
)

// This step is how the live round closed B-1 and B-2. Hauler re-writes a pulled
// manifest when the source object is Docker schema2, and a store assembled from a
// floating tag carries whatever that tag pointed at, so the packaged object can
// stop being the one images.tsv pins. Instead of loosening the pin or teaching the
// gate to forgive publisher metadata, packaging lands the approved bytes: the
// manifest the pin names, its linux/amd64 child when the pin is an index, plus
// exactly the config and layer blobs those manifests reference.
//
// The output is always a new store. An input store is only ever read, so this
// step cannot quietly rewrite the material another package was verified from.

// MaterialsLandInput describes one landing run.
type MaterialsLandInput struct {
	// ImagesTSVPath is the approved table whose pins decide what is correct.
	ImagesTSVPath string
	// EvidenceDirs hold the approved manifest bytes (content-addressed roots).
	EvidenceDirs []string
	// SourceStores are existing hauler stores that may already carry the blobs.
	SourceStores []string
	// OutStore is the new store directory. It must not exist, or must be empty.
	OutStore string
	// LockPath, when set, cross-checks every landed pair against the lock.
	LockPath string
	// CheckStores are the input stores whose own index must not contradict the
	// approved material. Landing never repairs a wrong package quietly: an input
	// that serves something other than the pin, or other than that pin's
	// linux/amd64 child, is refused instead of being overwritten.
	CheckStores []string
}

// ociIndex is the part of an OCI layout index this step reads and writes.
type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType,omitempty"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

const ociRefNameAnnotation = "org.opencontainers.image.ref.name"

// readStoreIndex loads a store's index.json.
func readStoreIndex(store string) (ociIndex, error) {
	var index ociIndex
	data, err := os.ReadFile(filepath.Join(store, "index.json"))
	if err != nil {
		return index, errors.Wrapf(err, "read the store index of %s", store)
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, errors.Wrapf(err, "parse the store index of %s", store)
	}
	return index, nil
}

// storeBlobPath resolves a digest inside a store, refusing to leave its directory.
func storeBlobPath(store, digest string) (string, error) {
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if len(hexDigest) != 64 || strings.ContainsAny(hexDigest, "/.\\") {
		return "", errors.Errorf("%q is not a sha256 digest", digest)
	}
	return filepath.Join(store, "blobs", "sha256", hexDigest), nil
}

// blobPresent verifies a candidate blob by hashing its bytes. A file that merely
// exists under the right name proves nothing.
func blobPresent(path string, digest string) bool {
	// A zero-length object is not a missing one: sha256:e3b0c44298fc1c14... is the
	// empty layer a real image may legitimately carry. What decides is the hash.
	if _, err := os.Stat(path); err != nil {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false
	}
	return "sha256:"+hex.EncodeToString(hasher.Sum(nil)) == digest
}

// findBlob looks for one digest across the source stores and evidence roots,
// always checking the bytes rather than the file name.
func findBlob(digest string, sources []string) (string, error) {
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	for _, root := range sources {
		candidates := []string{filepath.Join(root, "blobs", "sha256", hexDigest)}
		if strings.TrimSpace(root) == "" {
			continue
		}
		for _, candidate := range candidates {
			if blobPresent(candidate, digest) {
				return candidate, nil
			}
		}
	}
	return "", errors.Errorf("no source store carries blob %s with matching bytes", digest)
}

// manifestBlobs lists the config and layer digests a manifest references, in the
// order a pull needs them.
func manifestBlobs(body []byte) ([]string, error) {
	var probe struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, errors.Wrap(err, "parse the approved manifest for blobs")
	}
	blobs := []string{}
	if probe.Config.Digest == "" {
		return nil, errors.New("the approved manifest names no config blob")
	}
	blobs = append(blobs, probe.Config.Digest)
	for _, layer := range probe.Layers {
		if layer.Digest == "" {
			return nil, errors.New("the approved manifest names a layer without a digest")
		}
		blobs = append(blobs, layer.Digest)
	}
	return blobs, nil
}

// RunMaterialsLandApprovedImages builds a store that carries the approved content
// for every images.tsv row: verified blobs, the approved manifest bytes, and one
// tag per approved hauler reference pointing at exactly that manifest.
func RunMaterialsLandApprovedImages(input MaterialsLandInput, stdout io.Writer) error {
	if strings.TrimSpace(input.ImagesTSVPath) == "" || strings.TrimSpace(input.OutStore) == "" {
		return errors.New("--images-tsv and --out-store are required")
	}
	if len(input.EvidenceDirs) == 0 {
		return errors.New("--evidence-dir is required: the approved manifest bytes are what this step lands")
	}
	if err := checkOutStore(input.OutStore, append(append([]string(nil), input.EvidenceDirs...),
		append(input.SourceStores, input.CheckStores...)...)); err != nil {
		return err
	}
	tableData, err := os.ReadFile(input.ImagesTSVPath)
	if err != nil {
		return errors.Wrapf(err, "read the image table %s", input.ImagesTSVPath)
	}
	table, err := LoadImageTable(strings.Split(string(tableData), "\n"))
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return fmt.Errorf("the image table %s has no rows", input.ImagesTSVPath)
	}
	var lock *MaterialsLock
	if strings.TrimSpace(input.LockPath) != "" {
		lock, err = LoadMaterialsLock(input.LockPath)
		if err != nil {
			return err
		}
	}
	out := materialWriter(stdout)
	evidence := ImageEvidence{Roots: input.EvidenceDirs}
	sources := append(append([]string{}, input.EvidenceDirs...), input.SourceStores...)
	checkStores := input.CheckStores
	if len(checkStores) == 0 {
		checkStores = append([]string(nil), input.SourceStores...)
	}
	inputTags := map[string][]string{}
	for _, store := range checkStores {
		index, err := readStoreIndex(store)
		if err != nil {
			continue // not a store we can read; landing still verifies every byte it copies
		}
		for _, entry := range index.Manifests {
			if name := entry.Annotations[ociRefNameAnnotation]; name != "" {
				inputTags[name] = append(inputTags[name], entry.Digest)
			}
		}
	}

	if err := os.MkdirAll(filepath.Join(input.OutStore, "blobs", "sha256"), 0o755); err != nil {
		return errors.Wrap(err, "create the output store")
	}
	if err := os.WriteFile(filepath.Join(input.OutStore, "oci-layout"),
		[]byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		return errors.Wrap(err, "write the output store layout marker")
	}

	originals := make([]string, 0, len(table))
	for original := range table {
		originals = append(originals, original)
	}
	sort.Strings(originals)

	index := ociIndex{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json"}
	landed := 0
	for _, original := range originals {
		row := table[original]
		approved, err := evidence.resolveApproved(row)
		if err != nil {
			return errors.Wrapf(err, "land %s", original)
		}
		if lock != nil {
			if entry, locked := lock.ImageByOriginal(original); locked {
				if err := checkLockAgainstApproved(row, approved, *entry, false); err != nil {
					return err
				}
			}
		}
		// An input store that already serves this reference must be serving the
		// approved object or the pinned index it derives from. Landing never
		// papers over a store that carries something else.
		tagged := row.HaulerRef[strings.Index(row.HaulerRef, "/")+1:]
		for _, served := range inputTags[tagged] {
			if served == approved.PlatformDigest || served == row.Digest {
				continue
			}
			return errors.Errorf("image %s: the input store serves %s under %s, which is neither the approved pin %s nor that pin's linux/amd64 object %s; the packaged content does not match the pinned transport identity and landing will not overwrite it",
				original, served, tagged, row.Digest, approved.PlatformDigest)
		}
		path, err := storeBlobPath(input.OutStore, approved.PlatformDigest)
		if err != nil {
			return errors.Wrapf(err, "land %s", original)
		}
		if !blobPresent(path, approved.PlatformDigest) {
			if err := os.WriteFile(path, approved.PlatformBytes, 0o644); err != nil {
				return errors.Wrapf(err, "write the approved manifest of %s", original)
			}
			if !blobPresent(path, approved.PlatformDigest) {
				return errors.Errorf("the landed manifest of %s does not hash to %s", original, approved.PlatformDigest)
			}
		}
		// Every blob the approved manifest references has to be in the store:
		// a manifest whose layers are absent is a claim, not an image.
		blobs, err := manifestBlobs(approved.PlatformBytes)
		if err != nil {
			return errors.Wrapf(err, "land %s", original)
		}
		for _, digest := range blobs {
			destination, err := storeBlobPath(input.OutStore, digest)
			if err != nil {
				return errors.Wrapf(err, "land %s", original)
			}
			if blobPresent(destination, digest) {
				continue
			}
			source, err := findBlob(digest, sources)
			if err != nil {
				return errors.Wrapf(err, "image %s: the approved manifest references", original)
			}
			if err := copyFileVerified(source, destination, strings.TrimPrefix(digest, "sha256:"), 0o644); err != nil {
				return errors.Wrapf(err, "image %s: copy blob %s", original, digest)
			}
		}
		var doc struct {
			MediaType string `json:"mediaType"`
		}
		if err := json.Unmarshal(approved.PlatformBytes, &doc); err != nil {
			return errors.Wrapf(err, "image %s: the approved manifest is not JSON", original)
		}
		mediaType := doc.MediaType
		if mediaType == "" {
			mediaType = "application/vnd.oci.image.manifest.v1+json"
		}
		if _, err := RepositoryPath(row.HaulerRef); err != nil {
			return errors.Wrapf(err, "image %s", original)
		}
		// The store's tag is the reference minus its registry host, which is how
		// the approved table names it and how a registry serves it.
		refName := row.HaulerRef[strings.Index(row.HaulerRef, "/")+1:]
		index.Manifests = append(index.Manifests, ociDescriptor{
			MediaType:   mediaType,
			Digest:      approved.PlatformDigest,
			Size:        int64(len(approved.PlatformBytes)),
			Annotations: map[string]string{ociRefNameAnnotation: refName},
		})
		kind := "manifest"
		if approved.PinIsIndex {
			kind = "index->amd64"
		}
		fmt.Fprintf(out, "landed %s: %s pin %s, packaged manifest %s (%d bytes, %d blob reference(s))\n",
			original, kind, shortDigest(row.Digest), shortDigest(approved.PlatformDigest),
			len(approved.PlatformBytes), len(blobs))
		landed++
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the output store index")
	}
	if err := os.WriteFile(filepath.Join(input.OutStore, "index.json"), append(data, '\n'), 0o644); err != nil {
		return errors.Wrap(err, "write the output store index")
	}
	fmt.Fprintf(out, "output store %s carries %d approved image(s) and only the blobs those manifests reference\n",
		input.OutStore, landed)
	return nil
}

// refuseExistingStore keeps this step from rewriting a store that something else
// already verified: the output must be new.
func refuseExistingStore(dir string) error {
	return checkOutStore(dir, nil)
}

// checkOutStore keeps landing from writing into anything it also reads: a store it
// would "prune" may be the only surviving copy of the approved material, and an
// output nested inside one would mix the two.
func checkOutStore(dir string, readOnly []string) error {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return errors.Wrapf(err, "resolve the output store %s", dir)
	}
	for _, input := range readOnly {
		resolved, err := filepath.Abs(input)
		if err != nil {
			continue
		}
		if resolved == absolute || strings.HasPrefix(absolute+string(filepath.Separator), resolved+string(filepath.Separator)) {
			return errors.Errorf("refusing to write %s inside the read-only input %s: landing must not touch the material it verifies against", dir, input)
		}
		if strings.HasPrefix(resolved+string(filepath.Separator), absolute+string(filepath.Separator)) {
			return errors.Errorf("refusing to write %s around the read-only input %s: landing must not touch the material it verifies against", dir, input)
		}
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "check the output store %s", dir)
	}
	if len(entries) > 0 {
		return errors.Errorf("refusing to write into an existing store %s: landing produces a new store so no already-verified material is rewritten in place", dir)
	}
	return nil
}
