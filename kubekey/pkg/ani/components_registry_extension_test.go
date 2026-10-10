package ani

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComponentRegistryAppendRejectsForeignTagBeforeWriting(t *testing.T) {
	store := os.Getenv("ANI_LWS_IMAGE_STORE")
	if store == "" {
		t.Skip("requires fixed offline LWS image store on Fedora")
	}
	lock, err := LoadMaterialsLock(filepath.Join("..", "..", "ani", "components.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	row := r08FullTable(t)[LWSImageReference]
	dir := t.TempDir()
	tag := filepath.Join(dir, "docker/registry/v2/repositories/lws/lws/_manifests/tags/v0.10.0/current/link")
	if err := os.MkdirAll(filepath.Dir(tag), 0755); err != nil {
		t.Fatal(err)
	}
	foreign := "sha256:" + strings.Repeat("f", 64)
	if err := os.WriteFile(tag, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := componentRegistryFiles(store, dir, row, lock); err == nil || !strings.Contains(err.Error(), "existing registry reference conflicts") {
		t.Fatalf("foreign tag must prevent all registry additions: %v", err)
	}
	got, err := os.ReadFile(tag)
	if err != nil || string(got) != foreign {
		t.Fatalf("existing tag changed: %s %v", got, err)
	}
	count := 0
	if err := filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rejected import wrote %d files", count-1)
	}
}

func TestComponentRegistryAppendServesExactImageWithoutRestart(t *testing.T) {
	store, hauler := os.Getenv("ANI_LWS_IMAGE_STORE"), os.Getenv("ANI_HAULER_TEST_BIN")
	if store == "" || hauler == "" {
		t.Skip("requires approved Fedora offline image store and Hauler binary")
	}
	lock, err := LoadMaterialsLock(filepath.Join("..", "..", "ani", "components.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	approvedTool, ok := lock.ToolByArtifactPath("bin/hauler")
	if !ok || sha256FileHex(hauler) != strings.TrimPrefix(approvedTool.BinarySHA256, "sha256:") {
		t.Fatal("test Hauler is not source approved")
	}
	root := t.TempDir()
	emptyStore := filepath.Join(root, "empty-store")
	if err := os.MkdirAll(emptyStore, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"oci-layout": "{\"imageLayoutVersion\":\"1.0.0\"}", "index.json": "{\"schemaVersion\":2,\"mediaType\":\"application/vnd.oci.image.index.v1+json\",\"manifests\":[]}"} {
		if err := os.WriteFile(filepath.Join(emptyStore, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dataRoot := filepath.Join(root, "registry-data")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	log, err := os.Create(filepath.Join(root, "hauler.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, hauler, "--haulerdir", filepath.Join(root, "hauler-state"), "store", "serve", "registry", "--port", fmt.Sprint(port), "--directory", dataRoot, "--readonly=true", "--store", emptyStore)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	ready := false
	for ctx.Err() == nil {
		resp, err := client.Get("http://" + address + "/v2/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		data, _ := os.ReadFile(log.Name())
		t.Fatalf("fixed Hauler registry failed startup: %s", data)
	}
	row := r08FullTable(t)[LWSImageReference]
	files, err := componentRegistryFiles(store, dataRoot, row, lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := installRegistryAppendFiles(files); err != nil {
		t.Fatal(err)
	}
	gate := NewRegistryContentChecker(client, address, lock, []string{store}, true)
	if _, err := gate.Verify(ctx, row); err != nil {
		t.Fatalf("running readonly registry did not expose exact approved content: %v", err)
	}
	tag := files[len(files)-1].path
	before, err := os.Stat(tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := installRegistryAppendFiles(files); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(tag)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatal("same-version registry addition rewrote an existing tag")
	}
}
