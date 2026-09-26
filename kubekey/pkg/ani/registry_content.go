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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/pkg/errors"
)

// This file is the ONE registry-content checker (F06, extended by the live round
// with approved-source derivation). Packaging, an existing archive, the first
// install and a component addition all go through registryContentChecker.Verify,
// so no path can be checked by a weaker rule than another: the same pin, the same
// evidence, the same blob bytes and the same lock.

// registryObject is one manifest object as the registry served it: the raw
// bytes, their digest, and the digests they reference.
type registryObject struct {
	Reference string // the tag or digest the object was fetched by
	Digest    string // sha256 of the served bytes — the object's identity
	Body      []byte
	Manifest  ServedManifest
}

// resolvedImage is everything the registry serves for one images.tsv row,
// followed down to the object that carries this platform's content. A served
// multi-arch index is never the end of the check: its linux/amd64 sub-manifest
// is fetched too, because the index alone says nothing about the layers.
type resolvedImage struct {
	Row      Image
	Served   registryObject
	Platform registryObject // the object whose config/layers must exist
}

// manifestURLForReference builds the /v2/ URL for one reference (tag or digest)
// inside the repository a hauler reference names.
func manifestURLForReference(haulerRef, reference string) (string, error) {
	repoPath, err := RepositoryPath(haulerRef)
	if err != nil {
		return "", err
	}
	return repoPath + "/manifests/" + url.PathEscape(reference), nil
}

// manifestAccept lists the manifest media types the offline registry may answer with.
const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

// registryHTTPError carries the status a registry answered with, so a caller can
// tell "this object is absent" from "the registry or the object is broken"
// without matching on the message text.
type registryHTTPError struct {
	StatusCode int
	Kind       string // manifest or blob
	Reference  string
	HaulerRef  string
}

func (e *registryHTTPError) Error() string {
	return fmt.Sprintf("%s %s of %s returned HTTP %d", e.Kind, e.Reference, e.HaulerRef, e.StatusCode)
}

// absentInRegistry reports whether the failure is the registry saying the object
// simply is not there.
func absentInRegistry(err error) bool {
	return registryHTTPStatus(err) == http.StatusNotFound
}

// registryHTTPStatus returns the HTTP status a registry answered with, or 0 when
// the failure was not an HTTP answer at all.
func registryHTTPStatus(err error) int {
	var httpErr *registryHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

// fetchRegistryManifest reads one manifest reference from the offline registry.
// A non-200 answer is an error with the registry's own status: absence must be
// reported as absence, never as "verified".
func fetchRegistryManifest(ctx context.Context, client *http.Client, registryAddress, haulerRef, reference string) (registryObject, error) {
	path, err := manifestURLForReference(haulerRef, reference)
	if err != nil {
		return registryObject{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s%s", registryAddress, path), nil)
	if err != nil {
		return registryObject{}, errors.Wrapf(err, "build manifest request for %s", reference)
	}
	request.Header.Set("Accept", manifestAccept)
	response, err := client.Do(request)
	if err != nil {
		return registryObject{}, errors.Wrapf(err, "request manifest %s of %s", reference, haulerRef)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return registryObject{}, errors.Wrapf(err, "read manifest %s of %s", reference, haulerRef)
	}
	if response.StatusCode != http.StatusOK {
		return registryObject{}, &registryHTTPError{
			StatusCode: response.StatusCode,
			Kind:       "manifest",
			Reference:  reference,
			HaulerRef:  haulerRef,
		}
	}
	sum := sha256.Sum256(body)
	manifest, err := ParseServedImageManifest(body)
	if err != nil {
		return registryObject{}, fmt.Errorf("manifest %s of %s: %w", reference, haulerRef, err)
	}
	return registryObject{
		Reference: reference,
		Digest:    fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:])),
		Body:      body,
		Manifest:  manifest,
	}, nil
}

