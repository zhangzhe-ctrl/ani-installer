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
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaterialsLockSchemaVersion is the schema the parser understands. The lock
// document itself carries apiVersion ani.installer/v1; new batches append
// sections rather than changing field names.
const MaterialsLockSchemaVersion = "ani.installer/v1"

// InstallerPlatform is the only platform this installer ships images for. The
// lock records the platform manifest digest for it next to the (different)
// source index digest; the two are never compared with each other (R07 T-R07-03).
const InstallerPlatform = "linux/amd64"

// MaterialsLock is the parsed, normalised view of kubekey/ani/components.lock.yaml:
// the approved identity of every file material and image the artifact may carry.
// Parsing is strict about semantics (digest formats, conflicts, completeness) and
// tolerant about prose: the lock document is curated by hand and carries free-text
// fields next to the machine-checked ones.
type MaterialsLock struct {
	Path       string
	APIVersion string
	Kind       string
	LockedAt   string
	LockBatch  string
	// Platform is the single platform this installer ships: every platform
	// digest in the lock belongs to it.
	Platform string

	Images []LockedImage
	Charts []LockedChart
	Tools  []LockedTool

	// Excluded records the images a batch deliberately leaves out of the
	// artifact. They carry no digests and are not verified.
	Excluded []LockedExcluded
}

// LockedImage is one approved image identity.
type LockedImage struct {
	Original             string
	HaulerRef            string
	SourceManifestDigest string // the multi-arch index digest of the source repository; empty for injected images
	PlatformDigest       string // the linux/amd64 manifest digest inside that index
	Platform             string // always InstallerPlatform; recorded explicitly
	Use                  string
	Disabled             bool
	DisabledReason       string
	Injection            *ImageInjection `json:"injection,omitempty"`
}

// ImageInjection records a locally built docker-archive injection. Such an image
// has no source index digest, so the approval chain is the archive hash plus the
// conversion evidence instead.
type ImageInjection struct {
	Type          string
	ArchiveSHA256 string
	Evidence      string
}

// LockedChart is one approved Helm chart material.
type LockedChart struct {
	Name         string
	ChartVersion string
	AppVersion   string
	Source       string
	SHA256       string
	ArtifactPath string
	Release      string
	Namespace    string
}

// LockedTool is one approved binary tool shipped with the artifact.
type LockedTool struct {
	Name                string
	Version             string
	Source              string
	SourceTarballSHA256 string
	BinarySHA256        string
	ArtifactPath        string
	// Provenance records how the digests above were obtained, including how far
	// that evidence reaches: a publisher checksum file is not a signature, and a
	// lock entry that says so is honest, one that stays silent is not.
	ChecksumsFile   string
	ChecksumsSHA256 string
	ReleaseAssetID  string
	TagCommit       string
	IntegrityNote   string
}

// LockedExcluded records an image that is deliberately not shipped.
type LockedExcluded struct {
	Original string
	Reason   string
}

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fileHashPattern  = regexp.MustCompile(`^(?:sha256:)?[0-9a-f]{64}$`)
	gitObjectPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

func isSHA256Digest(value string) bool { return digestPattern.MatchString(value) }

func isFileHash(value string) bool { return fileHashPattern.MatchString(value) }

// rawImage mirrors one image entry of the lock document. Prose fields may vary
// between batches, so the decode is not field-strict; the semantics below are.
type rawImage struct {
	Original             string        `yaml:"original"`
	HaulerRef            string        `yaml:"haulerRef"`
	SourceManifestDigest *string       `yaml:"sourceManifestDigest"`
	AMD64ManifestDigest  *string       `yaml:"amd64ManifestDigest"`
	Use                  string        `yaml:"use"`
	Disabled             bool          `yaml:"disabled"`
	DisabledReason       string        `yaml:"disabledReason"`
	Scope                string        `yaml:"scope"`
	Injection            *rawInjection `yaml:"injection"`
}

type rawInjection struct {
	Type          string `yaml:"type"`
	ArchiveSha256 string `yaml:"archiveSha256"`
	Evidence      string `yaml:"evidence"`
}

