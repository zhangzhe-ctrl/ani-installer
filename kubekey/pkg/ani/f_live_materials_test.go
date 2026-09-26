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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-LIVE-01: every chart-backed component must name the chart and version the
// approved lock carries, and that version must be the one the chart archive
// declares about itself.
//
// The first real render of the selected combination on Fedora found
// cert-manager unreachable: the installer declared chart version "1.21.2"
// while the approved lock and the shipped archive's own Chart.yaml both say
// "v1.21.2". Nothing else could have caught it — no test had ever compared the
// spec table against the shipped materials — and the same string is what the
// live release check compares against, so a correctly installed cert-manager
// would have been refused as foreign too.
func TestChartSpecsMatchTheApprovedLockAndTheChartBytes(t *testing.T) {
	root := filepath.Join("..", "..", "ani")
	lock, err := LoadMaterialsLock(filepath.Join(root, "components.lock.yaml"))
	if err != nil {
		t.Fatalf("read the shipped materials lock: %v", err)
	}
	claimedBy := map[string]string{}
	for role, spec := range componentInstallSpecs {
		if spec.Chart == "" {
			continue
		}
		chart, err := chartFor(lock, spec.Chart, spec.ChartVersion)
		if err != nil {
			t.Fatalf("component %q declares chart %s %s, which the approved lock does not carry: %v",
				role, spec.Chart, spec.ChartVersion, err)
		}
		if other, taken := claimedBy[chart.Name+"/"+chart.ChartVersion]; taken {
			t.Fatalf("components %q and %q both claim approved chart %s %s; one approved chart is one component",
				other, role, chart.Name, chart.ChartVersion)
		}
		claimedBy[chart.Name+"/"+chart.ChartVersion] = role

		archive := filepath.Join(root, "charts", chart.Name, chart.Name+"-"+chart.ChartVersion+".tgz")
		if _, statErr := os.Stat(archive); statErr != nil {
			// Packaging renames this source into the lock's artifact path; the
			// development tree keeps the upstream name.
			archive = filepath.Join(root, chart.ArtifactPath)
		}
		name, version, err := readChartIdentity(archive)
		if err != nil {
			t.Fatalf("component %q: read the chart's own identity from %s: %v", role, archive, err)
		}
		if name != chart.Name || version != chart.ChartVersion {
			t.Fatalf("component %q: approved chart %s %s is packaged at %s, which declares itself %s %s",
				role, chart.Name, chart.ChartVersion, chart.ArtifactPath, name, version)
		}
	}
	for _, chart := range lock.Charts {
		if _, ok := claimedBy[chart.Name+"/"+chart.ChartVersion]; !ok {
			t.Fatalf("the approved lock carries chart %s %s that no component spec claims; "+
				"packaging it while nothing installs it hides a component that lost its material",
				chart.Name, chart.ChartVersion)
		}
	}
}

// T-LIVE-02: --unapproved-bin has to accept the form its own help documents.
//
// Packaging a real artifact declared `--unapproved-bin bin/hauler=/path`, and the
// step wrote it to bin/bin/hauler: the flag value was used as the file name and
// then prefixed with bin/ again, so the declared binary died on a missing parent
// directory after the approved tool had already landed. A caller following the
// documented form must get one bin/<name>, and a name that is not a single file
// name must still be refused rather than written elsewhere.
func TestPlaceToolsAcceptsTheDocumentedUnapprovedBinForm(t *testing.T) {
	lockTool := func(dir string) string {
		helm := filepath.Join(dir, "helm")
		g4Write(t, helm, []byte("approved helm bytes\n"))
		lockPath := filepath.Join(dir, "components.lock.yaml")
		g4Write(t, lockPath, []byte(fmt.Sprintf("apiVersion: ani.installer/v1\nkind: ComponentMaterialLock\n"+
			"lockedAt: \"2026-09-28\"\nlockBatch: T-LIVE\ntools:\n"+
			"  helm:\n    version: v3.20.0\n    source: https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz\n"+
			"    sourceTarballSha256: %s\n    binarySha256: %s\n    artifactPath: bin/helm\n",
			strings.Repeat("1", 64), g4Hex([]byte("approved helm bytes\n")))))
		return lockPath
	}
	unapproved := func(dir string) string {
		path := filepath.Join(dir, "hauler")
		g4Write(t, path, []byte("hauler bytes not named by the lock\n"))
		return path
	}

	for _, declaration := range []string{"hauler", "bin/hauler"} {
		t.Run(declaration, func(t *testing.T) {
			dir := t.TempDir()
			lockPath := lockTool(dir)
			artifactRoot := filepath.Join(dir, "artifact")
			if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			err := RunMaterialsPlaceTools(MaterialsToolInput{
				LockPath: lockPath, ArtifactRoot: artifactRoot,
				Sources:    []string{"helm=" + filepath.Join(dir, "helm")},
				Unapproved: []string{declaration + "=" + unapproved(dir)},
			}, &log)
			if err != nil {
				t.Fatalf("%q must package as bin/hauler: %v\n%s", declaration, err, log.String())
			}
			landed := filepath.Join(artifactRoot, "bin", "hauler")
			if _, statErr := os.Stat(landed); statErr != nil {
				t.Fatalf("bin/hauler did not land from %q: %v\n%s", declaration, statErr, log.String())
			}
			if info, statErr := os.Stat(landed); statErr != nil || info.Mode().Perm()&0o100 == 0 {
				t.Fatalf("bin/hauler is not executable: %v", info.Mode())
			}
			if strings.Contains(log.String(), "bin/bin/") {
				t.Fatalf("the declared binary was placed at a doubled path:\n%s", log.String())
			}
			if !strings.Contains(log.String(), "placed WITHOUT lock approval") {
				t.Fatalf("an unapproved binary must be reported as exactly that:\n%s", log.String())
			}
		})
	}

	// A name that is not one file name must be refused, and an approved tool
	// offered under the bin/ form must still be recognised as approved.
	for _, tc := range []struct {
		declaration string
		want        string
	}{
		{"bin/sub/hauler", "must be a single file name"},
		{"../escape", "must be a single file name"},
		{"bin/helm", "is approved by the lock"},
	} {
		t.Run(tc.declaration, func(t *testing.T) {
			dir := t.TempDir()
			lockPath := lockTool(dir)
			artifactRoot := filepath.Join(dir, "artifact")
			if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			err := RunMaterialsPlaceTools(MaterialsToolInput{
				LockPath: lockPath, ArtifactRoot: artifactRoot,
				Sources:    []string{"helm=" + filepath.Join(dir, "helm")},
				Unapproved: []string{tc.declaration + "=" + unapproved(dir)},
			}, &log)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%q must be refused mentioning %q, got %v\n%s",
					tc.declaration, tc.want, err, log.String())
			}
		})
	}
}