// resolveRegistryImage fetches the row's tag and follows an index down to the
// platform manifest, so the digests that must exist as blobs are known.
func resolveRegistryImage(ctx context.Context, client *http.Client, registryAddress string, row Image) (resolvedImage, error) {
	path, err := ManifestURL(row.HaulerRef)
	if err != nil {
		return resolvedImage{}, errors.Wrapf(err, "image %s: build manifest URL from the approved hauler_ref", row.Original)
	}
	tag := path[strings.LastIndex(path, "/")+1:]
	served, err := fetchRegistryManifest(ctx, client, registryAddress, row.HaulerRef, tag)
	if err != nil {
		return resolvedImage{}, errors.Wrapf(err, "image %s", row.Original)
	}
	resolved := resolvedImage{Row: row, Served: served, Platform: served}
	if !served.Manifest.IsIndex {
		return resolved, nil
	}
	platform, err := fetchRegistryManifest(ctx, client, registryAddress, row.HaulerRef, served.Manifest.PlatformDigest)
	if err != nil {
		cause := err
		if absentInRegistry(err) {
			// The index itself answers 200, so the error must not be wrapped into
			// "manifest not found" alone: this is the F06 case of an index whose
			// platform object is missing from the package.
			cause = fmt.Errorf("the served index declares linux/amd64 %s but that sub-manifest is absent from the registry", served.Manifest.PlatformDigest)
		}
		return resolvedImage{}, errors.Wrapf(cause, "image %s: an index whose platform object cannot be read is a broken artifact", row.Original)
	}
	if platform.Digest != served.Manifest.PlatformDigest {
		return resolvedImage{}, fmt.Errorf("image %s: the index's linux/amd64 entry %s was served as %s; the registry content is inconsistent",
			row.Original, served.Manifest.PlatformDigest, platform.Digest)
	}
	if platform.Manifest.IsIndex {
		return resolvedImage{}, fmt.Errorf("image %s: the linux/amd64 entry of the served index is itself an index; this installer ships single-platform content", row.Original)
	}
	if served.Manifest.PlatformSizeKnown && served.Manifest.PlatformSize != int64(len(platform.Body)) {
		return resolvedImage{}, fmt.Errorf("image %s: the served index declares its linux/amd64 manifest as %d bytes but the registry served %d",
			row.Original, served.Manifest.PlatformSize, len(platform.Body))
	}
	resolved.Platform = platform
	return resolved, nil
}

// blobDigests lists the config and layer digests of the object that carries this
// platform's content.
func (r resolvedImage) blobDigests() []string {
	digests := append([]string(nil), r.Platform.Manifest.LayerDigests...)
	if r.Platform.Manifest.ConfigDigest != "" {
		digests = append([]string{r.Platform.Manifest.ConfigDigest}, digests...)
	}
	return digests
}

// verifyRegistryBlobs HEADs every blob the platform manifest references. A
// manifest whose blobs are absent is a broken package, whatever its digest.
func verifyRegistryBlobs(ctx context.Context, client *http.Client, registryAddress string, image resolvedImage) error {
	repoPath, err := RepositoryPath(image.Row.HaulerRef)
	if err != nil {
		return err
	}
	for _, blob := range image.blobDigests() {
		request, err := http.NewRequestWithContext(ctx, http.MethodHead,
			fmt.Sprintf("http://%s%s/blobs/%s", registryAddress, repoPath, url.PathEscape(blob)), nil)
		if err != nil {
			return errors.Wrapf(err, "build blob request for %s", image.Row.Original)
		}
		response, err := client.Do(request)
		if err != nil {
			return errors.Wrapf(err, "request blob %s of image %s", blob, image.Row.Original)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return errors.Wrapf(&registryHTTPError{
				StatusCode: response.StatusCode,
				Kind:       "blob",
				Reference:  blob,
				HaulerRef:  image.Row.HaulerRef,
			}, "image %s: blob referenced by the served platform manifest %s", image.Row.Original, shortDigest(image.Platform.Digest))
		}
	}
	return nil
}

func shortDigest(digest string) string {
	if value := strings.TrimPrefix(digest, "sha256:"); len(value) > 12 {
		return value[:12]
	}
	return strings.TrimPrefix(digest, "sha256:")
}

