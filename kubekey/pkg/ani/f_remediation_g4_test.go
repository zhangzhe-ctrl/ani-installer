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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// F06 G4 regressions for the packaging-side material steps: chart placement is
// bound to the individual lock entry, the nested repository ISO is verified by
// content, and the packaged image store is checked with the install's own
// registry content gate. Everything runs in temp directories; nothing touches a
// cluster, a registry or the network.
// ---------------------------------------------------------------------------

func g4Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func g4FileDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := fileDigestHex(path)
	if err != nil {
		t.Fatalf("digest %s: %v", path, err)
	}
	return digest
}

func g4Write(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// g4Chart writes a realistic Helm chart archive: a gzipped tar whose top level is
// <name>/ with Chart.yaml inside, so the archive declares its own identity.
func g4Chart(t *testing.T, dir, name, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.tgz", name, version))
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressor := gzip.NewWriter(file)
	writer := tar.NewWriter(compressor)
	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)
	template := "# values\n"
	for _, entry := range []struct {
		name string
		body string
	}{
		{name: name + "/Chart.yaml", body: chartYAML},
		{name: name + "/values.yaml", body: template},
	} {
		if err := writer.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: entry.name, Size: int64(len(entry.body)), Mode: 0o644,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// g4Lock stages two approved charts and writes a lock that names both entries.
func g4Lock(t *testing.T, chartsDir string) (lockPath, alphaSHA, betaSHA string) {
	t.Helper()
	alpha := g4Chart(t, filepath.Join(chartsDir, "alpha"), "alpha", "1.0.0")
	beta := g4Chart(t, filepath.Join(chartsDir, "beta"), "beta", "2.0.0")
	alphaSHA, betaSHA = g4FileDigest(t, alpha), g4FileDigest(t, beta)
	lock := fmt.Sprintf("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"+
		"lockedAt: \"2026-09-26\"\nlockBatch: T-G4\n"+
		"components:\n"+
		"  alpha:\n    chartVersion: 1.0.0\n    sha256: %s\n    artifactChartPath: charts/alpha/1.0.0.tgz\n"+
		"  beta:\n    chartVersion: 2.0.0\n    sha256: %s\n    artifactChartPath: charts/beta/2.0.0.tgz\n", alphaSHA, betaSHA)
	lockPath = filepath.Join(t.TempDir(), "components.lock.yaml")
	g4Write(t, lockPath, []byte(lock))
	return lockPath, alphaSHA, betaSHA
}

// The approved pairing is per entry: each archive must carry its entry's digest,
// declare its entry's name and version, and land only at its own path.
func TestG4PlaceChartsBindsEachLockEntry(t *testing.T) {
	chartsDir := t.TempDir()
	lockPath, alphaSHA, betaSHA := g4Lock(t, chartsDir)
	artifactRoot := t.TempDir()

	var log bytes.Buffer
	if err := RunMaterialsPlaceCharts(MaterialsChartInput{
		LockPath: lockPath, ChartsDir: chartsDir, ArtifactRoot: artifactRoot,
	}, &log); err != nil {
		t.Fatalf("the approved chart set must place cleanly: %v\n%s", err, log.String())
	}
	for _, want := range []struct{ path, sha string }{
		{"charts/alpha/1.0.0.tgz", alphaSHA},
		{"charts/beta/2.0.0.tgz", betaSHA},
	} {
		if landed := filepath.Join(artifactRoot, want.path); g4FileDigest(t, landed) != want.sha {
			t.Fatalf("%s did not land with the approved digest", want.path)
		}
	}
	if !strings.Contains(log.String(), "each bound to its own lock entry") {
		t.Fatalf("the placement must report the per-entry binding:\n%s", log.String())
	}
}

// The exact repro the audit isolated: beta's archive presented as alpha's chart.
// The old grep accepted any hash that appeared anywhere in the lock text and
// copied it to alpha's official path; the per-entry pairing must refuse.
func TestG4PlaceChartsRejectsForeignChartInOwnDirectory(t *testing.T) {
	sourceDir := t.TempDir()
	beta := g4Chart(t, sourceDir, "beta", "2.0.0")
	betaBytes, err := os.ReadFile(beta)
	if err != nil {
		t.Fatal(err)
	}
	chartsDir := filepath.Join(sourceDir, "staged")
	g4Write(t, filepath.Join(chartsDir, "alpha", "beta-disguised.tgz"), betaBytes)

	lockPath := filepath.Join(t.TempDir(), "lock.yaml")
	g4Write(t, lockPath, []byte(fmt.Sprintf("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"+
		"lockedAt: \"2026-09-26\"\nlockBatch: T-G4\ncomponents:\n"+
		"  alpha:\n    chartVersion: 1.0.0\n    sha256: %s\n    artifactChartPath: charts/alpha/1.0.0.tgz\n",
		g4FileDigest(t, beta))))

	var log bytes.Buffer
	artifactRoot := t.TempDir()
	err = RunMaterialsPlaceCharts(MaterialsChartInput{
		LockPath: lockPath, ChartsDir: chartsDir, ArtifactRoot: artifactRoot,
	}, &log)
	if err == nil || !strings.Contains(err.Error(), "declares itself beta 2.0.0") {
		t.Fatalf("beta's bytes may not be packaged as alpha, got %v\n%s", err, log.String())
	}
	if _, statErr := os.Stat(filepath.Join(artifactRoot, "charts", "alpha", "1.0.0.tgz")); !os.IsNotExist(statErr) {
		t.Fatal("a refused placement must leave no chart at alpha's official path")
	}
}

// An archive whose digest is approved but which sits where another approved
// archive should be, is still a refusal: the entry's own source must exist.
func TestG4PlaceChartsRequiresEachApprovedSource(t *testing.T) {
	chartsDir := t.TempDir()
	lockPath, _, _ := g4Lock(t, chartsDir)
	if err := os.Remove(filepath.Join(chartsDir, "beta", "beta-2.0.0.tgz")); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	err := RunMaterialsPlaceCharts(MaterialsChartInput{
		LockPath: lockPath, ChartsDir: chartsDir, ArtifactRoot: t.TempDir(),
	}, &log)
	if err == nil || !strings.Contains(err.Error(), "no chart archive under") {
		t.Fatalf("a missing approved source must be refused, got %v\n%s", err, log.String())
	}
}

// A stray archive that no entry approves, and a stray archive already inside the
// artifact, are both refusals.
func TestG4PlaceChartsRefusesUnapprovedAndStray(t *testing.T) {
	t.Run("unapproved archive in the source tree", func(t *testing.T) {
		chartsDir := t.TempDir()
		lockPath, _, _ := g4Lock(t, chartsDir)
		g4Chart(t, filepath.Join(chartsDir, "extra"), "extra", "9.9.9")
		err := RunMaterialsPlaceCharts(MaterialsChartInput{
			LockPath: lockPath, ChartsDir: chartsDir, ArtifactRoot: t.TempDir(),
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "not the exact match of any lock entry") {
			t.Fatalf("an unapproved chart must be refused, got %v", err)
		}
	})

	t.Run("stray archive already in the artifact", func(t *testing.T) {
		chartsDir := t.TempDir()
		lockPath, _, _ := g4Lock(t, chartsDir)
		artifactRoot := t.TempDir()
		g4Write(t, filepath.Join(artifactRoot, "charts", "stray", "0.0.1.tgz"), []byte("no entry placed this\n"))
		err := RunMaterialsPlaceCharts(MaterialsChartInput{
			LockPath: lockPath, ChartsDir: chartsDir, ArtifactRoot: artifactRoot,
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "no lock entry placed") {
			t.Fatalf("a chart the artifact carries without an entry must be refused, got %v", err)
		}
	})

	// The artifact path of a lock entry is a path, not a suggestion.
	for _, bad := range []string{"/etc/passwd", "charts/../../escape.tgz", "images/alpha.tgz", "charts/alpha.txt", ""} {
		if _, err := artifactDestination(t.TempDir(), bad); err == nil {
			t.Fatalf("artifactChartPath %q must be refused", bad)
		}
	}
}

// g4ISORecord writes a source-side record "<file name> <sha256>" for the ISO.
func g4ISORecord(t *testing.T, dir, fileName, digest string) string {
	t.Helper()
	path := filepath.Join(dir, "repository-iso-checksums.txt")
	g4Write(t, path, []byte(fmt.Sprintf("%s %s\n", fileName, digest)))
	return path
}

// g4Tarball writes a tarball with the given entries, optionally gzipped.
func g4Tarball(t *testing.T, path string, entries map[string]string, gzipped bool) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var sink io.Writer = file
	var compressor *gzip.Writer
	if gzipped {
		compressor = gzip.NewWriter(file)
		defer compressor.Close()
		sink = compressor
	}
	writer := tar.NewWriter(sink)
	defer writer.Close()
	for name, body := range entries {
		if err := writer.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Size: int64(len(body)), Mode: 0o644,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}

// The nested ISO is judged by content, never by the presence of its name.
func TestG4InjectRepositoryISOVerifiesContent(t *testing.T) {
	isoBody := "approved repository iso bytes\n"
	entry := "repository/ubuntu-24.04-debs-amd64.iso"

	for _, form := range []struct {
		name    string
		gzipped bool
		suffix  string
	}{{"plain tar", false, ".tar"}, {"gzipped tar", true, ".tgz"}} {
		t.Run(form.name, func(t *testing.T) {
			dir := t.TempDir()
			iso := filepath.Join(dir, "ubuntu-24.04-debs-amd64.iso")
			g4Write(t, iso, []byte(isoBody))
			tarball := filepath.Join(dir, "kubekey-artifact"+form.suffix)
			g4Tarball(t, tarball, map[string]string{"bin/kk": "some binary\n"}, form.gzipped)
			artifactRoot := t.TempDir()

			input := MaterialsISOInput{
				ArtifactRoot:  artifactRoot,
				ArtifactTar:   tarball,
				ISOFile:       iso,
				EntryPath:     entry,
				LooseCopyPath: entry,
				ChecksumsFile: g4ISORecord(t, dir, "ubuntu-24.04-debs-amd64.iso", g4Hex([]byte(isoBody))),
			}
			var log bytes.Buffer
			if err := RunMaterialsInjectRepositoryISO(context.Background(), input, &log); err != nil {
				t.Fatalf("injecting the approved ISO: %v\n%s", err, log.String())
			}
			digest, found, err := tarEntryDigest(tarball, entry)
			if err != nil || !found || digest != g4Hex([]byte(isoBody)) {
				t.Fatalf("the tarball must carry the approved ISO: found=%v digest=%q err=%v", found, digest, err)
			}
			if other, found, _ := tarEntryDigest(tarball, "bin/kk"); !found || other != g4Hex([]byte("some binary\n")) {
				t.Fatal("the rewrite must preserve every other entry byte for byte")
			}
			// The loose copy the installer reads is the same approved bytes.
			if landed := g4FileDigest(t, filepath.Join(artifactRoot, entry)); landed != g4Hex([]byte(isoBody)) {
				t.Fatal("the loose repository copy must be the approved ISO")
			}

			// A second pass finds the verified entry and changes nothing.
			before := g4FileDigest(t, tarball)
			log.Reset()
			if err := RunMaterialsInjectRepositoryISO(context.Background(), input, &log); err != nil {
				t.Fatalf("the second pass must accept the verified entry: %v\n%s", err, log.String())
			}
			if !strings.Contains(log.String(), "already inside") {
				t.Fatalf("the log must record a verification, not a re-injection:\n%s", log.String())
			}
			if after := g4FileDigest(t, tarball); after != before {
				t.Fatal("a verified tarball must not be rewritten")
			}
		})
	}

	t.Run("a same-named ISO with different bytes is replaced", func(t *testing.T) {
		dir := t.TempDir()
		iso := filepath.Join(dir, "repository.iso")
		g4Write(t, iso, []byte(isoBody))
		tarball := filepath.Join(dir, "artifact.tar")
		g4Tarball(t, tarball, map[string]string{
			"repository/repository.iso": "a DIFFERENT iso that only matches by name\n",
		}, false)

		input := MaterialsISOInput{
			ArtifactTar: tarball, ISOFile: iso, EntryPath: "repository/repository.iso",
			ChecksumsFile: g4ISORecord(t, dir, "repository.iso", g4Hex([]byte(isoBody))),
		}
		var log bytes.Buffer
		if err := RunMaterialsInjectRepositoryISO(context.Background(), input, &log); err != nil {
			t.Fatalf("a wrong-content ISO entry must be replaced: %v\n%s", err, log.String())
		}
		if !strings.Contains(log.String(), "not the approved") || !strings.Contains(log.String(), "replacing that entry") {
			t.Fatalf("the log must record the replacement decision:\n%s", log.String())
		}
		digest, found, err := tarEntryDigest(tarball, input.EntryPath)
		if err != nil || !found || digest != g4Hex([]byte(isoBody)) {
			t.Fatalf("the entry must now hold the approved ISO: found=%v digest=%q err=%v", found, digest, err)
		}
	})

	t.Run("an ISO that does not match its source-side record is refused", func(t *testing.T) {
		dir := t.TempDir()
		iso := filepath.Join(dir, "repository.iso")
		g4Write(t, iso, []byte("tampered iso\n"))
		tarball := filepath.Join(dir, "artifact.tar")
		g4Tarball(t, tarball, map[string]string{}, false)
		var log bytes.Buffer
		err := RunMaterialsInjectRepositoryISO(context.Background(), MaterialsISOInput{
			ArtifactTar: tarball, ISOFile: iso, EntryPath: "repository/repository.iso",
			ChecksumsFile: g4ISORecord(t, dir, "repository.iso", g4Hex([]byte(isoBody))),
		}, &log)
		if err == nil || !strings.Contains(err.Error(), "source-side record says") {
			t.Fatalf("an unapproved ISO must be refused before packaging, got %v", err)
		}
		if digest := g4FileDigest(t, tarball); digest == "" {
			t.Fatal("the refusal must not damage the tarball")
		}
	})

	t.Run("a malformed or silent ISO record is refused", func(t *testing.T) {
		dir := t.TempDir()
		iso := filepath.Join(dir, "repository.iso")
		g4Write(t, iso, []byte(isoBody))
		base := MaterialsISOInput{
			ArtifactTar: filepath.Join(dir, "artifact.tar"), ISOFile: iso,
			EntryPath: "repository/repository.iso",
		}
		g4Tarball(t, base.ArtifactTar, map[string]string{}, false)

		badRecord := filepath.Join(dir, "bad.txt")
		g4Write(t, badRecord, []byte("repository.iso "+g4Hex([]byte(isoBody))+" extra\n"))
		err := RunMaterialsInjectRepositoryISO(context.Background(), func(in MaterialsISOInput) MaterialsISOInput {
			in.ChecksumsFile = badRecord
			return in
		}(base), nil)
		if err == nil || !strings.Contains(err.Error(), "not exactly") {
			t.Fatalf("a malformed record line must be refused, got %v", err)
		}

		other := filepath.Join(dir, "other.txt")
		g4Write(t, other, []byte("some-other.iso "+g4Hex([]byte(isoBody))+"\n"))
		err = RunMaterialsInjectRepositoryISO(context.Background(), func(in MaterialsISOInput) MaterialsISOInput {
			in.ChecksumsFile = other
			return in
		}(base), nil)
		if err == nil || !strings.Contains(err.Error(), "records no digest for") {
			t.Fatalf("a record that does not name this ISO must be refused, got %v", err)
		}
	})

	t.Run("a traversal entry path is refused", func(t *testing.T) {
		dir := t.TempDir()
		iso := filepath.Join(dir, "repository.iso")
		g4Write(t, iso, []byte(isoBody))
		err := RunMaterialsInjectRepositoryISO(context.Background(), MaterialsISOInput{
			ArtifactTar: filepath.Join(dir, "artifact.tar"), ISOFile: iso,
			EntryPath:     "repository/../escape.iso",
			ChecksumsFile: g4ISORecord(t, dir, "repository.iso", g4Hex([]byte(isoBody))),
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "no traversal") {
			t.Fatalf("a traversal entry path must be refused, got %v", err)
		}
	})
}

// The packaged store is verified with the install's own content gate: an index
// whose platform sub-manifest is missing cannot finish a package.
func TestG4VerifyPackagedImageStore(t *testing.T) {
	platformBody := r073PlatformManifest(r073ConfigDigest, r073LayerADigest)
	platformDigest := r073Digest(platformBody)
	indexBody := r073IndexBodySized(platformDigest, int64(len(platformBody)))

	dir := t.TempDir()
	lockFile := filepath.Join(dir, "components.lock.yaml")
	g4Write(t, lockFile, []byte(fmt.Sprintf("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"+
		"images:\n  - original: docker.io/example/app:v1\n    haulerRef: 127.0.0.1:5000/example/app:v1\n"+
		"    sourceManifestDigest: %s\n    amd64ManifestDigest: %s\n    use: test\n",
		r073Digest(indexBody), platformDigest)))
	if _, err := LoadMaterialsLock(lockFile); err != nil {
		t.Fatalf("the fixture lock must parse: %v", err)
	}
	tableFile := filepath.Join(dir, "images.tsv")
	g4Write(t, tableFile, []byte("original_ref\thauler_ref\tactual_digest\tuse_location\n"+
		fmt.Sprintf("docker.io/example/app:v1\t127.0.0.1:5000/example/app:v1\t%s\ttest\n", platformDigest)))

	registry := &r073Registry{
		manifests: map[string][]byte{
			"/v2/example/app/manifests/v1":                indexBody,
			"/v2/example/app/manifests/" + platformDigest: platformBody,
		},
		blobs: r073BlobSet(r073ConfigDigest, r073LayerADigest),
	}
	server := registry.start()
	defer server.Close()

	input := MaterialsRegistryVerifyInput{
		RegistryAddress: strings.TrimPrefix(server.URL, "http://"),
		ImagesTSVPath:   tableFile,
		LockPath:        lockFile,
	}
	var log bytes.Buffer
	if err := RunMaterialsVerifyRegistry(context.Background(), input, &log); err != nil {
		t.Fatalf("a complete store must verify: %v\n%s", err, log.String())
	}
	// The report must name, per row, which approval was actually proved, and the
	// tally must add up to the row count instead of computing a remainder.
	if !strings.Contains(log.String(), "approved materials lock") ||
		!strings.Contains(log.String(), "1 byte-exact with lock and pin") ||
		strings.Contains(log.String(), "pin only") {
		t.Fatalf("the report must say what approved each image:\n%s", log.String())
	}

	// The pin may name either object the row reaches: the tag's index or the
	// platform manifest inside it. Both are the packaged bytes; a third digest is
	// not.
	indexPinned := filepath.Join(t.TempDir(), "images.tsv")
	g4Write(t, indexPinned, []byte("original_ref\thauler_ref\tactual_digest\tuse_location\n"+
		fmt.Sprintf("docker.io/example/app:v1\t127.0.0.1:5000/example/app:v1\t%s\ttest\n", r073Digest(indexBody))))
	if err := RunMaterialsVerifyRegistry(context.Background(), MaterialsRegistryVerifyInput{
		RegistryAddress: input.RegistryAddress, ImagesTSVPath: indexPinned, LockPath: lockFile,
	}, nil); err != nil {
		t.Fatalf("a pin naming the served index must be accepted: %v", err)
	}
	wrongPinned := filepath.Join(t.TempDir(), "images.tsv")
	g4Write(t, wrongPinned, []byte("original_ref\thauler_ref\tactual_digest\tuse_location\n"+
		fmt.Sprintf("docker.io/example/app:v1\t127.0.0.1:5000/example/app:v1\t%s\ttest\n",
			r073Digest([]byte("some other object")))))
	err := RunMaterialsVerifyRegistry(context.Background(), MaterialsRegistryVerifyInput{
		RegistryAddress: input.RegistryAddress, ImagesTSVPath: wrongPinned, LockPath: lockFile,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not match the pinned transport identity") {
		t.Fatalf("a pin naming a different object must be refused, got %v", err)
	}

	// The packaged index without its platform sub-manifest: the old packaging
	// check passed this shape because it never looked below the index.
	registry.manifests = map[string][]byte{"/v2/example/app/manifests/v1": indexBody}
	err = RunMaterialsVerifyRegistry(context.Background(), input, nil)
	if err == nil || !strings.Contains(err.Error(), "sub-manifest is absent") {
		t.Fatalf("an index whose platform object is missing must fail packaging, got %v", err)
	}

	// A store missing a whole approved row is refused too.
	registry.manifests = map[string][]byte{}
	err = RunMaterialsVerifyRegistry(context.Background(), input, nil)
	if err == nil || !strings.Contains(err.Error(), "packaged image docker.io/example/app:v1") {
		t.Fatalf("a missing packaged image must fail, got %v", err)
	}
}

// Tool binaries are bound to their own lock entry too: the approved digest is
// the one recorded for THAT tool, never the first binarySha256 line in the file.
func TestG4PlaceToolsBindsEachApprovedBinary(t *testing.T) {
	dir := t.TempDir()
	helm := filepath.Join(dir, "helm")
	g4Write(t, helm, []byte("approved helm bytes\n"))
	hauler := filepath.Join(dir, "hauler")
	g4Write(t, hauler, []byte("hauler bytes not named by the lock\n"))
	lockPath := filepath.Join(dir, "components.lock.yaml")
	g4Write(t, lockPath, []byte(fmt.Sprintf("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"+
		"lockedAt: \"2026-09-26\"\nlockBatch: T-G4\ntools:\n"+
		"  helm:\n    version: v3.20.0\n    source: https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz\n"+
		"    sourceTarballSha256: "+strings.Repeat("1", 64)+"\n"+
		"    binarySha256: %s\n    artifactPath: bin/helm\n",
		g4Hex([]byte("approved helm bytes\n")))))
	artifactRoot := filepath.Join(dir, "artifact")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	input := MaterialsToolInput{
		LockPath: lockPath, ArtifactRoot: artifactRoot,
		Sources: []string{"helm=" + helm}, Unapproved: []string{"hauler=" + hauler},
	}
	if err := RunMaterialsPlaceTools(input, &log); err != nil {
		t.Fatalf("the approved tool set must place cleanly: %v\n%s", err, log.String())
	}
	if landed := g4FileDigest(t, filepath.Join(artifactRoot, "bin", "helm")); landed != g4Hex([]byte("approved helm bytes\n")) {
		t.Fatal("bin/helm did not land with the approved digest")
	}
	info, err := os.Stat(filepath.Join(artifactRoot, "bin", "hauler"))
	if err != nil {
		t.Fatalf("the declared unapproved binary must still be packaged: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("bin/hauler is not executable: %v", info.Mode())
	}
	if !strings.Contains(log.String(), "placed WITHOUT lock approval") {
		t.Fatalf("an unapproved binary must be reported as exactly that:\n%s", log.String())
	}

	cases := []struct {
		name   string
		mutate func(*MaterialsToolInput)
		want   string
	}{
		{
			name: "wrong binary for the approved entry",
			mutate: func(in *MaterialsToolInput) {
				in.Sources = []string{"helm=" + hauler}
			},
			want: "but the lock approves",
		},
		{
			name: "missing approved source",
			mutate: func(in *MaterialsToolInput) {
				in.Sources = nil
			},
			want: "no --source helm=... was given",
		},
		{
			name: "unknown tool name",
			mutate: func(in *MaterialsToolInput) {
				in.Sources = append(in.Sources, "docker=/bin/true")
			},
			want: "names no tool in the materials lock",
		},
		{
			name: "approved tool passed as unapproved",
			mutate: func(in *MaterialsToolInput) {
				in.Unapproved = append(in.Unapproved, "helm="+helm)
			},
			want: "approved by the lock; pass it as --source helm=",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			freshRoot := filepath.Join(t.TempDir(), "artifact")
			if err := os.MkdirAll(freshRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			broken := MaterialsToolInput{
				LockPath: lockPath, ArtifactRoot: freshRoot,
				Sources: []string{"helm=" + helm}, Unapproved: []string{"hauler=" + hauler},
			}
			tc.mutate(&broken)
			var out bytes.Buffer
			err := RunMaterialsPlaceTools(broken, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal mentioning %q, got %v\n%s", tc.want, err, out.String())
			}
		})
	}

	// A binary nobody declared cannot ride along in bin/.
	strayRoot := filepath.Join(t.TempDir(), "artifact")
	g4Write(t, filepath.Join(strayRoot, "bin", "curl"), []byte("nobody declared this\n"))
	err = RunMaterialsPlaceTools(MaterialsToolInput{
		LockPath: lockPath, ArtifactRoot: strayRoot, Sources: []string{"helm=" + helm},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "no tool entry placed") {
		t.Fatalf("an undeclared bin file must be refused, got %v", err)
	}
}