// rawChart mirrors one chart entry. Components name their hash chartSha256
// while batch sections use sha256; both are accepted.
type rawChart struct {
	Name           string `yaml:"name"`
	ChartVersion   string `yaml:"chartVersion"`
	AppVersion     string `yaml:"appVersion"`
	Source         string `yaml:"source"`
	SHA256         string `yaml:"sha256"`
	ChartSHA256    string `yaml:"chartSha256"`
	ArtifactPath   string `yaml:"artifactChartPath"`
	Release        string `yaml:"release"`
	Namespace      string `yaml:"namespace"`
	UpstreamNote   string `yaml:"upstreamNote"`
	RenderedWith   string `yaml:"renderedWith"`
	RenderedLines  int    `yaml:"renderedLines"`
	KubeConstraint string `yaml:"kubernetesDeclared"`
}

// rawTool mirrors one tool entry.
type rawTool struct {
	Version             string `yaml:"version"`
	GitVersion          string `yaml:"gitVersion"`
	Source              string `yaml:"source"`
	SourceTarballSha256 string `yaml:"sourceTarballSha256"`
	BinarySha256        string `yaml:"binarySha256"`
	ArtifactPath        string `yaml:"artifactPath"`
	Purpose             string `yaml:"purpose"`
	Status              string `yaml:"status"`
	ChecksumsFile       string `yaml:"checksumsFile"`
	ChecksumsSha256     string `yaml:"checksumsSha256"`
	ReleaseAssetID      string `yaml:"releaseAssetId"`
	TagCommit           string `yaml:"tagCommit"`
	IntegrityNote       string `yaml:"integrityNote"`
}

// rawExcluded mirrors one deliberately-excluded record.
type rawExcluded struct {
	Original string `yaml:"original"`
	Reason   string `yaml:"reason"`
}

// ParseMaterialsLock parses the lock document and validates every machine-checked
// rule. The walk is structural rather than section-name based, so a new batch
// section cannot escape validation by being added somewhere new.
func ParseMaterialsLock(data []byte) (*MaterialsLock, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse materials lock: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("materials lock document is empty")
	}
	lock := &MaterialsLock{Platform: InstallerPlatform}
	problems := collectMaterials(doc.Content[0], "$", lock)
	if len(problems) > 0 {
		return nil, fmt.Errorf("materials lock has %d problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
	}
	if lock.APIVersion != MaterialsLockSchemaVersion {
		problems = append(problems, fmt.Sprintf("$: apiVersion must be %q, got %q", MaterialsLockSchemaVersion, lock.APIVersion))
	}
	if lock.Kind != "ComponentMaterialLock" {
		problems = append(problems, fmt.Sprintf("$: kind must be ComponentMaterialLock, got %q", lock.Kind))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("materials lock has %d problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
	}
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	return lock, nil
}

// LoadMaterialsLock reads and parses the lock file from disk.
func LoadMaterialsLock(path string) (*MaterialsLock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read materials lock %s: %w", path, err)
	}
	lock, err := ParseMaterialsLock(data)
	if err != nil {
		return nil, err
	}
	lock.Path = path
	return lock, nil
}