// T-LIVE-03: one file must be one entry. The first whole-package verification of
// this round produced a KubeKey artifact tarball carrying three members for the
// same repository ISO path (one "./repository/…" and two "repository/…"), because
// the inject step asked only what the *first* matching entry held and the rewrite
// dropped only the byte-for-byte spelling. Content happened to agree that time,
// but nothing in the package guarantees that: an extractor may take any of those
// entries, so "the first one is the approved ISO" proves nothing.
func TestInjectRepositoryISORejectsShadowEntries(t *testing.T) {
	const isoBody = "approved repository ISO bytes for the live round\n"

	t.Run("an approved entry shadowed by a different one is rewritten", func(t *testing.T) {
		dir := t.TempDir()
		iso := filepath.Join(dir, "repository.iso")
		g4Write(t, iso, []byte(isoBody))
		tarball := filepath.Join(dir, "artifact.tar")
		g4Tarball(t, tarball, map[string]string{
			"./repository/repository.iso": isoBody,
			"repository/repository.iso":   "a different ISO that an extractor could pick instead\n",
			"bin/kk":                      "kubekey payload\n",
		}, false)

		input := MaterialsISOInput{ArtifactTar: tarball, ISOFile: iso,
			EntryPath:     "repository/repository.iso",
			ChecksumsFile: g4ISORecord(t, dir, "repository.iso", g4Hex([]byte(isoBody)))}
		var log bytes.Buffer
		if err := RunMaterialsInjectRepositoryISO(context.Background(), input, &log); err != nil {
			t.Fatalf("shadowed entries must be rewritten, not approved: %v\n%s", err, log.String())
		}
		if !strings.Contains(log.String(), "carries 2 entries") {
			t.Fatalf("the log must report how many entries named this path:\n%s", log.String())
		}
		entries, err := tarEntriesFor(tarball, input.EntryPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Digest != g4Hex([]byte(isoBody)) {
			t.Fatalf("exactly one approved entry must remain, got %+v", entries)
		}
		// Other payload must survive the rewrite.
		if _, found, err := tarEntryDigest(tarball, "bin/kk"); err != nil || !found {
			t.Fatalf("the rewrite must keep unrelated entries: found=%v err=%v", found, err)
		}
	})

	t.Run("two identical approved entries still collapse to one", func(t *testing.T) {
		dir := t.TempDir()
		iso := filepath.Join(dir, "repository.iso")
		g4Write(t, iso, []byte(isoBody))
		tarball := filepath.Join(dir, "artifact.tar")
		g4Tarball(t, tarball, map[string]string{
			"./repository/repository.iso": isoBody,
			"repository/repository.iso":   isoBody,
		}, false)
		input := MaterialsISOInput{ArtifactTar: tarball, ISOFile: iso,
			EntryPath:     "repository/repository.iso",
			ChecksumsFile: g4ISORecord(t, dir, "repository.iso", g4Hex([]byte(isoBody)))}
		var log bytes.Buffer
		if err := RunMaterialsInjectRepositoryISO(context.Background(), input, &log); err != nil {
			t.Fatal(err)
		}
		entries, err := tarEntriesFor(tarball, input.EntryPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("one file must be one entry after injection, got %d: %+v\n%s", len(entries), entries, log.String())
		}
	})
}