// VerifyImageTablePin compares the digests of what the registry served with the
// digest images.tsv pins for that row. The pin names the packaged object by its
// own digest, and the object may be reached either as the tag's manifest or as
// the platform manifest an index points at — both are the same bytes on the
// node. Anything else means the artifact does not carry what it claims.
func VerifyImageTablePin(row Image, servedDigest, servedPlatformDigest string) error {
	pin := strings.TrimSpace(row.Digest)
	if pin == "" {
		return fmt.Errorf("image %s: images.tsv carries no actual_digest for this row, so its content cannot be approved", row.Original)
	}
	if !isSHA256Digest(pin) {
		return fmt.Errorf("image %s: images.tsv digest %q is not a sha256 digest", row.Original, pin)
	}
	if servedPlatformDigest == pin || servedDigest == pin {
		return nil
	}
	return fmt.Errorf("image %s: the registry serves %s (platform %s) but images.tsv pins %s; the packaged content does not match the pinned transport identity",
		row.Original, servedDigest, servedPlatformDigest, pin)
}

// registryContentChecker is the one content gate for an images.tsv row. Packaging,
// verification of an existing archive, the first install and a component addition
// all reach it through Verify, so no path can pick a weaker rule than another.
//
// The pin in images.tsv stays the root of trust: the packaged object is accepted
// either because its bytes ARE the pinned object, or because packaged evidence
// bytes that hash to the pin derive it (the linux/amd64 child of a pinned index,
// or the same content re-described between Docker schema2 and OCI media types).
// A row whose evidence cannot be found is refused, never waved through.
type registryContentChecker struct {
	client        *http.Client
	registry      string
	lock          *MaterialsLock
	evidence      ImageEvidence
	verifiedBlobs map[string]bool
	// blobBytes makes the gate fetch and hash every config/layer blob instead of
	// only asking whether the registry has it.
	blobBytes bool
}

// NewRegistryContentChecker builds the shared gate. evidenceRoots may be empty,
// which simply means no row can be approved by derivation.
func NewRegistryContentChecker(client *http.Client, registryAddress string, lock *MaterialsLock,
	evidenceRoots []string, verifyBlobBytes bool) *registryContentChecker {
	return &registryContentChecker{
		client:        client,
		registry:      registryAddress,
		lock:          lock,
		evidence:      ImageEvidence{Roots: evidenceRoots},
		verifiedBlobs: map[string]bool{},
		blobBytes:     verifyBlobBytes,
	}
}

// manifestSizes reads the blob sizes the platform manifest itself declares, so a
// downloaded blob is checked against more than its digest prefix.
func manifestSizes(body []byte) (map[string]int64, error) {
	var probe struct {
		Config struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, errors.Wrap(err, "parse the platform manifest for blob sizes")
	}
	sizes := map[string]int64{}
	if probe.Config.Digest != "" {
		sizes[probe.Config.Digest] = probe.Config.Size
	}
	for _, layer := range probe.Layers {
		sizes[layer.Digest] = layer.Size
	}
	return sizes, nil
}

