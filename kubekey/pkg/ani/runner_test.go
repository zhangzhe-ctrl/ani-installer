package ani

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPathsSeparateArtifactAndRuntime(t *testing.T) {
	paths, err := resolveInstallPaths("/opt/ani-installer/artifacts/a1", validConfig())
	if err != nil {
		t.Fatalf("resolveInstallPaths() = %v", err)
	}
	wantArtifactRoot := "/opt/ani-installer/artifacts/a1"
	if paths.ArtifactRoot != wantArtifactRoot {
		t.Fatalf("ArtifactRoot = %q, want %q", paths.ArtifactRoot, wantArtifactRoot)
	}
	if paths.RuntimeRoot != "/var/lib/ani-installer/ani-lab" {
		t.Fatalf("RuntimeRoot = %q", paths.RuntimeRoot)
	}
	if paths.WorkRoot != "/var/lib/ani-installer/ani-lab/work" || paths.LogRoot != "/var/lib/ani-installer/ani-lab/logs" {
		t.Fatalf("runtime paths are not independent: %#v", paths)
	}
	for _, path := range []string{paths.WorkRoot, paths.LogRoot, paths.StoreDir, paths.RegistryDir, paths.KubeKeyWorkdir} {
		if strings.HasPrefix(path, paths.ArtifactRoot+string(os.PathSeparator)) {
			t.Fatalf("runtime path %q must not be inside artifact %q", path, paths.ArtifactRoot)
		}
	}
	if paths.HaulerPath != filepath.Join(paths.ArtifactRoot, "bin", "hauler") {
		t.Fatalf("HaulerPath = %q", paths.HaulerPath)
	}
}

func TestCreateRuntimeRootRejectsReuse(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "ani-lab")
	err := createRuntimeRoot(runtimeRoot)
	if err != nil {
		t.Fatalf("createRuntimeRoot() = %v", err)
	}
	err = createRuntimeRoot(runtimeRoot)
	if err == nil {
		t.Fatal("createRuntimeRoot() unexpectedly reused an existing root")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("createRuntimeRoot() existing-root error = %v, want fs.ErrExist", err)
	}
	info, err := os.Stat(runtimeRoot)
	if err != nil {
		t.Fatalf("stat runtime root: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("runtime root %q is not a directory", runtimeRoot)
	}
}
