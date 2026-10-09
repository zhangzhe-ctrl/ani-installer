package ani

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComponentImagesVerifySlowBlobBytesAndStillRejectCorruption(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	registry := &r073Registry{manifests: map[string][]byte{}, blobs: r073BlobSet(r073ConfigDigest, r073LayerADigest)}
	base := registry.handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+r073LayerADigest) && slow.Load() {
			// Start a valid body immediately, then model a cold large layer whose
			// full transfer exceeds the old component gate's 10-second timeout.
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(r073LayerABody[:1])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(11 * time.Second):
			}
			_, _ = w.Write(r073LayerABody[1:])
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	number, _ := strconv.Atoi(port)
	cluster := ClusterConfig{InstallerNode: "installer", Nodes: []NodeConfig{{Name: "installer", Address: host}}, RegistryConfig: Registry{Port: number}}
	cluster.Components.Valkey.Enabled = true
	body := r073PlatformManifest(r073ConfigDigest, r073LayerADigest)
	var rows []string
	for _, key := range imagesForComponent(cluster, "valkey") {
		ref := "127.0.0.1:5000/valkey/valkey:8.1.10-alpine"
		registry.manifests["/v2/valkey/valkey/manifests/8.1.10-alpine"] = body
		rows = append(rows, key.Original+"\t"+ref+"\t"+r073Digest(body)+"\ttest")
	}
	if len(rows) != 1 {
		t.Fatalf("expected the real valkey component's one image, got %d", len(rows))
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "images", "images.tsv"), []byte("original_ref\thauler_ref\tdigest\tuse\n"+strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightComponentImages(context.Background(), cluster, root, nil, []string{"valkey"}, io.Discard); err != nil {
		t.Fatalf("a complete slow transfer with correct bytes must pass: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := preflightComponentImages(ctx, cluster, root, nil, []string{"valkey"}, io.Discard); err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("caller deadline must still bound the transfer: %v", err)
	}
	slow.Store(false)
	registry.blobs[r073LayerADigest] = []byte("corrupted layer")
	if err := preflightComponentImages(context.Background(), cluster, root, nil, []string{"valkey"}, io.Discard); err == nil || !strings.Contains(err.Error(), "hash to") {
		t.Fatalf("a longer budget must still reject corrupt bytes: %v", err)
	}
}
