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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// This file carries the approved-source half of the registry content gate (F06,
// closed by the live round). images.tsv pins the object the SOURCE registry
// served, and packaging ships linux/amd64 only, so the packaged object can be
//
//   - the pinned bytes themselves,
//   - the linux/amd64 child of a pinned multi-arch index, or
//   - the same content re-written by the transport between Docker schema2 and
//     OCI media types.
//
// Anything else must be refused. The pin never changes and stays the root of
// trust: an evidence file is only ever accepted when its own bytes hash to the
// pin, so nothing here can be talked into approving a package by self-declaration.

// imageLandingRuleVersion names the conversion rules below. It is recorded with
// every verdict so a package can be re-checked against the rules that approved it.
const imageLandingRuleVersion = "ani-image-landing/v1"

// landing kinds reported per row.
const (
	landingExact            = "exact"
	landingPlatformSelected = "platform-selected"
	landingConverted        = "converted"
)

// mediaTypeLanding is the only field change the packaging transport may make:
// the same content described with Docker schema2 or OCI media types. Each pair is
// listed in both directions; any media type not in a value set is unknown here
// and therefore refused.
var mediaTypeLanding = map[string]map[string]bool{
	"application/vnd.docker.distribution.manifest.v2+json": {
		"application/vnd.oci.image.manifest.v1+json": true,
	},
	"application/vnd.oci.image.manifest.v1+json": {
		"application/vnd.docker.distribution.manifest.v2+json": true,
	},
	"application/vnd.docker.container.image.v1+json": {
		"application/vnd.oci.image.config.v1+json": true,
	},
	"application/vnd.oci.image.config.v1+json": {
		"application/vnd.docker.container.image.v1+json": true,
	},
	"application/vnd.docker.image.rootfs.diff.tar.gzip": {
		"application/vnd.oci.image.layer.v1.tar+gzip": true,
	},
	"application/vnd.oci.image.layer.v1.tar+gzip": {
		"application/vnd.docker.image.rootfs.diff.tar.gzip": true,
	},
}

var indexMediaTypes = map[string]bool{
	"application/vnd.oci.image.index.v1+json":                   true,
	"application/vnd.docker.distribution.manifest.list.v2+json": true,
}

// EvidenceRoots lists where an artifact carries the approved source manifests:
// the packaged evidence directory of a built artifact, or the development tree's
// equivalent. A missing root only means no row can be approved by derivation,
// which the gate reports instead of silently downgrading.
func EvidenceRoots(packageRoot string) []string {
	root := strings.TrimSpace(packageRoot)
	if root == "" {
		return nil
	}
	return []string{
		filepath.Join(root, "images", "evidence"),
		filepath.Join(root, "ani", "images-evidence"),
	}
}

// ImageEvidence locates the original bytes a pin names. Roots may be a packaged
// evidence directory, an artifact layout, or a preserved OCI store: the same
// content-addressed lookups work for all of them.
type ImageEvidence struct {
	Roots []string
}

var errEvidenceAbsent = errors.New("no evidence file carries these bytes")

// candidatePaths lists where one digest's bytes may live in an evidence root.
func candidatePaths(root, hexDigest string) []string {
	return []string{
		filepath.Join(root, "blobs", "sha256", hexDigest),
		filepath.Join(root, hexDigest+".json"),
		filepath.Join(root, hexDigest),
	}
}

// Manifest reads the bytes whose sha256 is digest from the evidence roots and
// verifies that claim. A file named after a digest is never trusted by its name.
func (e ImageEvidence) Manifest(digest string) ([]byte, string, error) {
	hexDigest := strings.TrimPrefix(strings.TrimSpace(digest), "sha256:")
	if !hex64Pattern.MatchString(hexDigest) {
		return nil, "", errors.Errorf("evidence request for %q is not a sha256 digest", digest)
	}
	for _, root := range e.Roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		for _, candidate := range candidatePaths(root, hexDigest) {
			body, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != hexDigest {
				return nil, candidate, errors.Errorf("%s claims to be sha256:%s but its bytes hash to sha256:%s; the evidence file is corrupt",
					candidate, hexDigest, hex.EncodeToString(sum[:]))
			}
			return body, candidate, nil
		}
	}
	return nil, "", errors.Wrapf(errEvidenceAbsent, "for sha256:%s (looked in %s)", hexDigest, strings.Join(e.Roots, ", "))
}

