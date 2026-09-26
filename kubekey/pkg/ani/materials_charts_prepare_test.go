/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// C10 — preparing the locked chart material a clean checkout does not carry.
//
// The gate test that keeps the component spec table, the approved lock and the
// shipped archives from drifting apart opens real .tgz files, and those files are
// gitignored, so a fresh clone has none of them. The chosen remedy is an explicit
// preparation step in front of the gate — never a skipped test. These tests use
// TLS test servers so the https rule the production fetcher enforces is the rule
// under test, not a rule the tests have to route around.
// ---------------------------------------------------------------------------

// c10ChartArchive writes a gzipped tar shaped the way readChartIdentity expects:
// every entry under <name>/ with Chart.yaml at depth 1.
func c10ChartArchive(t *testing.T, dir, name, version string) (path string, digest string) {
	t.Helper()
	path = filepath.Join(dir, fmt.Sprintf("%s-%s.tgz", name, version))
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	compressor := gzip.NewWriter(io_DiscardTee{first: file, second: hash})
	writer := tar.NewWriter(compressor)
	body := []byte(fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version))
	if err := writer.WriteHeader(&tar.Header{
		Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(hash.Sum(nil))
}

// io_DiscardTee writes to both sinks. Its name says what it is because the
// alternative — recomputing the digest after closing — would not prove the bytes
// that were written are the bytes that were hashed.
type io_DiscardTee struct{ first, second writeSink }

type writeSink interface{ Write([]byte) (int, error) }

func (t io_DiscardTee) Write(p []byte) (int, error) {
	if _, err := t.second.Write(p); err != nil {
		return 0, err
	}
	return t.first.Write(p)
}

// c10Lock writes a one-chart lock pointing at serverURL.
func c10Lock(t *testing.T, name, version, serverURL, digest string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "components.lock.yaml")
	lock := fmt.Sprintf("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\ncharts:\n"+
		"  - name: %s\n    chartVersion: %q\n    source: %s\n    sha256: %s\n"+
		"    artifactChartPath: charts/%s/%s.tgz\n", name, version, serverURL, digest, name, version)
	if err := os.WriteFile(path, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestC10_PrepareChartsLandsAndVerifiesEveryApprovedChart(t *testing.T) {
	chartDir := t.TempDir()
	type entry struct {
		name, version, digest, path string
	}
	entries := []entry{}
	for _, spec := range []struct{ name, version string }{
		{"alpha", "1.0.0"}, {"beta", "v2.3.4"}, {"gamma", "0.1.0"},
	} {
		path, digest := c10ChartArchive(t, chartDir, spec.name, spec.version)
		entries = append(entries, entry{spec.name, spec.version, digest, path})
	}

	mux := http.NewServeMux()
	served := map[string]int{}
	for _, e := range entries {
		data, err := os.ReadFile(e.path)
		if err != nil {
			t.Fatal(err)
		}
		key := e.name + "-" + e.version + ".tgz"
		mux.HandleFunc("/"+key, func(w http.ResponseWriter, _ *http.Request) {
			served[key]++
			_, _ = w.Write(data)
		})
	}
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	lockPath := filepath.Join(t.TempDir(), "components.lock.yaml")
	var lock strings.Builder
	lock.WriteString("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\ncharts:\n")
	for _, e := range entries {
		fmt.Fprintf(&lock, "  - name: %s\n    chartVersion: %q\n    source: %s/%s-%s.tgz\n    sha256: %s\n"+
			"    artifactChartPath: charts/%s/%s.tgz\n",
			e.name, e.version, server.URL, e.name, e.version, e.digest, e.name, e.version)
	}
	if err := os.WriteFile(lockPath, []byte(lock.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	out := &strings.Builder{}
	input := MaterialsPrepareChartsInput{LockPath: lockPath, Root: root, CacheDir: cache, Client: server.Client()}
	if err := RunMaterialsPrepareCharts(input, out); err != nil {
		t.Fatalf("preparation must land all three charts: %v\n%s", err, out)
	}
	for _, e := range entries {
		landed := filepath.Join(root, "charts", e.name, e.name+"-"+e.version+".tgz")
		got, err := fileDigestHex(landed)
		if err != nil {
			t.Fatalf("chart %s %s never reached %s: %v", e.name, e.version, landed, err)
		}
		if got != e.digest {
			t.Fatalf("chart %s %s landed as %s, the lock approves %s", e.name, e.version, got, e.digest)
		}
		name, version, err := readChartIdentity(landed)
		if err != nil {
			t.Fatalf("read the identity of %s: %v", landed, err)
		}
		if name != e.name || version != e.version {
			t.Fatalf("%s %s landed as %s %s", e.name, e.version, name, version)
		}
		if _, err := os.Stat(filepath.Join(cache, "sha256", e.digest+".tgz")); err != nil {
			t.Fatalf("the verified bytes were not cached: %v", err)
		}
	}

	// Idempotence, measured at the server rather than inferred from the log: a
	// second preparation must not fetch anything again.
	before := map[string]int{}
	for key, count := range served {
		before[key] = count
	}
	second := &strings.Builder{}
	if err := RunMaterialsPrepareCharts(input, second); err != nil {
		t.Fatalf("the second preparation must be a no-op: %v\n%s", err, second)
	}
	for key, count := range before {
		if served[key] != count {
			t.Fatalf("chart %s was fetched again on the second run (%d -> %d)", key, count, served[key])
		}
	}
	if n := strings.Count(second.String(), "already present"); n != len(entries) {
		t.Fatalf("expected every chart verified in place, got:\n%s", second)
	}
}

func TestC10_CacheHitIsAdoptedWithoutFetching(t *testing.T) {
	chartDir := t.TempDir()
	path, digest := c10ChartArchive(t, chartDir, "alpha", "1.0.0")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(data)
	}))
	defer server.Close()

	cache := filepath.Join(t.TempDir(), "cache")
	// Warm the cache from a first run, then clear the source tree: the second run
	// must be satisfied offline from cache and re-verify it.
	lockPath := c10Lock(t, "alpha", "1.0.0", server.URL+"/alpha-1.0.0.tgz", digest)
	root := t.TempDir()
	input := MaterialsPrepareChartsInput{LockPath: lockPath, Root: root, CacheDir: cache, Client: server.Client()}
	if err := RunMaterialsPrepareCharts(input, os.Stdout); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}
	if requests != 1 {
		t.Fatalf("the first run fetched %d times, want exactly 1", requests)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	offline := input
	offline.Root = root
	offline.Offline = true
	out := &strings.Builder{}
	if err := RunMaterialsPrepareCharts(offline, out); err != nil {
		t.Fatalf("a warm cache must satisfy --offline: %v\n%s", err, out)
	}
	if requests != 1 {
		t.Fatalf("--offline still reached the network (%d requests)", requests)
	}
	if !strings.Contains(out.String(), "from cache") {
		t.Fatalf("the run did not report a cache adoption:\n%s", out)
	}
}

func TestC10_WrongDigestIsRefusedAndNothingIsAdopted(t *testing.T) {
	approvedDir := t.TempDir()
	otherPath, _ := c10ChartArchive(t, approvedDir, "alpha", "9.9.9")
	other, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	_, approvedDigest := c10ChartArchive(t, approvedDir, "alpha", "1.0.0")

	cases := []struct {
		name   string
		handle http.HandlerFunc
		want   string
	}{
		{
			name:   "different bytes",
			handle: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(other) },
			want:   "hashes to",
		},
		{
			name:   "not found",
			handle: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:   "404",
		},
		{
			name:   "empty body",
			handle: func(w http.ResponseWriter, _ *http.Request) {},
			want:   "empty body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(tc.handle))
			defer server.Close()
			root := t.TempDir()
			lockPath := c10Lock(t, "alpha", "1.0.0", server.URL+"/alpha-1.0.0.tgz", approvedDigest)
			err := RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
				LockPath: lockPath, Root: root, CacheDir: filepath.Join(t.TempDir(), "cache"), Client: server.Client(),
			}, os.Stdout)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected a refusal naming %q, got %v", tc.want, err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "charts", "alpha", "alpha-1.0.0.tgz")); !os.IsNotExist(statErr) {
				t.Fatal("a refused fetch still placed a chart")
			}
		})
	}
}

