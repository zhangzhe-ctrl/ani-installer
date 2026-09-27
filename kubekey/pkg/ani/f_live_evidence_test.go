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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The live round proved a digest comparison alone cannot decide what a packaged
// image is: a pin may name a multi-arch index, and the packaging transport may
// re-describe the same content under OCI media types. These cases fix what the
// shared gate accepts — only by proof from bytes that hash to the original pin —
// and every way a package could otherwise look correct while carrying something
// else.

const (
	dockerManifestMedia = "application/vnd.docker.distribution.manifest.v2+json"
	ociManifestMedia    = "application/vnd.oci.image.manifest.v1+json"
	ociConfigMedia      = "application/vnd.oci.image.config.v1+json"
	dockerConfigMedia   = "application/vnd.docker.container.image.v1+json"
	ociLayerMedia       = "application/vnd.oci.image.layer.v1.tar+gzip"
	dockerLayerMedia    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

// liveStore is an in-process registry with content-addressed objects, so every
// case below states exactly which bytes exist and which are corrupted.
type liveStore struct {
	objects map[string][]byte            // digest -> manifest bytes
	tags    map[string]string            // "repo:tag" -> digest
	blobs   map[string][]byte            // digest -> blob bytes
	copies  map[string]map[string][]byte // repo -> digest -> the bytes that repository answers with
	hidden  map[string]bool
}

func newLiveStore() *liveStore {
	return &liveStore{objects: map[string][]byte{}, tags: map[string]string{},
		blobs: map[string][]byte{}, copies: map[string]map[string][]byte{}, hidden: map[string]bool{}}
}

func (s *liveStore) addManifest(body []byte) string {
	digest := hexDigestOf(body)
	s.objects[digest] = body
	return digest
}

func (s *liveStore) addBlob(body []byte) string {
	digest := hexDigestOf(body)
	s.blobs[digest] = body
	return digest
}

func (s *liveStore) corruptBlob(repo, digest string, body []byte) {
	if s.copies[repo] == nil {
		s.copies[repo] = map[string][]byte{}
	}
	s.copies[repo][digest] = body
}

func (s *liveStore) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v2/")
		if marker := strings.Index(path, "/manifests/"); marker >= 0 {
			repo, reference := path[:marker], path[marker+len("/manifests/"):]
			body := s.objects[reference]
			if !strings.HasPrefix(reference, "sha256:") {
				digest, ok := s.tags[repo+":"+reference]
				if !ok {
					http.Error(w, "unknown", http.StatusNotFound)
					return
				}
				body = s.objects[digest]
			}
			if body == nil {
				http.Error(w, "unknown", http.StatusNotFound)
				return
			}
			var probe struct {
				MediaType string `json:"mediaType"`
			}
			_ = json.Unmarshal(body, &probe)
			contentType := probe.MediaType
			if contentType == "" {
				contentType = ociManifestMedia
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Docker-Content-Digest", hexDigestOf(body))
			_, _ = w.Write(body)
			return
		}
		if marker := strings.Index(path, "/blobs/"); marker >= 0 {
			repo, digest := path[:marker], path[marker+len("/blobs/"):]
			if s.hidden[digest] {
				http.Error(w, "unknown", http.StatusNotFound)
				return
			}
			if body, ok := s.copies[repo][digest]; ok {
				_, _ = w.Write(body)
				return
			}
			body, ok := s.blobs[digest]
			if !ok {
				http.Error(w, "unknown", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func address(server *httptest.Server) string { return strings.TrimPrefix(server.URL, "http://") }

type liveDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// liveManifest writes a real single-platform manifest with the given media types.
func liveManifest(manifestMedia, configMedia, layerMedia, config string, configSize int64, layers ...liveDescriptor) []byte {
	document := struct {
		SchemaVersion int              `json:"schemaVersion"`
		MediaType     string           `json:"mediaType,omitempty"`
		Config        liveDescriptor   `json:"config"`
		Layers        []liveDescriptor `json:"layers"`
	}{2, manifestMedia, liveDescriptor{configMedia, config, configSize}, layers}
	for index := range document.Layers {
		if document.Layers[index].MediaType == "" {
			document.Layers[index].MediaType = layerMedia
		}
	}
	body, _ := json.Marshal(document)
	return body
}

func liveIndex(entries ...map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.index.v1+json", "manifests": entries})
	return body
}

func amd64Entry(digest string, size int64) map[string]any {
	return map[string]any{"mediaType": ociManifestMedia, "digest": digest, "size": size,
		"platform": map[string]any{"os": "linux", "architecture": "amd64"}}
}

// liveEvidence publishes the given bytes under their own digests, as a package's
// images/evidence directory does.
func liveEvidence(t *testing.T, bodies ...[]byte) string {
	t.Helper()
	dir := t.TempDir()
	for _, body := range bodies {
		name := strings.TrimPrefix(hexDigestOf(body), "sha256:") + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func row(original, haulerRef, pin string) Image {
	return Image{Original: original, HaulerRef: haulerRef, Digest: pin, Use: "live round"}
}

func verify(t *testing.T, server *httptest.Server, image Image, lock *MaterialsLock, evidence []string) (string, error) {
	t.Helper()
	checker := NewRegistryContentChecker(&http.Client{Timeout: 15 * time.Second}, address(server), lock, evidence, true)
	return checker.Verify(context.Background(), image)
}

func mustApprove(t *testing.T, want string, approval string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected an approval naming %q, got error: %v", want, err)
	}
	if !strings.Contains(approval, want) {
		t.Fatalf("the approval must name %q, got:\n%s", want, approval)
	}
}

func mustRefuse(t *testing.T, want string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("this content must be refused, and the refusal must name %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal must name %q, got: %v", want, err)
	}
}

// T-LIVE-03: the landings that are provably the approved content, and every way a
// packaged object can differ from it.
func TestLiveGateLandingRules(t *testing.T) {
	// A shared body used by the positive cases.
	configBytes := []byte("the image config bytes")
	layerOne := []byte("first layer bytes")
	layerTwo := []byte("second layer bytes")

	landedOCI := func(store *liveStore, config, first, second string) string {
		return store.addManifest(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia, config,
			int64(len(configBytes)),
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
	}
	approvedDocker := func(store *liveStore, config, first, second string) string {
		return store.addManifest(liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia, config,
			int64(len(configBytes)),
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
	}

	t.Run("byte-exact: the packaged object is the pinned bytes", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := store.addManifest(liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia, config,
			int64(len(configBytes)),
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
		store.tags["example/app:v1"] = pin
		server := store.server()
		defer server.Close()
		approval, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin), nil, nil)
		mustApprove(t, "images.tsv pin (no materials lock available)", approval, err)
	})

	t.Run("platform-selected: the pin is the source index", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		child := approvedDocker(store, config, first, second)
		index := store.addManifest(liveIndex(amd64Entry(child, int64(len(store.objects[child])))))
		// The package ships only linux/amd64: the tag serves the index's amd64
		// object while the pin stays the index the evidence reproduces.
		store.tags["example/indexed:v1"] = child
		server := store.server()
		defer server.Close()
		evidence := liveEvidence(t, store.objects[index], store.objects[child])
		approval, err := verify(t, server,
			row("docker.io/example/indexed:v1", "127.0.0.1:5000/example/indexed:v1", index), nil, []string{evidence})
		mustApprove(t, "landing platform-selected", approval, err)
		if strings.Contains(approval, "converted") {
			t.Fatalf("selecting a platform is not a conversion:\n%s", approval)
		}
	})

	t.Run("converted: docker schema2 landed as the same OCI content", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		landedOCI(store, config, first, second)
		store.tags["example/app:v1"] = landedOCI(store, config, first, second)
		server := store.server()
		defer server.Close()
		approval, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustApprove(t, "landing converted", approval, err)
	})

	t.Run("forged evidence cannot approve anything", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		pin := approvedDocker(store, config, store.addBlob(layerOne), store.addBlob(layerTwo))
		store.tags["example/app:v1"] = landedOCI(store, config, store.addBlob(layerOne), store.addBlob(layerTwo))
		server := store.server()
		defer server.Close()
		forged := t.TempDir()
		if err := os.WriteFile(filepath.Join(forged, strings.TrimPrefix(pin, "sha256:")+".json"),
			[]byte(`{"schemaVersion":2,"forged":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin), nil, []string{forged})
		mustRefuse(t, "but its bytes hash to", err) // a file named after a digest it is not
	})

	t.Run("a different config is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		other := store.addBlob([]byte("xhe image config bytes")) // same length, different content
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = store.addManifest(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia,
			other, int64(len("xhe image config bytes")),
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "config digest changed", err)
	})

	t.Run("a swapped layer is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		extra := store.addBlob([]byte("a third layer"))
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = store.addManifest(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia,
			config, int64(len(configBytes)),
			liveDescriptor{Digest: extra, Size: int64(len("a third layer"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "layer 1 digest changed", err)
	})

	t.Run("reordered layers are refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = store.addManifest(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia,
			config, int64(len(configBytes)),
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))},
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))}))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "layer 1 digest changed", err)
	})

	t.Run("an added layer is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = store.addManifest(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia,
			config, int64(len(configBytes)),
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))},
			liveDescriptor{Digest: store.addBlob([]byte("inserted")), Size: 8}))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "layer count changed from 2 to 3", err)
	})

	t.Run("a lying blob size is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = store.addManifest(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia,
			config, int64(len(configBytes)),
			liveDescriptor{Digest: first, Size: 9999},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin), nil, nil)
		mustRefuse(t, "is declared as 9999 bytes", err)
	})

	t.Run("a missing blob is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = pin
		store.hidden[second] = true
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin), nil, nil)
		mustRefuse(t, "blob sha256:", err)
	})

	t.Run("a corrupted copy of a shared blob in one repository is still caught", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		shared := store.addBlob(layerOne)
		second := store.addBlob(layerTwo)
		for _, repo := range []string{"example/one", "example/two"} {
			digest := store.addManifest(liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia,
				config, int64(len(configBytes)),
				liveDescriptor{Digest: shared, Size: int64(len("first layer bytes"))},
				liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
			store.tags[repo+":v1"] = digest
		}
		store.corruptBlob("example/two", shared, []byte("corrupted in this repository"))
		server := store.server()
		defer server.Close()
		if _, err := verify(t, server, row("docker.io/example/one:v1", "127.0.0.1:5000/example/one:v1",
			store.tags["example/one:v1"]), nil, nil); err != nil {
			t.Fatalf("the intact repository must pass: %v", err)
		}
		_, err := verify(t, server, row("docker.io/example/two:v1", "127.0.0.1:5000/example/two:v1",
			store.tags["example/two:v1"]), nil, nil)
		mustRefuse(t, "that hash to", err)
	})

	t.Run("annotations the approved object never had are refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		var document map[string]any
		if err := json.Unmarshal(store.objects[landedOCI(store, config, first, second)], &document); err != nil {
			t.Fatal(err)
		}
		document["annotations"] = map[string]string{"com.docker.official-images.bashbrew.arch": "amd64"}
		mutated, _ := json.Marshal(document)
		store.tags["example/app:v1"] = store.addManifest(mutated)
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "adds top-level field annotations", err)
	})

	t.Run("an unapproved media type is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		landed := string(landedOCI(store, config, first, second))
		_ = landed
		store.tags["example/app:v1"] = store.addManifest([]byte(strings.Replace(string(
			liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia, config, int64(len(configBytes)),
				liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
				liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))})),
			ociLayerMedia, "application/vnd.oci.image.layer.v1.tar+zstd", -1)))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "no approved landing", err)
	})

	t.Run("duplicate JSON keys cannot hide a second layer set", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		good := string(liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia, config, int64(len(configBytes)),
			liveDescriptor{Digest: first, Size: int64(len("first layer bytes"))},
			liveDescriptor{Digest: second, Size: int64(len("second layer bytes"))}))
		attacker := liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia, config, int64(len(configBytes)),
			liveDescriptor{Digest: store.addBlob([]byte("attacker")), Size: 8})
		var attackerDoc map[string]any
		if err := json.Unmarshal(attacker, &attackerDoc); err != nil {
			t.Fatal(err)
		}
		duplicated := strings.TrimSuffix(good, "}") + `,"layers":` +
			string(mustMarshalLayers(t, attackerDoc["layers"])) + "}"
		store.tags["example/app:v1"] = store.addManifest([]byte(duplicated))
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			nil, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, `repeats the key "layers"`, err)
	})

	t.Run("a disabled lock entry cannot be derived around", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		first, second := store.addBlob(layerOne), store.addBlob(layerTwo)
		pin := approvedDocker(store, config, first, second)
		store.tags["example/app:v1"] = landedOCI(store, config, first, second)
		server := store.server()
		defer server.Close()
		lock := &MaterialsLock{Images: []LockedImage{{Original: "docker.io/example/app:v1",
			HaulerRef: "127.0.0.1:5000/example/app:v1", PlatformDigest: pin,
			Disabled: true, DisabledReason: "excluded from this batch"}}}
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin),
			lock, []string{liveEvidence(t, store.objects[pin])})
		mustRefuse(t, "is disabled in the materials lock", err)
	})

	t.Run("an index with two amd64 entries is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		child := store.addManifest(liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia,
			config, int64(len(configBytes))))
		plain := amd64Entry(child, int64(len(store.objects[child])))
		variant := amd64Entry(child, int64(len(store.objects[child])))
		variant["platform"].(map[string]any)["variant"] = "v1"
		index := store.addManifest(liveIndex(plain, variant))
		store.tags["example/app:v1"] = index
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", index), nil,
			[]string{liveEvidence(t, store.objects[index], store.objects[child])})
		mustRefuse(t, "2 linux/amd64 entries", err)
	})

	t.Run("an approved index that lies about its child is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		child := store.addManifest(liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia,
			config, int64(len(configBytes))))
		index := store.addManifest(liveIndex(amd64Entry(child, int64(len(store.objects[child]))+1000)))
		store.tags["example/app:v1"] = child
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", index), nil,
			[]string{liveEvidence(t, store.objects[index], store.objects[child])})
		mustRefuse(t, "declares its amd64 manifest as", err)
	})

	t.Run("an index without linux/amd64 is refused", func(t *testing.T) {
		store := newLiveStore()
		config := store.addBlob(configBytes)
		child := store.addManifest(liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia,
			config, int64(len(configBytes))))
		entry := amd64Entry(child, int64(len(store.objects[child])))
		entry["platform"] = map[string]any{"os": "linux", "architecture": "arm64"}
		index := store.addManifest(liveIndex(entry))
		store.tags["example/app:v1"] = index
		server := store.server()
		defer server.Close()
		_, err := verify(t, server, row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", index), nil, nil)
		mustRefuse(t, "no linux/amd64 entry", err)
	})
}

func mustMarshalLayers(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// T-LIVE-04: landing produces a store of approved bytes, and refuses inputs that
// would leave anything unapproved behind.
func TestLandingBuildsAnApprovedStore(t *testing.T) {
	store := newLiveStore()
	config := store.addBlob([]byte("config bytes"))
	layer := store.addBlob([]byte("layer bytes"))
	childBody := liveManifest(dockerManifestMedia, dockerConfigMedia, dockerLayerMedia, config,
		int64(len("config bytes")), liveDescriptor{Digest: layer, Size: int64(len("layer bytes"))})
	child := store.addManifest(childBody)
	indexBody := liveIndex(amd64Entry(child, int64(len(childBody))))
	pin := store.addManifest(indexBody)
	store.tags["example/indexed:v1"] = pin
	evidence := liveEvidence(t, indexBody, childBody)

	dir := t.TempDir()
	sources := filepath.Join(dir, "source-store")
	if err := os.MkdirAll(filepath.Join(sources, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	for digest, body := range store.blobs {
		if err := os.WriteFile(filepath.Join(sources, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sources, "index.json"),
		[]byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"`+
			pin+`","size":`+strconv.Itoa(len(indexBody))+`,"annotations":{"org.opencontainers.image.ref.name":"example/indexed:v1"}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	tableFile := filepath.Join(dir, "images.tsv")
	if err := os.WriteFile(tableFile, []byte("original_ref\thauler_ref\tactual_digest\tuse_location\n"+
		"docker.io/example/indexed:v1\t127.0.0.1:5000/example/indexed:v1\t"+pin+"\tlive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outStore := filepath.Join(dir, "landed")
	var log bytes.Buffer
	if err := RunMaterialsLandApprovedImages(MaterialsLandInput{
		ImagesTSVPath: tableFile, EvidenceDirs: []string{evidence},
		SourceStores: []string{sources}, OutStore: outStore,
	}, &log); err != nil {
		t.Fatalf("a complete approved input must land: %v\n%s", err, log.String())
	}

	index, err := readStoreIndex(outStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest != child {
		t.Fatalf("the tag must point at the pinned index's amd64 object, got %+v", index.Manifests)
	}
	if index.Manifests[0].Annotations[ociRefNameAnnotation] != "example/indexed:v1" {
		t.Fatalf("the landed tag is %q", index.Manifests[0].Annotations[ociRefNameAnnotation])
	}
	if index.Manifests[0].Size != int64(len(childBody)) {
		t.Fatalf("the landed descriptor size is %d, want %d", index.Manifests[0].Size, len(childBody))
	}
	landed, err := filepath.Glob(filepath.Join(outStore, "blobs", "sha256", "*"))
	if err != nil {
		t.Fatal(err)
	}
	// The landed store holds the amd64 manifest plus exactly the blobs it
	// references. The pinned index stays in the evidence, never in the store.
	if want := 1 + len(store.blobs); len(landed) != want {
		t.Fatalf("the landed store holds %d objects, want the platform manifest plus %d referenced blobs", len(landed), len(store.blobs))
	}
	for _, path := range landed {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if hexDigestOf(body) != "sha256:"+filepath.Base(path) {
			t.Fatalf("%s does not hash to its own name", path)
		}
	}

	// Offline proof: serve the landed bytes alone and re-run the same gate.
	restored := newLiveStore()
	for _, entry := range index.Manifests {
		body, err := os.ReadFile(filepath.Join(outStore, "blobs", "sha256", strings.TrimPrefix(entry.Digest, "sha256:")))
		if err != nil {
			t.Fatal(err)
		}
		digest := restored.addManifest(body)
		if digest != entry.Digest {
			t.Fatal("the landed bytes do not hash to the tag")
		}
		restored.tags[entry.Annotations[ociRefNameAnnotation]] = digest // the amd64 object, as landed
		// Every blob the landed manifest names must be in the landed store too:
		// that is what makes it installable offline.
		var landedManifest struct {
			Config liveDescriptor   `json:"config"`
			Layers []liveDescriptor `json:"layers"`
		}
		if err := json.Unmarshal(body, &landedManifest); err != nil {
			t.Fatal(err)
		}
		for _, referenced := range append([]liveDescriptor{landedManifest.Config}, landedManifest.Layers...) {
			blob, err := os.ReadFile(filepath.Join(outStore, "blobs", "sha256", strings.TrimPrefix(referenced.Digest, "sha256:")))
			if err != nil {
				t.Fatalf("the landed store is missing the blob %s: %v", referenced.Digest, err)
			}
			if restored.addBlob(blob) != referenced.Digest {
				t.Fatalf("the landed blob %s does not hash to its name", referenced.Digest)
			}
			if referenced.Size != int64(len(blob)) {
				t.Fatalf("the landed manifest declares %s as %d bytes, the store holds %d",
					referenced.Digest, referenced.Size, len(blob))
			}
		}
	}
	// The pinned index must travel with the package as evidence, not as a tag:
	// publishing an index whose other children are absent would pretend to be a
	// multi-arch image.
	indexCopy, err := os.ReadFile(filepath.Join(evidence, strings.TrimPrefix(pin, "sha256:")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	restored.addManifest(indexCopy)
	server := restored.server()
	defer server.Close()
	approval, err := verify(t, server,
		row("docker.io/example/indexed:v1", "127.0.0.1:5000/example/indexed:v1", pin), nil, []string{evidence})
	mustApprove(t, "landing platform-selected", approval, err)

	// Refusals.
	if err := os.WriteFile(filepath.Join(outStore, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = RunMaterialsLandApprovedImages(MaterialsLandInput{ImagesTSVPath: tableFile,
		EvidenceDirs: []string{evidence}, SourceStores: []string{sources}, OutStore: outStore}, &bytes.Buffer{})
	mustRefuse(t, "refusing to write into an existing store", err)

	err = RunMaterialsLandApprovedImages(MaterialsLandInput{ImagesTSVPath: tableFile,
		EvidenceDirs: []string{evidence}, SourceStores: []string{sources},
		OutStore: filepath.Join(sources, "landed")}, &bytes.Buffer{})
	mustRefuse(t, "read-only input", err)

	contradiction := filepath.Join(dir, "contradiction")
	if err := copyTree(sources, contradiction); err != nil {
		t.Fatal(err)
	}
	other := liveManifest(ociManifestMedia, ociConfigMedia, ociLayerMedia, config, int64(len("config bytes")))
	if err := os.WriteFile(filepath.Join(contradiction, "index.json"),
		[]byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+
			hexDigestOf(other)+`","size":`+strconv.Itoa(len(other))+`,"annotations":{"org.opencontainers.image.ref.name":"example/indexed:v1"}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	err = RunMaterialsLandApprovedImages(MaterialsLandInput{ImagesTSVPath: tableFile,
		EvidenceDirs: []string{evidence}, SourceStores: []string{contradiction},
		OutStore: filepath.Join(dir, "landed-2")}, &bytes.Buffer{})
	mustRefuse(t, "does not match the pinned transport identity", err)
}

// T-LIVE-05: the table is validated before it is trusted, and the lock cannot
// disagree with it in silence.
func TestTableAndLockMustNotDisagree(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	for _, haulerRef := range []string{"127.0.0.1:5000/example/app", "127.0.0.1:5000/example/app:",
		"127.0.0.1:5000/example/app:a:b"} {
		text := "original_ref\thauler_ref\tactual_digest\tuse_location\n" +
			"docker.io/example/app:v1\t" + haulerRef + "\t" + pin + "\tlive\n"
		if _, err := LoadImageTable(strings.Split(text, "\n")); err == nil {
			t.Fatalf("hauler_ref %q must be refused as not one repository:tag", haulerRef)
		}
	}
	duplicated := "original_ref\thauler_ref\tactual_digest\tuse_location\n" +
		"docker.io/example/one:v1\t127.0.0.1:5000/example/app:v1\t" + pin + "\tlive\n" +
		"docker.io/example/two:v1\t127.0.0.1:5000/example/app:v1\t" + pin + "\tlive\n"
	_, err := LoadImageTable(strings.Split(duplicated, "\n"))
	mustRefuse(t, "duplicate hauler_ref", err)

	table := ImageTable{"docker.io/typo/app:v1": row("docker.io/typo/app:v1", "127.0.0.1:5000/example/app:v1", pin)}
	lock := &MaterialsLock{Images: []LockedImage{{Original: "docker.io/example/app:v1",
		HaulerRef: "127.0.0.1:5000/example/app:v1", SourceManifestDigest: pin}}}
	mustRefuse(t, "disagree about one packaged object", refuseLockNamedOtherReference(lock, table))

	// A locked image missing from the table is a defect, not an omission.
	skipMissing := ImageTable{"docker.io/example/app:v1": row("docker.io/example/app:v1", "127.0.0.1:5000/example/app:v1", pin)}
	lockedAway := &MaterialsLock{Images: []LockedImage{{Original: "docker.io/gone/app:v1",
		HaulerRef: "127.0.0.1:5000/gone/app:v1", SourceManifestDigest: pin}}}
	mustRefuse(t, "missing from images.tsv", lockedAway.VerifyImageTableAgainstLock(skipMissing))
}

func copyTree(from, to string) error {
	return filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
}
