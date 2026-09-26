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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// r073Digest is the sha256:<hex> digest of an arbitrary byte string.
func r073Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// r073Registry is an isolated in-process registry: it serves canned manifests
// and blobs and records whether each requested blob exists.
type r073Registry struct {
	manifests map[string][]byte // "/v2/<repo>/manifests/<tag>" -> body
	blobs     map[string][]byte // digest -> the bytes the content gate must hash
	missing   string            // requests to this repo always 404 (wrong RepoTag)
}

func (r *r073Registry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.Contains(req.URL.Path, "/manifests/"):
			if req.URL.Path == r.missing {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			body, ok := r.manifests[req.URL.Path]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write(body)
		case strings.Contains(req.URL.Path, "/blobs/"):
			parts := strings.Split(req.URL.Path, "/")
			digest := parts[len(parts)-1]
			body, ok := r.blobs[digest]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func (r *r073Registry) start() *httptest.Server {
	server := httptest.NewServer(r.handler())
	return server
}

// r073 real blob bodies: the content gate hashes every blob it downloads, so a
// fixture can only use digests that actually describe the bytes it serves.
var (
	r073ConfigBody    = []byte("r073 config blob")
	r073LayerABody    = []byte("r073 layer a")
	r073LayerBBody    = []byte("r073 layer b")
	r073ConfigDigest  = r073Digest(r073ConfigBody)
	r073LayerADigest  = r073Digest(r073LayerABody)
	r073LayerBDigest  = r073Digest(r073LayerBBody)
	r073MissingDigest = r073Digest([]byte("r073 blob nobody serves"))
)

// r073BlobSet builds the blob map for the digests a manifest references.
func r073BlobSet(digests ...string) map[string][]byte {
	bodies := map[string][]byte{
		r073ConfigDigest: r073ConfigBody,
		r073LayerADigest: r073LayerABody,
		r073LayerBDigest: r073LayerBBody,
	}
	set := map[string][]byte{}
	for _, digest := range digests {
		if body, ok := bodies[digest]; ok {
			set[digest] = body
		}
	}
	return set
}

// r073PlatformManifest is a single-manifest body with the config and layers the
// registry must be able to serve as blobs.
func r073PlatformManifest(config string, layers ...string) []byte {
	sizes := map[string]int{
		r073ConfigDigest: len(r073ConfigBody),
		r073LayerADigest: len(r073LayerABody),
		r073LayerBDigest: len(r073LayerBBody),
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"schemaVersion":2,"config":{"digest":"%s","size":%d},"layers":[`, config, sizes[config])
	for index, layer := range layers {
		if index > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"digest":"%s","size":%d}`, layer, sizes[layer])
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// r073IndexBody builds an index whose only amd64 entry is the platform body, with
// the size that body really has: a descriptor with a lying or missing size is a
// broken artifact, so the fixture must be able to express both.
func r073IndexBody(platformBody []byte) []byte {
	return r073IndexBodySized(r073Digest(platformBody), int64(len(platformBody)))
}

func r073IndexBodySized(platformDigest string, size int64) []byte {
	return []byte(`{"manifests":[{"digest":"` + platformDigest +
		`","size":` + strconv.FormatInt(size, 10) +
		`,"platform":{"os":"linux","architecture":"amd64"}}]}`)
}

// r073Run runs verifyRegistryImages against the registry and returns the log so
// assertions can inspect what was (not) verified.
func r073Run(t *testing.T, registry *httptest.Server, table ImageTable, lock *MaterialsLock) string {
	t.Helper()
	var log strings.Builder
	if err := verifyRegistryImages(context.Background(), &log,
		strings.TrimPrefix(registry.URL, "http://"), table, lock, nil, t.TempDir()); err != nil {
		return "ERROR: " + err.Error() + "\n" + log.String()
	}
	return log.String()
}

// T-R07-03: an image whose index digest and platform digest are different is
// verified correctly: each served object is compared with its own approved
// digest, the two are never compared with each other, and the index is followed
// down to the platform manifest so ITS blobs are the ones checked (F06).
func TestRegistryDigestVerificationKeepsIndexAndPlatformSeparate(t *testing.T) {
	platformBody := r073PlatformManifest(r073ConfigDigest, r073LayerADigest)
	platformDigest := r073Digest(platformBody)
	indexBody := r073IndexBodySized(platformDigest, int64(len(platformBody)))
	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/example/app/manifests/v1":                indexBody,
			"/v2/example/app/manifests/" + platformDigest: platformBody,
			"/v2/example/tool/manifests/v1":               platformBody,
			"/v2/example/inject/manifests/v1":             platformBody,
		},
		blobs: r073BlobSet(r073ConfigDigest, r073LayerADigest),
	}
	server := registry.start()
	defer server.Close()

	lock := &MaterialsLock{Images: []LockedImage{
		{
			Original: "quay.io/example/app:v1", HaulerRef: "127.0.0.1:5000/example/app:v1",
			SourceManifestDigest: r073Digest(indexBody), PlatformDigest: platformDigest,
			Platform: InstallerPlatform,
		},
		{
			Original: "quay.io/example/inject:v1", HaulerRef: "127.0.0.1:5000/example/inject:v1",
			PlatformDigest: platformDigest, Platform: InstallerPlatform,
			Injection: &ImageInjection{Type: "docker-archive",
				ArchiveSHA256: "sha256:" + strings.Repeat("e", 64), Evidence: "/tmp/converted.md"},
		},
	}}
	table := ImageTable{
		"quay.io/example/app:v1":    {Original: "quay.io/example/app:v1", HaulerRef: "127.0.0.1:5000/example/app:v1", Digest: platformDigest},
		"quay.io/example/inject:v1": {Original: "quay.io/example/inject:v1", HaulerRef: "127.0.0.1:5000/example/inject:v1", Digest: platformDigest},
	}
	log := r073Run(t, server, table, lock)
	if strings.Contains(log, "ERROR:") {
		t.Fatalf("correct index and platform digests must pass:\n%s", log)
	}
	if !strings.Contains(log, "approved materials lock") ||
		!strings.Contains(log, "multi-arch index") || !strings.Contains(log, "platform manifest") {
		t.Fatalf("the log must record what kind was served and what approved it:\n%s", log)
	}
}

// T-R07-02: wrong content, a broken pin, missing blobs and an index without
// its platform object must all fail the verification.
func TestRegistryDigestVerificationRejectsWrongContent(t *testing.T) {
	platformBody := r073PlatformManifest(r073ConfigDigest, r073LayerADigest)
	approvedPlatform := r073Digest(platformBody)
	tamperedBody := []byte(strings.Replace(string(platformBody), `"schemaVersion":2`,
		`"annotations":{"x":"tampered"},"schemaVersion":2`, 1))
	tamperedDigest := r073Digest(tamperedBody)
	bothBlobs := r073BlobSet(r073ConfigDigest, r073LayerADigest)

	cases := map[string]struct {
		manifests map[string][]byte
		blobs     map[string][]byte
		digest    string
		want      string
	}{
		"served platform digest differs": {
			// The table pins the tampered bytes, so the transport check passes and
			// only the approved lock can refuse them.
			manifests: map[string][]byte{"/v2/example/app/manifests/v1": tamperedBody},
			blobs:     bothBlobs,
			digest:    tamperedDigest,
			want:      "does not match the approved platform digest",
		},
		"images.tsv pin differs": {
			manifests: map[string][]byte{"/v2/example/app/manifests/v1": tamperedBody},
			blobs:     bothBlobs,
			digest:    approvedPlatform,
			want:      "does not match the pinned transport identity",
		},
		"config blob missing": {
			manifests: map[string][]byte{"/v2/example/app/manifests/v1": platformBody},
			blobs:     r073BlobSet(r073LayerADigest),
			digest:    approvedPlatform,
			want:      "blob sha256:",
		},
		"layer blob missing": {
			manifests: map[string][]byte{"/v2/example/app/manifests/v1": platformBody},
			blobs:     r073BlobSet(r073ConfigDigest),
			digest:    approvedPlatform,
			want:      "blob sha256:",
		},
		"wrong RepoTag": {
			manifests: map[string][]byte{},
			blobs:     bothBlobs,
			digest:    approvedPlatform,
			want:      "manifest v1 of 127.0.0.1:5000/example/app:v1 returned HTTP 404",
		},
		"index without its platform object": {
			// F06: the index is served and declares the approved amd64 digest, but
			// the sub-manifest itself is absent, so the layers were never packaged.
			manifests: map[string][]byte{"/v2/example/app/manifests/v1": r073IndexBodySized(approvedPlatform, int64(len(platformBody)))},
			blobs:     bothBlobs,
			digest:    approvedPlatform,
			want:      "sub-manifest is absent",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := &r073Registry{manifests: tc.manifests, blobs: tc.blobs}
			server := registry.start()
			defer server.Close()

			lock := &MaterialsLock{Images: []LockedImage{{
				Original: "quay.io/example/app:v1", HaulerRef: "127.0.0.1:5000/example/app:v1",
				SourceManifestDigest: r073Digest([]byte("unused source index")),
				PlatformDigest:       approvedPlatform, Platform: InstallerPlatform,
			}}}
			table := ImageTable{"quay.io/example/app:v1": {
				Original: "quay.io/example/app:v1", HaulerRef: "127.0.0.1:5000/example/app:v1", Digest: tc.digest}}
			log := r073Run(t, server, table, lock)
			if !strings.Contains(log, "ERROR:") || !strings.Contains(log, tc.want) {
				t.Fatalf("%s must fail with %q; got:\n%s", name, tc.want, log)
			}
		})
	}
}

// T-R07-04: an injected image must never be served as a multi-arch index, and
// a tag the registry does not have fails the run up front.
// T-R07-04: an injected image must never be served as a multi-arch index, and
// a tag the registry does not have fails the run up front.
func TestRegistryDigestVerificationInjectionRules(t *testing.T) {
	platformBody := r073PlatformManifest(r073ConfigDigest)
	approved := r073Digest(platformBody)
	indexBody := r073IndexBodySized(approved, int64(len(platformBody)))

	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/kubercloud/kc-networking/manifests/dev":         indexBody,
			"/v2/kubercloud/kc-networking/manifests/" + approved: platformBody,
		},
		blobs: r073BlobSet(r073ConfigDigest),
	}
	server := registry.start()
	defer server.Close()

	lock := &MaterialsLock{Images: []LockedImage{{
		Original:       "docker.changqingyun.cn/kubercloud/kc-networking:dev",
		HaulerRef:      "127.0.0.1:5000/kubercloud/kc-networking:dev",
		PlatformDigest: approved, Platform: InstallerPlatform,
		Injection: &ImageInjection{Type: "docker-archive",
			ArchiveSHA256: "sha256:" + strings.Repeat("e", 64), Evidence: "/tmp/converted.md"},
	}}}
	table := ImageTable{
		"docker.changqingyun.cn/kubercloud/kc-networking:dev": {
			Original:  "docker.changqingyun.cn/kubercloud/kc-networking:dev",
			HaulerRef: "127.0.0.1:5000/kubercloud/kc-networking:dev",
			Digest:    approved,
		},
	}
	log := r073Run(t, server, table, lock)
	if !strings.Contains(log, "local injection with no approved source index digest") {
		t.Fatalf("an injection served as an index must be rejected:\n%s", log)
	}

	// The same injection served as a single platform manifest passes.
	registry2 := &r073Registry{
		manifests: map[string][]byte{
			"/v2/kubercloud/kc-networking/manifests/dev": platformBody,
		},
		blobs: r073BlobSet(r073ConfigDigest),
	}
	server2 := registry2.start()
	defer server2.Close()
	log2 := r073Run(t, server2, table, lock)
	if strings.Contains(log2, "ERROR:") {
		t.Fatalf("an injection served as a platform manifest must pass:\n%s", log2)
	}
	if !strings.Contains(log2, "images.tsv pin") {
		t.Fatalf("the injection row is approved by its pin and lock record:\n%s", log2)
	}
}