func TestC10_CorruptCacheEntryIsNotLaunderedIntoAPass(t *testing.T) {
	chartDir := t.TempDir()
	_, digest := c10ChartArchive(t, chartDir, "alpha", "1.0.0")
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(filepath.Join(cache, "sha256"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A different valid archive, stored under the approved digest's file name.
	otherDir := t.TempDir()
	otherPath, _ := c10ChartArchive(t, otherDir, "alpha", "9.9.9")
	other, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "sha256", digest+".tgz"), other, 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	lockPath := c10Lock(t, "alpha", "1.0.0", "https://example.invalid/alpha-1.0.0.tgz", digest)
	err = RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
		LockPath: lockPath, Root: root, CacheDir: cache, Offline: true,
	}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "cache entry") {
		t.Fatalf("a cache file that is not its own digest must be refused, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "charts", "alpha", "alpha-1.0.0.tgz")); !os.IsNotExist(statErr) {
		t.Fatal("the corrupt cache entry was adopted")
	}
}

func TestC10_WrongBytesOnDiskAreReportedNotOverwritten(t *testing.T) {
	chartDir := t.TempDir()
	goodPath, digest := c10ChartArchive(t, chartDir, "alpha", "1.0.0")
	good, err := os.ReadFile(goodPath)
	if err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	otherPath, _ := c10ChartArchive(t, otherDir, "alpha", "9.9.9")
	other, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "charts", "alpha", "alpha-1.0.0.tgz")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, other, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := fileDigestHex(destination)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(good)
	}))
	defer server.Close()
	lockPath := c10Lock(t, "alpha", "1.0.0", server.URL+"/alpha-1.0.0.tgz", digest)
	err = RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
		LockPath: lockPath, Root: root, CacheDir: filepath.Join(t.TempDir(), "cache"), Client: server.Client(),
	}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "remove it deliberately") {
		t.Fatalf("wrong material on disk must be reported, not silently replaced: %v", err)
	}
	after, err := fileDigestHex(destination)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("the preparation overwrote material an operator had placed")
	}
}