// verifyBlobBytes pulls every blob the platform manifest references and hashes
// the bytes it actually received. A HEAD 200 or a listing is not proof: it says a
// server has something under that name, not that the content is what the manifest
// claims. Digests already checked in this run are not read twice.
func (c *registryContentChecker) verifyBlobBytesOf(ctx context.Context, image resolvedImage) error {
	repoPath, err := RepositoryPath(image.Row.HaulerRef)
	if err != nil {
		return err
	}
	sizes, err := manifestSizes(image.Platform.Body)
	if err != nil {
		return errors.Wrapf(err, "image %s", image.Row.Original)
	}
	for _, blob := range image.blobDigests() {
		// Every repository's own copy of an object is read: sharing a digest across
		// images never shares the proof that a given copy holds the right bytes.
		if c.verifiedBlobs[repoPath+" "+blob] {
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("http://%s%s/blobs/%s", c.registry, repoPath, url.PathEscape(blob)), nil)
		if err != nil {
			return errors.Wrapf(err, "build blob request for %s", image.Row.Original)
		}
		response, err := c.client.Do(request)
		if err != nil {
			return errors.Wrapf(err, "request blob %s of image %s", blob, image.Row.Original)
		}
		hash := sha256.New()
		received, copyErr := io.Copy(hash, response.Body)
		_ = response.Body.Close()
		if copyErr != nil {
			return errors.Wrapf(copyErr, "read blob %s of image %s", blob, image.Row.Original)
		}
		if response.StatusCode != http.StatusOK {
			return errors.Wrapf(&registryHTTPError{StatusCode: response.StatusCode, Kind: "blob",
				Reference: blob, HaulerRef: image.Row.HaulerRef}, "image %s: fetch blob of the served platform manifest %s",
				image.Row.Original, shortDigest(image.Platform.Digest))
		}
		if actual := "sha256:" + hex.EncodeToString(hash.Sum(nil)); actual != blob {
			return fmt.Errorf("image %s: blob %s was served as %d bytes that hash to %s; the packaged content is not what the manifest claims",
				image.Row.Original, shortDigest(blob), received, actual)
		}
		if declared, known := sizes[blob]; known && declared != received {
			return fmt.Errorf("image %s: blob %s is declared as %d bytes but %d were served",
				image.Row.Original, shortDigest(blob), declared, received)
		}
		c.verifiedBlobs[repoPath+" "+blob] = true
	}
	return nil
}

// Verify checks one row and names the strongest approval it actually proved.
func (c *registryContentChecker) Verify(ctx context.Context, row Image) (string, error) {
	resolved, err := resolveRegistryImage(ctx, c.client, c.registry, row)
	if err != nil {
		return "", err
	}
	if err := verifyRegistryBlobs(ctx, c.client, c.registry, resolved); err != nil {
		return "", err
	}
	if c.blobBytes {
		if err := c.verifyBlobBytesOf(ctx, resolved); err != nil {
			return "", err
		}
	}
	kind := "platform manifest"
	if resolved.Served.Manifest.IsIndex {
		kind = "multi-arch index"
	}
	pinErr := VerifyImageTablePin(row, resolved.Served.Digest, resolved.Platform.Digest)
	if pinErr != nil {
		// The packaged object is not the pinned bytes. It can still be the
		// approved content if packaged evidence derives it — and only if the
		// evidence bytes themselves hash to the pin.
		approved, resolveErr := c.evidence.resolveApproved(row)
		if resolveErr != nil {
			switch {
			case errors.Is(resolveErr, errEvidenceAbsent) && strings.Contains(resolveErr.Error(), "linux/amd64 manifest"):
				return "", errors.Wrapf(resolveErr, "image %s: %s; the pinned source manifest is packaged, but the object it selects for linux/amd64 is not",
					row.Original, pinMismatchText(row, resolved))
			case errors.Is(resolveErr, errEvidenceAbsent):
				return "", errors.Wrapf(resolveErr, "image %s: %s, and no approved source evidence is packaged to derive it",
					row.Original, pinMismatchText(row, resolved))
			default:
				return "", errors.Wrapf(resolveErr, "image %s: %s", row.Original, pinMismatchText(row, resolved))
			}
		}
		landing, landingErr := compareImageLanding(approved.PlatformBytes, resolved.Platform.Body)
		if landingErr != nil {
			return "", errors.Wrapf(landingErr, "image %s: %s; approved evidence is at %s",
				row.Original, pinMismatchText(row, resolved), approved.PlatformPath)
		}
		if landing == landingExact && approved.PinIsIndex {
			landing = landingPlatformSelected
		}
		if c.lock != nil {
			if entry, locked := c.lock.ImageByOriginal(row.Original); locked {
				if err := checkLockAgainstApproved(row, approved, *entry, resolved.Served.Manifest.IsIndex); err != nil {
					return "", err
				}
				blobProof := "declared"
				if c.blobBytes {
					blobProof = "byte-verified"
				}
				return fmt.Sprintf("approved materials lock, images.tsv pin and source evidence: landing %s from %s, packaged %s %s with %d blob(s) %s (rule %s)",
					landing, approved.PlatformPath, kind, shortDigest(resolved.Platform.Digest),
					len(resolved.blobDigests()), blobProof, imageLandingRuleVersion), nil
			}
		}
		return fmt.Sprintf("images.tsv pin and source evidence: landing %s from %s, packaged %s %s with %d blob(s) checked (no lock entry names this image)",
			landing, approved.PlatformPath, kind, shortDigest(resolved.Platform.Digest),
			len(resolved.blobDigests())), nil
	}
	if c.lock == nil {
		return fmt.Sprintf("images.tsv pin (no materials lock available): served %s %s, platform %s",
			kind, shortDigest(resolved.Served.Digest), resolved.Platform.Digest), nil
	}
	entry, locked := c.lock.ImageByOriginal(row.Original)
	if !locked {
		return fmt.Sprintf("images.tsv pin only (no lock entry, so no approved lock identity exists for it): served %s %s, platform %s",
			kind, shortDigest(resolved.Served.Digest), resolved.Platform.Digest), nil
	}
	if err := VerifyServedManifest(*entry, resolved.Served.Digest, resolved.Served.Manifest); err != nil {
		return "", err
	}
	if entry.PlatformDigest != "" && resolved.Platform.Digest != entry.PlatformDigest {
		return "", fmt.Errorf("image %q: the resolved %s digest %s does not match the approved platform digest %s",
			row.Original, kind, resolved.Platform.Digest, entry.PlatformDigest)
	}
	return fmt.Sprintf("approved materials lock and images.tsv pin: served %s %s matches, platform %s with %d blob(s) present",
		kind, shortDigest(resolved.Served.Digest), resolved.Platform.Digest, len(resolved.blobDigests())), nil
}