// Images without a lock entry keep the presence-only baseline: their digests
// stay unknown instead of being fabricated.
// Images without a lock entry are still content-verified: their blobs must
// exist and the served platform digest must equal the images.tsv pin. The log
// says exactly which approval was checked, so "lock-verified" can never be
// claimed for an image the lock does not list (F06 item 5).
func TestRegistryDigestVerificationUnlockedImage(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"config":{"digest":"` +
		r073ConfigDigest + `","size":` + strconv.Itoa(len(r073ConfigBody)) + `},"layers":[]}`)
	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/example/base/manifests/v1": body,
		},
		blobs: r073BlobSet(r073ConfigDigest),
	}
	server := registry.start()
	defer server.Close()

	lock := &MaterialsLock{}
	table := ImageTable{
		"docker.io/example/base:v1": {Original: "docker.io/example/base:v1",
			HaulerRef: "127.0.0.1:5000/example/base:v1", Digest: r073Digest(body)},
	}
	log := r073Run(t, server, table, lock)
	if strings.Contains(log, "ERROR:") {
		t.Fatalf("an image without a lock entry must pass on its table pin:\n%s", log)
	}
	if !strings.Contains(log, "no lock entry") || !strings.Contains(log, r073Digest(body)) {
		t.Fatalf("the log must record the pin-only approval and the served digest:\n%s", log)
	}
	if strings.Contains(log, "approved materials lock and") {
		t.Fatalf("an image with no lock entry must never be logged as lock-verified:\n%s", log)
	}

	// The same image whose served bytes differ from the pin is refused.
	tampered := append(append([]byte(nil), body...), ' ')
	registry2 := &r073Registry{
		manifests: map[string][]byte{"/v2/example/base/manifests/v1": tampered},
		blobs:     r073BlobSet(r073ConfigDigest),
	}
	server2 := registry2.start()
	defer server2.Close()
	log2 := r073Run(t, server2, table, lock)
	if !strings.Contains(log2, "ERROR:") ||
		!strings.Contains(log2, "does not match the pinned transport identity") {
		t.Fatalf("content that does not match the images.tsv pin must fail:\n%s", log2)
	}
}