// collectMaterials walks the lock document and pulls out every image, chart and
// tool entry, wherever a batch section put it. problems accumulates the
// structural problems found while walking (bad entry shapes).
func collectMaterials(node *yaml.Node, path string, lock *MaterialsLock) []string {
	var problems []string
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) > 0 {
			problems = append(problems, collectMaterials(node.Content[0], path, lock)...)
		}
		return problems
	case yaml.SequenceNode:
		for index, item := range node.Content {
			problems = append(problems, collectMaterials(item, fmt.Sprintf("%s[%d]", path, index), lock)...)
		}
		return problems
	case yaml.MappingNode:
	default:
		return problems
	}

	// Collect the direct keys of this mapping once.
	keys := map[string]string{} // key -> value path
	var keyOrder []string
	values := map[string]*yaml.Node{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		keys[key] = fmt.Sprintf("%s.%s", path, key)
		keyOrder = append(keyOrder, key)
		values[key] = node.Content[i+1]
	}

	// Identity of the document itself.
	if v, ok := values["apiVersion"]; ok && v.Kind == yaml.ScalarNode {
		lock.APIVersion = v.Value
	}
	if v, ok := values["kind"]; ok && v.Kind == yaml.ScalarNode {
		lock.Kind = v.Value
	}
	if v, ok := values["lockedAt"]; ok && v.Kind == yaml.ScalarNode {
		lock.LockedAt = v.Value
	}
	if v, ok := values["lockBatch"]; ok && v.Kind == yaml.ScalarNode {
		lock.LockBatch = v.Value
	}

	isImage := hasKey(keys, "original") &&
		(hasKey(keys, "haulerRef") || hasKey(keys, "sourceManifestDigest") || hasKey(keys, "amd64ManifestDigest") || hasKey(keys, "injection"))
	isChart := (hasKey(keys, "sha256") || hasKey(keys, "chartSha256")) && hasKey(keys, "artifactChartPath")
	isTool := hasKey(keys, "binarySha256")

	switch {
	case isImage:
		var raw rawImage
		decodeInto(node, &raw, path, &problems)
		lock.Images = append(lock.Images, normalizeImage(raw, path))
	case isChart:
		var raw rawChart
		decodeInto(node, &raw, path, &problems)
		name := raw.Name
		if name == "" {
			name = pathLeaf(path)
		}
		approvedHash := raw.SHA256
		if approvedHash == "" {
			approvedHash = raw.ChartSHA256
		}
		lock.Charts = append(lock.Charts, LockedChart{
			Name:         name,
			ChartVersion: raw.ChartVersion,
			AppVersion:   raw.AppVersion,
			Source:       raw.Source,
			SHA256:       approvedHash,
			ArtifactPath: raw.ArtifactPath,
			Release:      raw.Release,
			Namespace:    raw.Namespace,
		})
	case isTool:
		var raw rawTool
		decodeInto(node, &raw, path, &problems)
		name := pathLeaf(path)
		lock.Tools = append(lock.Tools, LockedTool{
			Name:                name,
			Version:             raw.Version,
			Source:              raw.Source,
			SourceTarballSHA256: raw.SourceTarballSha256,
			BinarySHA256:        raw.BinarySha256,
			ArtifactPath:        raw.ArtifactPath,
			ChecksumsFile:       raw.ChecksumsFile,
			ChecksumsSHA256:     raw.ChecksumsSha256,
			ReleaseAssetID:      raw.ReleaseAssetID,
			TagCommit:           raw.TagCommit,
			IntegrityNote:       raw.IntegrityNote,
		})
		if raw.ChecksumsSha256 != "" && !isSHA256Digest("sha256:"+raw.ChecksumsSha256) {
			problems = append(problems, fmt.Sprintf("%s: checksumsSha256 %q is not a 64-hex sha256", path, raw.ChecksumsSha256))
		}
		if raw.TagCommit != "" && !gitObjectPattern.MatchString(raw.TagCommit) {
			problems = append(problems, fmt.Sprintf("%s: tagCommit %q is not a hex git object id", path, raw.TagCommit))
		}
		// Declaring a checksum source without saying what was verified is exactly
		// the ambiguity this entry exists to remove.
		if (raw.ChecksumsFile != "" || raw.ChecksumsSha256 != "") && raw.IntegrityNote == "" {
			problems = append(problems, fmt.Sprintf("%s: records a checksum source but no integrityNote stating how far that evidence reaches (checksum published != signature verified)", path))
		}
	case hasKey(keys, "original") && hasKey(keys, "reason"):
		var raw rawExcluded
		decodeInto(node, &raw, path, &problems)
		lock.Excluded = append(lock.Excluded, LockedExcluded{Original: raw.Original, Reason: raw.Reason})
	}

	// A chart hash without its artifact path is a defect: the approved hash has
	// nowhere to be verified against. (An entry that is a chart carries both, so
	// this rule only fires for non-entry mappings.)
	if hasKey(keys, "chartSha256") && !hasKey(keys, "artifactChartPath") {
		problems = append(problems, fmt.Sprintf("%s: chartSha256 without artifactChartPath; the approved hash has nowhere to be verified", path))
		return problems
	}

	// Whether this mapping was an entry or not, its nested values may carry more
	// entries (a chart's own images list, a new batch section, ...): walk them.
	// This is why the cases above do not return: a chart entry that swallowed its
	// nested images list would hide those images from every check (found live
	// while wiring R07.1 — the cert-manager and nats images were invisible).
	for _, key := range keyOrder {
		problems = append(problems, collectMaterials(values[key], keys[key], lock)...)
	}
	return problems
}