// approvedObject is the approved platform object for one images.tsv row: what the
// pin names, descended to the object that carries linux/amd64 content.
type approvedObject struct {
	PinBytes       []byte
	PinPath        string
	PinIsIndex     bool
	PlatformDigest string
	PlatformBytes  []byte
	PlatformPath   string
	PlatformDoc    map[string]any
}

// Platform is the digest of the object that must equal the packaged content.
func (a approvedObject) Platform() string {
	if a.PlatformDigest != "" {
		return a.PlatformDigest
	}
	return ""
}

// resolveApproved descends from the pin to the linux/amd64 object. Every step is
// checked by content: the pin's bytes must hash to the pin, an index's amd64
// descriptor must name the child exactly, and the child's bytes must hash to that
// descriptor and be its declared size. Ambiguity is a refusal, never a guess.
func (e ImageEvidence) resolveApproved(row Image) (approvedObject, error) {
	var out approvedObject
	pinBytes, pinPath, err := e.Manifest(row.Digest)
	if err != nil {
		return out, errors.Wrapf(err, "image %s: the approved source manifest", row.Original)
	}
	out.PinBytes, out.PinPath = pinBytes, pinPath
	var doc map[string]any
	if err := decodeJSONNumbers(pinBytes, &doc); err != nil {
		return out, errors.Wrapf(err, "image %s: the approved source manifest at %s is not JSON", row.Original, pinPath)
	}
	mediaType, _ := doc["mediaType"].(string)
	entries, hasEntries := doc["manifests"].([]any)
	if !indexMediaTypes[mediaType] && !hasEntries {
		out.PlatformDigest = row.Digest
		out.PlatformBytes = pinBytes
		out.PlatformPath = pinPath
		out.PlatformDoc = doc
		return out, nil
	}
	out.PinIsIndex = true
	if !indexMediaTypes[mediaType] {
		return out, errors.Errorf("image %s: the pinned object at %s lists manifests but is not an index media type %q",
			row.Original, pinPath, mediaType)
	}
	var matches []map[string]any
	for index, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return out, errors.Errorf("image %s: entry %d of the approved index at %s is not an object",
				row.Original, index+1, pinPath)
		}
		platform, _ := entry["platform"].(map[string]any)
		if platform["os"] == "linux" && platform["architecture"] == "amd64" {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 {
		return out, errors.Errorf("image %s: the approved index at %s has %d linux/amd64 entries; this installer ships one amd64 object and will not pick between them",
			row.Original, pinPath, len(matches))
	}
	entry := matches[0]
	if variant, ok := entry["platform"].(map[string]any)["variant"]; ok && variant != "" && variant != nil {
		return out, errors.Errorf("image %s: the approved index's linux/amd64 entry declares variant %v; the pinned content is not the plain amd64 build this installer ships",
			row.Original, variant)
	}
	childDigest, _ := entry["digest"].(string)
	if childDigest == "" {
		return out, errors.Errorf("image %s: the approved index's linux/amd64 entry names no digest", row.Original)
	}
	childBytes, childPath, err := e.Manifest(childDigest)
	if err != nil {
		return out, errors.Wrapf(err, "image %s: the linux/amd64 manifest %s the approved index points at", row.Original, childDigest)
	}
	var childDoc map[string]any
	if err := decodeJSONNumbers(childBytes, &childDoc); err != nil {
		return out, errors.Wrapf(err, "image %s: the approved amd64 manifest at %s is not JSON", row.Original, childPath)
	}
	if childMedia, _ := childDoc["mediaType"].(string); indexMediaTypes[childMedia] || childDoc["manifests"] != nil {
		return out, errors.Errorf("image %s: the approved index's amd64 entry is itself an index; this installer ships single-platform content", row.Original)
	}
	sizeValue, declared := entrySize(matches[0])
	if !declared {
		return out, errors.Errorf("image %s: the approved index's linux/amd64 entry %s declares no size",
			row.Original, childDigest)
	}
	if sizeValue != int64(len(childBytes)) {
		return out, errors.Errorf("image %s: the approved index declares its amd64 manifest as %d bytes but the evidence holds %d",
			row.Original, sizeValue, len(childBytes))
	}
	out.PlatformDigest, out.PlatformBytes, out.PlatformPath, out.PlatformDoc = childDigest, childBytes, childPath, childDoc
	return out, nil
}