// pinMismatchText rebuilds the refusal a row without derivation deserves, naming
// both digests so the operator can see what the package actually carries.
func pinMismatchText(row Image, resolved resolvedImage) string {
	return fmt.Sprintf("the registry serves %s (platform %s) but images.tsv pins %s; the packaged content does not match the pinned transport identity",
		resolved.Served.Digest, resolved.Platform.Digest, row.Digest)
}

// checkLockAgainstApproved keeps the lock's strict meaning on the APPROVED
// identity while the packaged object may be a derived landing of it: the source
// and platform digests the lock names must be the ones the evidence shows.
func checkLockAgainstApproved(row Image, approved approvedObject, entry LockedImage, servedIsIndex bool) error {
	// These two rules used to live only on the byte-exact branch, which meant a
	// blocked or injection-shaped image became acceptable exactly when the package
	// was a derived object - the case the new rule exists to admit.
	if entry.Disabled {
		return fmt.Errorf("image %q is disabled in the materials lock (%s); serving it is not approved",
			row.Original, entry.DisabledReason)
	}
	if entry.Injection != nil && servedIsIndex {
		return fmt.Errorf("image %q is approved as a locally injected image, but the packaged object is a multi-arch index", row.Original)
	}
	if entry.SourceManifestDigest == "" && entry.PlatformDigest == "" {
		return fmt.Errorf("image %q: the materials lock entry names neither a source nor a platform digest, so it cannot approve this derivation",
			row.Original)
	}
	if entry.PlatformDigest != "" && entry.PlatformDigest != approved.PlatformDigest {
		return fmt.Errorf("image %q: the approved evidence derives linux/amd64 platform %s but the materials lock approves %s",
			row.Original, approved.PlatformDigest, entry.PlatformDigest)
	}
	// images.tsv legitimately pins either the source object (a multi-arch index)
	// or the platform object inside it. Anything else means the table and the
	// lock no longer describe the same approved image.
	pinIsSource := entry.SourceManifestDigest != "" && entry.SourceManifestDigest == row.Digest
	pinIsPlatform := entry.PlatformDigest != "" && entry.PlatformDigest == row.Digest
	if !pinIsSource && !pinIsPlatform {
		return fmt.Errorf("image %q: images.tsv pins %s, which is neither the approved source digest %s nor the approved platform digest %s",
			row.Original, row.Digest, entry.SourceManifestDigest, entry.PlatformDigest)
	}
	return nil
}