func decodeInto(node *yaml.Node, target any, path string, problems *[]string) {
	if err := node.Decode(target); err != nil {
		*problems = append(*problems, fmt.Sprintf("%s: %v", path, err))
	}
}

func hasKey(keys map[string]string, key string) bool {
	_, ok := keys[key]
	return ok
}

func pathLeaf(path string) string {
	parts := strings.Split(path, ".")
	return parts[len(parts)-1]
}

func normalizeImage(raw rawImage, path string) LockedImage {
	entry := LockedImage{
		Original:       raw.Original,
		HaulerRef:      raw.HaulerRef,
		Platform:       InstallerPlatform,
		Use:            raw.Use,
		Disabled:       raw.Disabled,
		DisabledReason: raw.DisabledReason,
	}
	if raw.SourceManifestDigest != nil {
		entry.SourceManifestDigest = strings.TrimSpace(*raw.SourceManifestDigest)
	}
	if raw.AMD64ManifestDigest != nil {
		entry.PlatformDigest = strings.TrimSpace(*raw.AMD64ManifestDigest)
	}
	if raw.Injection != nil {
		entry.Injection = &ImageInjection{
			Type:          raw.Injection.Type,
			ArchiveSHA256: raw.Injection.ArchiveSha256,
			Evidence:      raw.Injection.Evidence,
		}
	}
	return entry
}