// numberEqual compares JSON numbers across the two documents without assuming a
// Go type: a registry may answer an int where the source wrote a float.
func numberEqual(a, b any) bool {
	toFloat := func(value any) (float64, bool) {
		switch typed := value.(type) {
		case float64:
			return typed, true
		case json.Number:
			parsed, err := typed.Float64()
			return parsed, err == nil
		case int64:
			return float64(typed), true
		}
		return 0, false
	}
	left, leftOK := toFloat(a)
	right, rightOK := toFloat(b)
	return leftOK && rightOK && left == right
}

func mediaTypeOf(object map[string]any) string {
	value, _ := object["mediaType"].(string)
	return value
}

// sameOrLanding accepts a media type that is identical or an approved transport
// re-description of the same content. Anything else is refused with both values
// named, so an unknown media type can never be waved through as "equivalent".
func sameOrLanding(kind, field, approved, served string) string {
	if approved == served {
		return ""
	}
	if mediaTypeLanding[approved][served] {
		return ""
	}
	return kind + " " + field + ": approved media type " + approved + " has no approved landing to " + served
}

// compareDescriptor checks one descriptor (config or layer) for content equality
// with only the allowed media-type mapping, and refuses any field the approved
// side does not carry.
func compareDescriptor(kind string, approved, served map[string]any) string {
	problems := []string{}
	for field := range served {
		if field == "mediaType" {
			continue
		}
		if _, known := approved[field]; !known {
			problems = append(problems, kind+" carries an extra field "+field+" the approved object does not declare")
		}
	}
	for field, approvedValue := range approved {
		if field == "mediaType" {
			continue
		}
		servedValue, present := served[field]
		if !present {
			problems = append(problems, kind+" lost the approved field "+field)
			continue
		}
		switch field {
		case "digest":
			approvedDigest, _ := approvedValue.(string)
			servedDigest, _ := servedValue.(string)
			if approvedDigest != servedDigest {
				problems = append(problems, kind+" digest changed from "+approvedDigest+" to "+servedDigest)
			}
		case "size":
			if !numberEqual(approvedValue, servedValue) {
				problems = append(problems, fmt.Sprintf("%s size changed from %v to %v", kind, approvedValue, servedValue))
			}
		default:
			if !jsonValuesEqual(approvedValue, servedValue) {
				problems = append(problems, kind+" field "+field+" is not the approved value")
			}
		}
	}
	if problem := sameOrLanding(kind, "mediaType", mediaTypeOf(approved), mediaTypeOf(served)); problem != "" {
		problems = append(problems, problem)
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ")
}

// jsonValuesEqual compares two decoded JSON values structurally.
func jsonValuesEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

// compareImageLanding decides how the packaged object relates to the approved
// one. It returns a landing kind, or the first reason the two cannot be the same
// content. Identity is always preferred: bytes equal means exact, and only a
// documented media-type re-description may differ.
func compareImageLanding(approved []byte, served []byte) (string, error) {
	if hexDigestOf(approved) == hexDigestOf(served) {
		return landingExact, nil
	}
	// A last-key-wins decode would let a packaged manifest carry a second,
	// attacker-chosen layer set behind the approved one and still "compare equal".
	if duplicate, err := duplicateJSONKey(approved); err != nil {
		return "", errors.Wrap(err, "the approved manifest is malformed")
	} else if duplicate != "" {
		return "", errors.Errorf("the approved manifest repeats the key %q; a manifest with duplicate keys cannot be compared", duplicate)
	}
	if duplicate, err := duplicateJSONKey(served); err != nil {
		return "", errors.Wrap(err, "the packaged manifest is malformed")
	} else if duplicate != "" {
		return "", errors.Errorf("the packaged manifest repeats the key %q; it is not the approved object", duplicate)
	}
	var approvedDoc, servedDoc map[string]any
	if err := json.Unmarshal(approved, &approvedDoc); err != nil {
		return "", errors.Wrap(err, "the approved manifest is not JSON")
	}
	if err := json.Unmarshal(served, &servedDoc); err != nil {
		return "", errors.Wrap(err, "the packaged manifest is not JSON")
	}
	if indexMediaTypes[mediaTypeOf(approvedDoc)] || approvedDoc["manifests"] != nil {
		return "", errors.New("the approved object is an index and the packaged object is not byte-identical to it; descend to the platform manifest first")
	}
	if indexMediaTypes[mediaTypeOf(servedDoc)] || servedDoc["manifests"] != nil {
		return "", errors.New("the packaged object is an index while the approved object is a single manifest")
	}
	problems := []string{}
	for field := range servedDoc {
		if _, known := approvedDoc[field]; !known && field != "mediaType" {
			problems = append(problems, "the packaged manifest adds top-level field "+field)
		}
	}
	for field, approvedValue := range approvedDoc {
		if field == "mediaType" {
			continue
		}
		servedValue, present := servedDoc[field]
		if !present {
			problems = append(problems, "the packaged manifest drops top-level field "+field)
			continue
		}
		switch field {
		case "schemaVersion":
			if !numberEqual(approvedValue, servedValue) {
				problems = append(problems, "schemaVersion changed")
			}
		case "config":
			approvedConfig, _ := approvedValue.(map[string]any)
			servedConfig, _ := servedValue.(map[string]any)
			if problem := compareDescriptor("config", approvedConfig, servedConfig); problem != "" {
				problems = append(problems, problem)
			}
		case "layers":
			approvedLayers, _ := approvedValue.([]any)
			servedLayers, _ := servedValue.([]any)
			if len(approvedLayers) != len(servedLayers) {
				problems = append(problems, fmt.Sprintf("layer count changed from %d to %d", len(approvedLayers), len(servedLayers)))
				continue
			}
			for index := range approvedLayers {
				approvedLayer, _ := approvedLayers[index].(map[string]any)
				servedLayer, _ := servedLayers[index].(map[string]any)
				if problem := compareDescriptor(fmt.Sprintf("layer %d", index+1), approvedLayer, servedLayer); problem != "" {
					problems = append(problems, problem)
				}
			}
		default:
			if !jsonValuesEqual(approvedValue, servedValue) {
				problems = append(problems, "top-level field "+field+" is not the approved value")
			}
		}
	}
	if problem := sameOrLanding("manifest", "mediaType", mediaTypeOf(approvedDoc), mediaTypeOf(servedDoc)); problem != "" {
		problems = append(problems, problem)
	}
	if len(problems) > 0 {
		return "", errors.Errorf("the packaged object is not the approved content: %s", strings.Join(problems, "; "))
	}
	return landingConverted, nil
}

// hex64Pattern is a bare sha256 digest, and nothing that could escape a
// content-addressed directory.
var hex64Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// entrySize reads a descriptor's declared size, which is required.
func entrySize(entry map[string]any) (int64, bool) {
	switch value := entry["size"].(type) {
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	case float64:
		return int64(value), true
	}
	return 0, false
}

// decodeJSONNumbers decodes keeping every number exact, so a declared size is
// never silently lost to float rounding.
func decodeJSONNumbers(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decoder.Decode(target)
}

// duplicateJSONKey returns the first key a JSON object states twice, because a
// decoding map keeps only the last value and would hide the first.
func duplicateJSONKey(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var path []string
	found := ""
	var walk func(depth int) error
	walk = func(depth int) error {
		first, err := decoder.Token()
		if err != nil {
			return err
		}
		switch open := first.(type) {
		case json.Delim:
			switch open {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return err
					}
					key, _ := keyToken.(string)
					if seen[key] && found == "" {
						found = strings.Join(append(append([]string(nil), path...), key), ".")
					}
					seen[key] = true
					path = append(path, key)
					if err := walk(depth + 1); err != nil {
						return err
					}
					path = path[:len(path)-1]
				}
				_, err = decoder.Token()
				return err
			case '[':
				for index := 0; decoder.More(); index++ {
					path = append(path, strconv.Itoa(index))
					if err := walk(depth + 1); err != nil {
						return err
					}
					path = path[:len(path)-1]
				}
				_, err = decoder.Token()
				return err
			}
			return nil
		default:
			return nil
		}
	}
	if err := walk(0); err != nil {
		return "", err
	}
	return found, nil
}

func hexDigestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
