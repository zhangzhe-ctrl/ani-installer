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
	"net/http"
	"net/http/httptest"
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
	blobs     map[string]bool   // digest -> exists
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
			if !r.blobs[digest] {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func (r *r073Registry) start() *httptest.Server {
	server := httptest.NewServer(r.handler())
	return server
}

// r073PlatformManifest is a single-manifest body with the config and layers the
// registry must be able to serve as blobs.
func r073PlatformManifest(config string, layers ...string) []byte {
	var b strings.Builder
	b.WriteString(`{"schemaVersion":2,"config":{"digest":"` + config + `"},"layers":[`)
	for index, layer := range layers {
		if index > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"digest":"` + layer + `","size":1}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func r073IndexBody(platformDigest string) []byte {
	return []byte(`{"manifests":[{"digest":"` + platformDigest +
		`","platform":{"os":"linux","architecture":"amd64"}}]}`)
}

// r073Run runs verifyRegistryImages against the registry and returns the log so
// assertions can inspect what was (not) verified.
func r073Run(t *testing.T, registry *httptest.Server, table ImageTable, lock *MaterialsLock) string {
	t.Helper()
	var log strings.Builder
	if err := verifyRegistryImages(context.Background(), &log,
		strings.TrimPrefix(registry.URL, "http://"), table, lock, nil); err != nil {
		return "ERROR: " + err.Error() + "\n" + log.String()
	}
	return log.String()
}

// T-R07-03: an image whose index digest and platform digest are different is
// verified correctly: each served object is compared with its own approved
// digest, and the two are never compared with each other.
func TestRegistryDigestVerificationKeepsIndexAndPlatformSeparate(t *testing.T) {
	platformBody := r073PlatformManifest("sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("a", 64))
	indexBody := r073IndexBody(r073Digest(platformBody))
	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/example/app/manifests/v1":    indexBody,
			"/v2/example/tool/manifests/v1":   platformBody,
			"/v2/example/inject/manifests/v1": platformBody,
		},
		blobs: map[string]bool{
			"sha256:" + strings.Repeat("c", 64): true,
			"sha256:" + strings.Repeat("a", 64): true,
		},
	}
	server := registry.start()
	defer server.Close()

	lock := &MaterialsLock{Images: []LockedImage{
		{
			Original: "quay.io/example/app:v1", HaulerRef: "127.0.0.1:5000/example/app:v1",
			SourceManifestDigest: r073Digest(indexBody), PlatformDigest: r073Digest(platformBody),
			Platform: InstallerPlatform,
		},
		{
			Original: "quay.io/example/inject:v1", HaulerRef: "127.0.0.1:5000/example/inject:v1",
			PlatformDigest: r073Digest(platformBody), Platform: InstallerPlatform,
			Injection: &ImageInjection{Type: "docker-archive",
				ArchiveSHA256: "sha256:" + strings.Repeat("e", 64), Evidence: "/tmp/converted.md"},
		},
	}}
	table := ImageTable{
		"quay.io/example/app:v1":    {Original: "quay.io/example/app:v1", HaulerRef: "127.0.0.1:5000/example/app:v1"},
		"quay.io/example/inject:v1": {Original: "quay.io/example/inject:v1", HaulerRef: "127.0.0.1:5000/example/inject:v1"},
	}
	log := r073Run(t, server, table, lock)
	if strings.Contains(log, "ERROR:") {
		t.Fatalf("correct index and platform digests must pass:\n%s", log)
	}
	if !strings.Contains(log, "matches the approved lock entry") ||
		!strings.Contains(log, "multi-arch index") || !strings.Contains(log, "platform manifest") {
		t.Fatalf("the log must record what kind was served:\n%s", log)
	}
}

// T-R07-02: wrong content and missing blobs must fail the verification.
func TestRegistryDigestVerificationRejectsWrongContent(t *testing.T) {
	platformBody := r073PlatformManifest("sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("a", 64))
	approvedPlatform := r073Digest(platformBody)

	// Same shape and same referenced blobs as the approved manifest, different
	// bytes: only the served digest differs, which is exactly what the digest
	// comparison must catch.
	tamperedBody := append([]byte(nil), platformBody...)
	tamperedBody = []byte(strings.Replace(string(platformBody), `"schemaVersion":2`,
		`"annotations":{"x":"tampered"},"schemaVersion":2`, 1))

	cases := map[string]func(t *testing.T) (string, string, string){
		"served platform digest differs": func(*testing.T) (string, string, string) {
			return "quay.io/example/app:v1", "127.0.0.1:5000/example/app:v1",
				string(tamperedBody) // same shape, different bytes -> different digest
		},
		"config blob missing": func(*testing.T) (string, string, string) {
			return "quay.io/example/app:v1", "127.0.0.1:5000/example/app:v1", string(platformBody)
		},
		"layer blob missing": func(*testing.T) (string, string, string) {
			return "quay.io/example/app:v1", "127.0.0.1:5000/example/app:v1", string(platformBody)
		},
		"wrong RepoTag": func(*testing.T) (string, string, string) {
			return "quay.io/example/app:v1", "127.0.0.1:5000/example/app:v1", string(platformBody)
		},
	}
	missingBlob := map[string]string{
		"config blob missing": "sha256:" + strings.Repeat("c", 64),
		"layer blob missing":  "sha256:" + strings.Repeat("a", 64),
	}
	wantSubstrings := map[string]string{
		"served platform digest differs": "does not match the approved platform digest",
		"config blob missing":            "returned HTTP 404",
		"layer blob missing":             "returned HTTP 404",
		"wrong RepoTag":                  "manifest returned HTTP 404",
	}
	for name := range cases {
		t.Run(name, func(t *testing.T) {
			original, haulerRef, body := cases[name](t)
			registry := &r073Registry{
				manifests: map[string][]byte{
					"/v2/example/app/manifests/v1": []byte(body),
				},
				blobs: map[string]bool{
					"sha256:" + strings.Repeat("c", 64): missingBlob[name] != "sha256:"+strings.Repeat("c", 64),
					"sha256:" + strings.Repeat("a", 64): missingBlob[name] != "sha256:"+strings.Repeat("a", 64),
				},
			}
			if name == "layer blob missing" {
				registry.blobs["sha256:"+strings.Repeat("a", 64)] = false
			}
			if name == "config blob missing" {
				registry.blobs["sha256:"+strings.Repeat("c", 64)] = false
			}
			if name == "wrong RepoTag" {
				registry.missing = "/v2/example/app/manifests/v1"
			}
			server := registry.start()
			defer server.Close()

			lock := &MaterialsLock{Images: []LockedImage{{
				Original: original, HaulerRef: haulerRef,
				SourceManifestDigest: r073Digest([]byte("unused source index")),
				PlatformDigest:       approvedPlatform, Platform: InstallerPlatform,
			}}}
			table := ImageTable{original: {Original: original, HaulerRef: haulerRef}}
			log := r073Run(t, server, table, lock)
			if !strings.Contains(log, "ERROR:") || !strings.Contains(log, wantSubstrings[name]) {
				t.Fatalf("%s must fail with %q; got:\n%s", name, wantSubstrings[name], log)
			}
		})
	}
}

// T-R07-04: an injected image must never be served as a multi-arch index, and
// a tag the registry does not have fails the run up front.
func TestRegistryDigestVerificationInjectionRules(t *testing.T) {
	platformBody := r073PlatformManifest("sha256:" + strings.Repeat("c", 64))
	approved := r073Digest(platformBody)
	indexBody := r073IndexBody(approved)

	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/kubercloud/kc-networking/manifests/dev": indexBody,
		},
		blobs: map[string]bool{"sha256:" + strings.Repeat("c", 64): true},
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
		blobs: map[string]bool{"sha256:" + strings.Repeat("c", 64): true},
	}
	server2 := registry2.start()
	defer server2.Close()
	table2 := ImageTable{
		"docker.changqingyun.cn/kubercloud/kc-networking:dev": {
			Original:  "docker.changqingyun.cn/kubercloud/kc-networking:dev",
			HaulerRef: "127.0.0.1:5000/kubercloud/kc-networking:dev",
		},
	}
	log2 := r073Run(t, server2, table2, lock)
	if strings.Contains(log2, "ERROR:") {
		t.Fatalf("an injection served as a platform manifest must pass:\n%s", log2)
	}
}

// Images without a lock entry keep the presence-only baseline: their digests
// stay unknown instead of being fabricated.
func TestRegistryDigestVerificationUnlockedImage(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:` +
		strings.Repeat("d", 64) + `"},"layers":[]}`)
	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/example/base/manifests/v1": body,
		},
		// The blob existence check applies to unlocked images as well: a served
		// manifest whose blobs are missing is broken regardless of lock status.
		blobs: map[string]bool{"sha256:" + strings.Repeat("d", 64): true},
	}
	server := registry.start()
	defer server.Close()

	lock := &MaterialsLock{}
	table := ImageTable{
		"docker.io/example/base:v1": {Original: "docker.io/example/base:v1",
			HaulerRef: "127.0.0.1:5000/example/base:v1"},
	}
	log := r073Run(t, server, table, lock)
	if strings.Contains(log, "ERROR:") {
		t.Fatalf("an image without a lock entry must keep the presence-only baseline:\n%s", log)
	}
	if !strings.Contains(log, "no lock entry") || !strings.Contains(log, r073Digest(body)) {
		t.Fatalf("the log must record the served digest as not lock-verified:\n%s", log)
	}
}