func TestC10_ArchiveDeclaringAnotherChartIsRefused(t *testing.T) {
	// The bytes match the approved digest, so a digest-only check would pass. The
	// archive must also say which chart it is — the pairing rule packaging already
	// enforces, and the source tree must not be where it is relaxed.
	chartDir := t.TempDir()
	path, digest := c10ChartArchive(t, chartDir, "impostor", "1.0.0")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(good)
	}))
	defer server.Close()
	root := t.TempDir()
	lockPath := c10Lock(t, "alpha", "1.0.0", server.URL+"/alpha-1.0.0.tgz", digest)
	err = RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
		LockPath: lockPath, Root: root, CacheDir: filepath.Join(t.TempDir(), "cache"), Client: server.Client(),
	}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "declares itself") {
		t.Fatalf("an archive naming a different chart must be refused, got %v", err)
	}
}

func TestC10_NonHttpsSourceIsRefused(t *testing.T) {
	root := t.TempDir()
	lockPath := c10Lock(t, "alpha", "1.0.0", "http://example.invalid/alpha-1.0.0.tgz", strings.Repeat("a", 64))
	err := RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
		LockPath: lockPath, Root: root, CacheDir: filepath.Join(t.TempDir(), "cache"),
	}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("a plaintext source must be refused, got %v", err)
	}
}

func TestC10_LockNamesCannotEscapeTheSourceTree(t *testing.T) {
	// The destination is built from the entry's own name and version rather than
	// copied from artifactChartPath, so those two fields are the input that has to
	// be constrained. A lock that names a chart "../evil" must be refused before
	// anything is fetched or written.
	for _, tc := range []struct{ name, version string }{
		{"../evil", "1.0.0"},
		{"a/../../evil", "1.0.0"},
		{"..", "1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			lockPath := c10Lock(t, tc.name, tc.version, "https://example.invalid/a.tgz", strings.Repeat("a", 64))
			err := RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
				LockPath: lockPath, Root: root, CacheDir: filepath.Join(t.TempDir(), "cache"), Offline: true,
			}, os.Stdout)
			if err == nil || !strings.Contains(err.Error(), "not a single safe path element") {
				t.Fatalf("a lock-supplied chart name %q that leaves the root must be refused, got %v", tc.name, err)
			}
		})
	}
}

func TestC10_OfflineNamesEveryChartItCouldNotPrepare(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "components.lock.yaml")
	var lock strings.Builder
	lock.WriteString("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\ncharts:\n")
	for _, name := range []string{"alpha", "beta"} {
		fmt.Fprintf(&lock, "  - name: %s\n    chartVersion: \"1.0.0\"\n"+
			"    source: https://example.invalid/%s.tgz\n    sha256: %s\n"+
			"    artifactChartPath: charts/%s/1.0.0.tgz\n", name, name, strings.Repeat("b", 64), name)
	}
	if err := os.WriteFile(lockPath, []byte(lock.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RunMaterialsPrepareCharts(MaterialsPrepareChartsInput{
		LockPath: lockPath, Root: root, CacheDir: filepath.Join(t.TempDir(), "cache"), Offline: true,
	}, os.Stdout)
	if err == nil {
		t.Fatal("--offline with no material anywhere must fail, never skip")
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("the refusal must name every missing chart, %q missing from %v", name, err)
		}
	}
	if !strings.Contains(err.Error(), "network access") {
		t.Fatalf("the refusal must say how to satisfy it: %v", err)
	}
}

// TestC10_ShippedLockCanActuallyBePreparedForEveryChart is pure data and needs no
// network. Components spell their origin chartSource while batch sections spell
// it source; reading only one of the two silently lost half the lock's provenance
// and made those entries unpreparable.
func TestC10_ShippedLockCanActuallyBePreparedForEveryChart(t *testing.T) {
	lock, err := LoadMaterialsLock(filepath.Join("..", "..", "ani", "components.lock.yaml"))
	if err != nil {
		t.Fatalf("read the shipped lock: %v", err)
	}
	if len(lock.Charts) == 0 {
		t.Fatal("the shipped lock carries no charts")
	}
	for _, chart := range lock.Charts {
		if chart.Name == "" || chart.ChartVersion == "" {
			t.Fatalf("incomplete lock entry: %+v", chart)
		}
		if !strings.HasPrefix(chart.Source, "https://") {
			t.Fatalf("chart %s %s has source %q; preparation accepts nothing but https",
				chart.Name, chart.ChartVersion, chart.Source)
		}
		if !isHex64(strings.ToLower(strings.TrimPrefix(chart.SHA256, "sha256:"))) {
			t.Fatalf("chart %s %s has no 64-hex approved digest: %q", chart.Name, chart.ChartVersion, chart.SHA256)
		}
	}
}