// Validate checks every machine-checked rule of the lock. It never compares the
// source index digest with the platform digest: they describe different objects
// and must be allowed to differ.
func (l *MaterialsLock) Validate() error {
	var problems []string

	originals := map[string]string{}
	localRefs := map[string]string{}
	paths := map[string]string{}

	for _, image := range l.Images {
		where := fmt.Sprintf("image %q", image.Original)
		if strings.TrimSpace(image.Original) == "" {
			where = "image entry"
			problems = append(problems, where+": original is empty")
			continue
		}
		if previous, ok := originals[image.Original]; ok {
			problems = append(problems, fmt.Sprintf("%s: original %q is already declared by %s", where, image.Original, previous))
			continue
		}
		originals[image.Original] = image.HaulerRef

		if strings.TrimSpace(image.HaulerRef) == "" {
			problems = append(problems, where+": haulerRef (the local reference) is empty")
		} else if _, err := ManifestURL(image.HaulerRef); err != nil {
			problems = append(problems, fmt.Sprintf("%s: haulerRef %q is not a valid local reference: %v", where, image.HaulerRef, err))
		} else if previous, ok := localRefs[image.HaulerRef]; ok {
			problems = append(problems, fmt.Sprintf("%s: local reference %q conflicts with %s", where, image.HaulerRef, previous))
		} else {
			localRefs[image.HaulerRef] = image.Original
		}

		if image.Disabled {
			// A deliberately disabled entry may carry null digests; whatever is
			// written must still be well-formed.
			problems = append(problems, checkOptionalDigest(where, "sourceManifestDigest", image.SourceManifestDigest)...)
			problems = append(problems, checkOptionalDigest(where, "amd64ManifestDigest", image.PlatformDigest)...)
			continue
		}

		problems = append(problems, checkOptionalDigest(where, "sourceManifestDigest", image.SourceManifestDigest)...)
		problems = append(problems, checkOptionalDigest(where, "amd64ManifestDigest", image.PlatformDigest)...)

		injected := image.Injection != nil
		if injected {
			if image.Injection.Type != "docker-archive" {
				problems = append(problems, fmt.Sprintf("%s: injection.type must be \"docker-archive\", got %q", where, image.Injection.Type))
			}
			if !isSHA256Digest(image.Injection.ArchiveSHA256) {
				problems = append(problems, fmt.Sprintf("%s: injection.archiveSha256 must be sha256:<64 hex>, got %q", where, image.Injection.ArchiveSHA256))
			}
			if strings.TrimSpace(image.Injection.Evidence) == "" {
				problems = append(problems, fmt.Sprintf("%s: injection.evidence must point at the conversion record", where))
			}
		}
		if !injected && image.SourceManifestDigest == "" {
			problems = append(problems, fmt.Sprintf("%s: sourceManifestDigest is missing; an unknown digest must be recorded as disabled/blocked, never fabricated", where))
		}
		if image.PlatformDigest == "" {
			problems = append(problems, fmt.Sprintf("%s: amd64ManifestDigest is missing; the platform manifest digest must be locked", where))
		}
	}

	for _, chart := range l.Charts {
		where := fmt.Sprintf("chart %q", chart.Name)
		if strings.TrimSpace(chart.Name) == "" {
			problems = append(problems, "chart entry: name is empty")
		}
		if !isFileHash(chart.SHA256) {
			problems = append(problems, fmt.Sprintf("%s: sha256 must be 64 hex (optionally sha256: prefixed), got %q", where, chart.SHA256))
		}
		if strings.TrimSpace(chart.ArtifactPath) == "" {
			problems = append(problems, fmt.Sprintf("%s: artifactChartPath is empty", where))
		} else if previous, ok := paths[chart.ArtifactPath]; ok {
			problems = append(problems, fmt.Sprintf("%s: artifact path %q conflicts with %s", where, chart.ArtifactPath, previous))
		} else {
			paths[chart.ArtifactPath] = where
		}
	}

	for _, tool := range l.Tools {
		where := fmt.Sprintf("tool %q", tool.Name)
		if strings.TrimSpace(tool.Name) == "" {
			problems = append(problems, "tool entry: name is empty")
		}
		for field, value := range map[string]string{
			"sourceTarballSha256": tool.SourceTarballSHA256,
			"binarySha256":        tool.BinarySHA256,
		} {
			if !isFileHash(value) {
				problems = append(problems, fmt.Sprintf("%s: %s must be 64 hex (optionally sha256: prefixed), got %q", where, field, value))
			}
		}
		if strings.TrimSpace(tool.ArtifactPath) == "" {
			problems = append(problems, fmt.Sprintf("%s: artifactPath is empty", where))
		} else if previous, ok := paths[tool.ArtifactPath]; ok {
			problems = append(problems, fmt.Sprintf("%s: artifact path %q conflicts with %s", where, tool.ArtifactPath, previous))
		} else {
			paths[tool.ArtifactPath] = where
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("materials lock has %d problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
}

func checkOptionalDigest(where, field, value string) []string {
	if value == "" {
		return nil
	}
	if !isSHA256Digest(value) {
		return []string{fmt.Sprintf("%s: %s must be sha256:<64 hex>, got %q", where, field, value)}
	}
	return nil
}

// ChartByArtifactPath returns the approved chart entry for an artifact path.
func (l *MaterialsLock) ChartByArtifactPath(path string) (*LockedChart, bool) {
	for index := range l.Charts {
		if l.Charts[index].ArtifactPath == path {
			return &l.Charts[index], true
		}
	}
	return nil, false
}

// ToolByArtifactPath returns the approved tool entry for an artifact path.
func (l *MaterialsLock) ToolByArtifactPath(path string) (*LockedTool, bool) {
	for index := range l.Tools {
		if l.Tools[index].ArtifactPath == path {
			return &l.Tools[index], true
		}
	}
	return nil, false
}

// VerifyFileMaterial is the pure check every file material must pass: the
// sha256 of the actual bytes must equal the hash approved in the lock. It takes
// the hash from the lock, never from the package being checked, so regenerating
// an in-package SHA256SUMS cannot make a wrong file acceptable (R07 T-R07-05).
func VerifyFileMaterial(material string, content []byte, approvedHash string) error {
	if !isFileHash(approvedHash) {
		return fmt.Errorf("material %s: approved hash %q is malformed", material, approvedHash)
	}
	sum := sha256.Sum256(content)
	actual := hex.EncodeToString(sum[:])
	want := strings.TrimPrefix(approvedHash, "sha256:")
	if actual != want {
		return fmt.Errorf("material %s: sha256 %s does not match the approved %s", material, actual, want)
	}
	return nil
}

// VerifyImageDigests compares the digests observed for one image against its
// approved entry. The source index digest and the platform manifest digest are
// each checked against their own field; the two are different objects and are
// never compared with each other (R07 T-R07-03).
func VerifyImageDigests(entry LockedImage, observedSource, observedPlatform string) error {
	where := fmt.Sprintf("image %q", entry.Original)
	if entry.Disabled {
		return fmt.Errorf("%s is disabled in the lock; serving it is not approved", where)
	}
	if observedSource != "" {
		if !isSHA256Digest(observedSource) {
			return fmt.Errorf("%s: observed source index digest %q is malformed", where, observedSource)
		}
		if entry.SourceManifestDigest == "" {
			return fmt.Errorf("%s: the lock has no source index digest for this injected image; the observed one cannot be approved", where)
		}
		if observedSource != entry.SourceManifestDigest {
			return fmt.Errorf("%s: observed source index digest %s does not match the approved %s", where, observedSource, entry.SourceManifestDigest)
		}
	}
	if !isSHA256Digest(observedPlatform) {
		return fmt.Errorf("%s: observed platform digest %q is malformed", where, observedPlatform)
	}
	if entry.PlatformDigest == "" {
		return fmt.Errorf("%s: the lock has no platform digest", where)
	}
	if observedPlatform != entry.PlatformDigest {
		return fmt.Errorf("%s: observed platform digest %s does not match the approved %s", where, observedPlatform, entry.PlatformDigest)
	}
	return nil
}

// VerifyImageTableAgainstLock cross-checks the packaged images.tsv against the
// approved lock: every locked image must be present with the exact local
// reference it was approved for. The reverse direction is intentionally not
// asserted here — images.tsv also carries KubeKey-artifact and base images whose
// approval lives outside this lock — but nothing approved may silently vanish
// from the table the installer renders from.
func (l *MaterialsLock) VerifyImageTableAgainstLock(table ImageTable) error {
	var problems []string
	for _, image := range l.Images {
		if image.Disabled {
			continue
		}
		row, ok := table[image.Original]
		if !ok {
			problems = append(problems, fmt.Sprintf("locked image %q is missing from images.tsv", image.Original))
			continue
		}
		if row.HaulerRef != image.HaulerRef {
			problems = append(problems, fmt.Sprintf("locked image %q has haulerRef %q but images.tsv says %q",
				image.Original, image.HaulerRef, row.HaulerRef))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("images.tsv does not match the materials lock:\n%s", strings.Join(problems, "\n"))
}

// ServedManifest is the parsed shape of whatever the registry actually served
// for a locked tag: either a multi-arch index or a single (platform) manifest.
type ServedManifest struct {
	IsIndex           bool
	PlatformDigest    string // an index: the linux/amd64 manifest inside it
	PlatformSize      int64  // an index: the size that manifest declares
	PlatformSizeKnown bool
	ConfigDigest      string   // a single manifest: the config blob digest
	LayerDigests      []string // a single manifest: the layer blob digests
}

// ParseServedImageManifest parses a served OCI/Docker manifest body and returns
// the digests it references. It never assumes which kind was served.
func ParseServedImageManifest(body []byte) (ServedManifest, error) {
	var probe struct {
		Manifests *[]struct {
			Digest   string `json:"digest"`
			Size     *int64 `json:"size"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
		Config *struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ServedManifest{}, fmt.Errorf("served manifest is not valid JSON: %w", err)
	}
	if probe.Manifests != nil {
		// The first match is not enough: an index may list several linux/amd64
		// builds (plain plus a variant), and picking one by order would ship
		// content nobody chose. Exactly one, or refuse.
		matches := []struct {
			digest string
			size   int64
		}{}
		for _, entry := range *probe.Manifests {
			if entry.Platform != nil && entry.Platform.OS == "linux" && entry.Platform.Architecture == "amd64" {
				if !isSHA256Digest(entry.Digest) {
					return ServedManifest{}, fmt.Errorf("served index amd64 entry digest %q is malformed", entry.Digest)
				}
				if entry.Size == nil {
					return ServedManifest{}, fmt.Errorf("served index amd64 entry %s declares no size", entry.Digest)
				}
				matches = append(matches, struct {
					digest string
					size   int64
				}{entry.Digest, *entry.Size})
			}
		}
		if len(matches) == 0 {
			return ServedManifest{}, fmt.Errorf("served index has no linux/amd64 entry")
		}
		if len(matches) > 1 {
			return ServedManifest{}, fmt.Errorf("served index has %d linux/amd64 entries; this installer ships one amd64 object and will not pick between them", len(matches))
		}
		return ServedManifest{IsIndex: true, PlatformDigest: matches[0].digest,
			PlatformSize: matches[0].size, PlatformSizeKnown: true}, nil
	}
	if probe.Config == nil || !isSHA256Digest(probe.Config.Digest) {
		return ServedManifest{}, fmt.Errorf("served manifest has no valid config digest")
	}
	served := ServedManifest{ConfigDigest: probe.Config.Digest}
	for _, layer := range probe.Layers {
		if !isSHA256Digest(layer.Digest) {
			return ServedManifest{}, fmt.Errorf("served manifest references a malformed layer digest %q", layer.Digest)
		}
		served.LayerDigests = append(served.LayerDigests, layer.Digest)
	}
	return served, nil
}

// VerifyServedManifest compares what the registry served with the approved lock
// entry (R07.3). The rules are deliberately asymmetric:
//   - a served multi-arch index must carry the approved source index digest, and
//     its linux/amd64 entry must carry the approved platform digest;
//   - a served single manifest must carry the approved platform digest — the
//     source index digest is NOT compared here, because conversion/local
//     injection legitimately serves an object that never had an index;
//   - an image recorded as an injection must never be served as an index;
//   - every config/layer digest the manifest references must be present.
func VerifyServedManifest(entry LockedImage, servedDigest string, served ServedManifest) error {
	where := fmt.Sprintf("image %q", entry.Original)
	if entry.Disabled {
		return fmt.Errorf("%s is disabled in the lock; serving it is not approved", where)
	}
	if !isSHA256Digest(servedDigest) {
		return fmt.Errorf("%s: served digest %q is malformed", where, servedDigest)
	}
	if served.IsIndex {
		if entry.Injection != nil || entry.SourceManifestDigest == "" {
			return fmt.Errorf("%s: the registry served a multi-arch index, but this image is a local injection with no approved source index digest", where)
		}
		if servedDigest != entry.SourceManifestDigest {
			return fmt.Errorf("%s: served index digest %s does not match the approved source index digest %s",
				where, servedDigest, entry.SourceManifestDigest)
		}
		if served.PlatformDigest != entry.PlatformDigest {
			return fmt.Errorf("%s: the index's linux/amd64 digest %s does not match the approved platform digest %s",
				where, served.PlatformDigest, entry.PlatformDigest)
		}
		return nil
	}
	if servedDigest != entry.PlatformDigest {
		return fmt.Errorf("%s: served manifest digest %s does not match the approved platform digest %s",
			where, servedDigest, entry.PlatformDigest)
	}
	if entry.Injection != nil && strings.TrimSpace(entry.Injection.ArchiveSHA256) == "" {
		return fmt.Errorf("%s: the injection record has no archive hash; the served manifest cannot be approved", where)
	}
	return nil
}

// ImageByOriginal returns the approved lock entry for an original reference.
func (l *MaterialsLock) ImageByOriginal(original string) (*LockedImage, bool) {
	for index := range l.Images {
		if l.Images[index].Original == original {
			return &l.Images[index], true
		}
	}
	return nil, false
}

// RepositoryPath returns the /v2/ repository prefix for a hauler reference, for
// building blob URLs next to ManifestURL.
func RepositoryPath(haulerRef string) (string, error) {
	manifestURL, err := ManifestURL(haulerRef)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(manifestURL, "/manifests/"+manifestURL[strings.LastIndex(manifestURL, "/")+1:]), nil
}
