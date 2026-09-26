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
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// This file holds the material steps the packaging script used to do with
// grep/awk (F06): placing approved charts, injecting and verifying the nested
// repository ISO, and checking the finished image store. Each one reuses the
// same lock parser and content gate the install uses, so packaging and
// consumption cannot grow into two different rule sets.

// materialWriter keeps the packaging logs from ever writing into a nil writer.
func materialWriter(stdout io.Writer) io.Writer {
	if stdout == nil {
		return io.Discard
	}
	return stdout
}

// ---------------------------------------------------------------------------
// Charts: place exactly what the lock approves, bound to the entry that
// approved it, and verify every landed file.
// ---------------------------------------------------------------------------

// MaterialsChartInput describes one chart-placement run.
type MaterialsChartInput struct {
	LockPath     string
	ChartsDir    string
	ArtifactRoot string
}

// fileDigestHex streams a file through sha256 and returns the hex digest. Big
// materials (the repository ISO) must never be read into memory whole.
func fileDigestHex(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.Wrapf(err, "open %s", path)
	}
	defer file.Close()
	return hashStream(file)
}

// readChartIdentity opens a packaged chart and reads its own Chart.yaml, so a
// file is approved by more than its bytes: the archive must say which chart it
// is.
func readChartIdentity(path string) (name string, version string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", errors.Wrapf(err, "open chart archive %s", path)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return "", "", fmt.Errorf("chart archive %s is not gzip: %v", path, err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return "", "", fmt.Errorf("chart archive %s carries no Chart.yaml at its top level", path)
		}
		if err != nil {
			return "", "", errors.Wrapf(err, "read chart archive %s", path)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		// A chart stores everything under <name>/, so Chart.yaml sits at depth 1.
		segments := strings.Split(strings.TrimPrefix(header.Name, "./"), "/")
		if len(segments) != 2 || segments[1] != "Chart.yaml" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(archive, 1<<20))
		if err != nil {
			return "", "", errors.Wrapf(err, "read Chart.yaml of %s", path)
		}
		var declared struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		}
		if err := yaml.Unmarshal(body, &declared); err != nil {
			return "", "", errors.Wrapf(err, "parse Chart.yaml of %s", path)
		}
		if strings.TrimSpace(declared.Name) == "" || strings.TrimSpace(declared.Version) == "" {
			return "", "", fmt.Errorf("Chart.yaml of %s declares no name and version", path)
		}
		return declared.Name, declared.Version, nil
	}
}

// safeArtifactPath resolves one lock-declared artifact path inside the artifact
// root, refusing absolute paths, traversal, backslashes, and anything that
// escapes the root after cleaning.
func safeArtifactPath(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("the lock entry names no artifact path")
	}
	if filepath.IsAbs(relative) || strings.ContainsRune(relative, '\\') ||
		strings.Contains(relative, "..") || !filepath.IsLocal(filepath.Clean(relative)) {
		return "", fmt.Errorf("artifact path %q must be a relative path with no traversal", relative)
	}
	destination := filepath.Join(root, relative)
	if destination != filepath.Clean(destination) ||
		!strings.HasPrefix(destination, filepath.Clean(root)+string(os.PathSeparator)) {
		return "", fmt.Errorf("artifact path %q resolves outside the artifact root", relative)
	}
	return destination, nil
}

// artifactDestination resolves one lock artifactChartPath inside the artifact
// root; charts additionally live under charts/ and must be .tgz archives.
func artifactDestination(root, artifactPath string) (string, error) {
	if !strings.HasPrefix(artifactPath, "charts/") || !strings.HasSuffix(artifactPath, ".tgz") {
		return "", fmt.Errorf("lock artifactChartPath %q must be a .tgz archive under charts/", artifactPath)
	}
	return safeArtifactPath(root, artifactPath)
}

// RunMaterialsPlaceCharts places every approved chart and verifies every landed
// file. Pairing is per lock entry: the source must carry that entry's approved
// digest AND declare that entry's chart name and version, and it lands only at
// that entry's own artifactChartPath. A chart dropped into another chart's
// directory, an unapproved chart, an ambiguous digest, a shared source, a
// duplicate path and a missing approved chart all fail, and nothing is reported
// as placed without being re-read from its destination.
func RunMaterialsPlaceCharts(input MaterialsChartInput, stdout io.Writer) error {
	out := materialWriter(stdout)
	lock, err := LoadMaterialsLock(input.LockPath)
	if err != nil {
		return err
	}
	if len(lock.Charts) == 0 {
		return errors.New("the materials lock approves no charts; packaging would ship an artifact with no chart material")
	}
	if strings.TrimSpace(input.ChartsDir) == "" || strings.TrimSpace(input.ArtifactRoot) == "" {
		return errors.New("both --charts-dir and --artifact-root are required")
	}

	candidatesByDigest := map[string][]string{}
	if err := filepath.WalkDir(input.ChartsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".tgz") {
			return nil
		}
		digest, err := fileDigestHex(path)
		if err != nil {
			return err
		}
		candidatesByDigest[digest] = append(candidatesByDigest[digest], path)
		return nil
	}); err != nil {
		return errors.Wrapf(err, "walk the chart directory %s", input.ChartsDir)
	}
	if len(candidatesByDigest) == 0 {
		return fmt.Errorf("no .tgz chart archives found under %s", input.ChartsDir)
	}

	placed := map[string]bool{}
	used := map[string]bool{}
	for _, chart := range lock.Charts {
		where := fmt.Sprintf("lock chart %s %s", chart.Name, chart.ChartVersion)
		digest := strings.TrimPrefix(chart.SHA256, "sha256:")
		matches := candidatesByDigest[digest]
		if len(matches) == 0 {
			return fmt.Errorf("%s: no chart archive under %s carries the approved sha256 %s", where, input.ChartsDir, digest)
		}
		if len(matches) > 1 {
			sorted := append([]string(nil), matches...)
			sort.Strings(sorted)
			return fmt.Errorf("%s: the approved sha256 %s is carried by %d different archives (%s); an approved chart must be identifiable by one file",
				where, digest, len(matches), strings.Join(sorted, ", "))
		}
		source := matches[0]
		if used[source] {
			return fmt.Errorf("%s: chart archive %s is already placed for another lock entry; two approved entries may not share one archive", where, source)
		}
		// The archive must declare the very chart the entry approves: bytes alone
		// would let another chart's file sit in this chart's directory and be
		// packaged under this chart's path.
		name, version, err := readChartIdentity(source)
		if err != nil {
			return errors.Wrapf(err, "%s", where)
		}
		if name != chart.Name || version != chart.ChartVersion {
			return fmt.Errorf("%s: %s declares itself %s %s; an archive is only approved for the lock entry that matches its own name and version",
				where, source, name, version)
		}
		destination, err := artifactDestination(input.ArtifactRoot, chart.ArtifactPath)
		if err != nil {
			return errors.Wrapf(err, "%s", where)
		}
		if placed[destination] {
			return fmt.Errorf("%s: two lock entries place the same artifact path %s", where, chart.ArtifactPath)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return errors.Wrapf(err, "create the artifact chart directory for %s", chart.ArtifactPath)
		}
		if err := copyFileVerified(source, destination, digest, 0o644); err != nil {
			return errors.Wrapf(err, "%s", where)
		}
		used[source] = true
		placed[destination] = true
		fmt.Fprintf(out, "chart %s %s placed at %s and re-read: sha256 %s matches the lock\n",
			chart.Name, chart.ChartVersion, chart.ArtifactPath, digest)
	}

	if err := rejectExtraCharts(input.ArtifactRoot, placed); err != nil {
		return err
	}
	unapproved := []string{}
	for _, matches := range candidatesByDigest {
		for _, path := range matches {
			if !used[path] {
				unapproved = append(unapproved, path)
			}
		}
	}
	sort.Strings(unapproved)
	if len(unapproved) > 0 {
		return fmt.Errorf("%d chart archive(s) under %s are not the exact match of any lock entry: %s",
			len(unapproved), input.ChartsDir, strings.Join(unapproved, ", "))
	}
	fmt.Fprintf(out, "packaged %d approved chart archives, each bound to its own lock entry\n", len(placed))
	return nil
}

// copyFileMode copies a file, sets its mode, and returns the digest re-read from
// the destination.
func copyFileMode(source, destination string, mode os.FileMode) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", source)
	}
	if err := os.WriteFile(destination, data, mode); err != nil {
		return "", errors.Wrapf(err, "write %s", destination)
	}
	if err := os.Chmod(destination, mode); err != nil {
		return "", errors.Wrapf(err, "set the mode of %s", destination)
	}
	return fileDigestHex(destination)
}

// copyFileVerified copies a material and re-reads the destination, so a landed
// file is proven rather than assumed.
func copyFileVerified(source, destination, expectedSHA256 string, mode os.FileMode) error {
	landed, err := copyFileMode(source, destination, mode)
	if err != nil {
		return err
	}
	if landed != expectedSHA256 {
		return fmt.Errorf("%s was written as sha256 %s instead of the approved %s", destination, landed, expectedSHA256)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Fixed tool binaries.
// ---------------------------------------------------------------------------

// MaterialsToolInput describes one tool-placement run.
type MaterialsToolInput struct {
	LockPath     string
	ArtifactRoot string
	// Sources pairs a tool name with the binary that provides it, as
	// "name=/path". Every approved tool needs exactly one source, and no source
	// may name a tool the lock does not approve.
	Sources []string
	// Unapproved lists binaries the lock does not carry, as "name=/path". They
	// must be named here — packaging can never ship a bin/ file that nobody
	// declared — and each one is reported with its digest as NOT lock-approved,
	// so the artifact records exactly how little was verified about it.
	Unapproved []string
}

// RunMaterialsPlaceTools places every approved tool binary at its own
// artifactPath and verifies the landed bytes. The pairing is per entry: a
// digest taken from a different tool's line, a binary that does not match its
// own entry, a missing source and an unapproved extra file in bin/ all fail.
func RunMaterialsPlaceTools(input MaterialsToolInput, stdout io.Writer) error {
	out := materialWriter(stdout)
	lock, err := LoadMaterialsLock(input.LockPath)
	if err != nil {
		return err
	}
	if len(lock.Tools) == 0 {
		return errors.New("the materials lock approves no tools; packaging would ship an artifact with no fixed binaries")
	}
	if strings.TrimSpace(input.ArtifactRoot) == "" {
		return errors.New("--artifact-root is required")
	}
	byName := map[string]string{}
	for _, pair := range input.Sources {
		name, path, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(path) == "" {
			return fmt.Errorf("tool source %q is not a name=path pair", pair)
		}
		if previous, exists := byName[name]; exists {
			return fmt.Errorf("tool %q is given twice (%s and %s); one approved binary per tool", name, previous, path)
		}
		byName[name] = path
	}
	for name := range byName {
		known := false
		for _, tool := range lock.Tools {
			if tool.Name == name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("tool source %q names no tool in the materials lock", name)
		}
	}

	placed := map[string]bool{}
	for _, tool := range lock.Tools {
		where := fmt.Sprintf("lock tool %s %s", tool.Name, tool.Version)
		source, ok := byName[tool.Name]
		if !ok {
			return fmt.Errorf("%s: no --source %s=... was given, so the approved tool cannot be packaged", where, tool.Name)
		}
		digest := strings.TrimPrefix(tool.BinarySHA256, "sha256:")
		observed, err := fileDigestHex(source)
		if err != nil {
			return errors.Wrapf(err, "%s", where)
		}
		if observed != digest {
			return fmt.Errorf("%s: %s is sha256 %s but the lock approves %s; the matching entry was never checked",
				where, source, observed, digest)
		}
		destination, err := safeArtifactPath(input.ArtifactRoot, tool.ArtifactPath)
		if err != nil {
			return errors.Wrapf(err, "%s", where)
		}
		if placed[destination] {
			return fmt.Errorf("%s: two lock tool entries place the same artifact path %s", where, tool.ArtifactPath)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return errors.Wrapf(err, "create the artifact bin directory for %s", tool.ArtifactPath)
		}
		if err := copyFileVerified(source, destination, digest, 0o755); err != nil {
			return errors.Wrapf(err, "%s", where)
		}
		placed[destination] = true
		fmt.Fprintf(out, "tool %s %s placed at %s and re-read: sha256 %s matches the lock\n",
			tool.Name, tool.Version, tool.ArtifactPath, digest)
	}

	// A binary the lock does not carry may still be packaged, but only when the
	// caller names it: nothing lands in bin/ that nobody declared, and every such
	// file is reported as unapproved instead of silently looking verified.
	for _, pair := range input.Unapproved {
		name, path, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(path) == "" {
			return fmt.Errorf("unapproved bin %q is not a name=path pair", pair)
		}
		// The flag documents bin/<name>=/path, so the bin/ prefix is the place
		// the file goes, not part of its name: accepting both spellings keeps
		// one destination, and a name that still holds a separator is refused
		// rather than written somewhere else.
		name = strings.TrimPrefix(name, "bin/")
		if !filepath.IsLocal(name) || strings.Contains(name, "/") {
			return fmt.Errorf("unapproved bin name %q must be a single file name", name)
		}
		for _, tool := range lock.Tools {
			if tool.Name == name {
				return fmt.Errorf("bin %q is approved by the lock; pass it as --source %s=..., not as an unapproved bin", name, name)
			}
		}
		relative := "bin/" + name
		destination, err := safeArtifactPath(input.ArtifactRoot, relative)
		if err != nil {
			return errors.Wrapf(err, "unapproved bin %s", name)
		}
		if placed[destination] {
			return fmt.Errorf("bin %s is already placed by a lock entry", relative)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return errors.Wrapf(err, "create the artifact bin directory for %s", relative)
		}
		landed, err := copyFileMode(path, destination, 0o755)
		if err != nil {
			return errors.Wrapf(err, "unapproved bin %s", relative)
		}
		placed[destination] = true
		fmt.Fprintf(out, "bin %s placed WITHOUT lock approval: sha256 %s (the lock names no approval for it; recorded in materials-source.txt)\n",
			relative, landed)
	}

	extra := []string{}
	if err := filepath.WalkDir(filepath.Join(input.ArtifactRoot, "bin"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !placed[path] {
			extra = append(extra, path)
		}
		return nil
	}); err != nil {
		return errors.Wrapf(err, "scan the packaged binaries of %s", input.ArtifactRoot)
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("the packaged artifact carries %d bin file(s) no tool entry placed: %s",
			len(extra), strings.Join(extra, ", "))
	}
	fmt.Fprintf(out, "packaged %d approved tool binaries\n", len(placed))
	return nil
}

// rejectExtraCharts refuses any .tgz under the packaged charts tree that no lock
// entry placed.
func rejectExtraCharts(artifactRoot string, placed map[string]bool) error {
	extra := []string{}
	chartsRoot := filepath.Join(artifactRoot, "charts")
	if _, err := os.Stat(chartsRoot); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("the artifact carries no charts directory at %s", chartsRoot)
		}
		return errors.Wrapf(err, "stat %s", chartsRoot)
	}
	if err := filepath.WalkDir(chartsRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".tgz") {
			return nil
		}
		if !placed[path] {
			extra = append(extra, path)
		}
		return nil
	}); err != nil {
		return errors.Wrapf(err, "scan the packaged charts of %s", artifactRoot)
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("the packaged artifact carries %d chart archive(s) no lock entry placed: %s",
			len(extra), strings.Join(extra, ", "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Repository ISO nested inside the KubeKey artifact tarball.
// ---------------------------------------------------------------------------

// MaterialsISOInput describes the repository-ISO step: the loose copy the
// artifact ships, and the copy nested inside the KubeKey artifact tarball.
type MaterialsISOInput struct {
	// ArtifactRoot is the artifact output directory (the loose copy lives under it).
	ArtifactRoot string
	// ArtifactTar is the KubeKey artifact tarball (gzipped or a plain tar).
	ArtifactTar string
	// ISOFile is the approved repository ISO as it stands outside the artifact.
	ISOFile string
	// ChecksumsFile is the source-side record ("<file name> <sha256>" per line).
	// It lives outside the artifact, so regenerating the artifact's own
	// SHA256SUMS cannot make a wrong ISO acceptable.
	ChecksumsFile string
	// EntryPath is the path the installer expects inside the tarball.
	EntryPath string
	// LooseCopyPath, when set, is the artifact-relative path the ISO is also
	// copied to (the installer's repository directory).
	LooseCopyPath string
}

// readISORecord finds one file name's digest in the source-side record. The
// record is strict: two whitespace-separated fields and a 64-hex digest.
func readISORecord(checksumsFile, fileName string) (string, error) {
	data, err := os.ReadFile(checksumsFile)
	if err != nil {
		return "", errors.Wrapf(err, "read the ISO record %s", checksumsFile)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 2 {
			return "", fmt.Errorf("%s line %d is not exactly \"<file name> <sha256>\": %q", checksumsFile, index+1, line)
		}
		if fields[0] != fileName {
			continue
		}
		if !isFileHash(fields[1]) {
			return "", fmt.Errorf("%s line %d records %q for %s, which is not a sha256 digest",
				checksumsFile, index+1, fields[1], fileName)
		}
		return strings.TrimPrefix(fields[1], "sha256:"), nil
	}
	return "", fmt.Errorf("%s records no digest for %s; that ISO would ship with no approval", checksumsFile, fileName)
}

// RunMaterialsInjectRepositoryISO guarantees the artifact ships the approved
// repository ISO, both loose and nested inside the KubeKey artifact tarball. A
// file name proves nothing: the bytes are hashed against the source-side record,
// and only a matching ISO counts as already present. A missing or wrong ISO is
// written from the verified source file and the result is re-read.
func RunMaterialsInjectRepositoryISO(ctx context.Context, input MaterialsISOInput, stdout io.Writer) error {
	out := materialWriter(stdout)
	if strings.TrimSpace(input.ArtifactTar) == "" || strings.TrimSpace(input.ISOFile) == "" {
		return errors.New("both --artifact-tar and --iso are required")
	}
	name := filepath.Base(input.EntryPath)
	if input.EntryPath != "repository/"+name || !filepath.IsLocal(name) {
		return fmt.Errorf("the ISO entry path %q must be repository/<file name> with no traversal", input.EntryPath)
	}
	approved, err := readISORecord(input.ChecksumsFile, filepath.Base(input.ISOFile))
	if err != nil {
		return err
	}
	sourceSHA, err := fileDigestHex(input.ISOFile)
	if err != nil {
		return errors.Wrapf(err, "read the approved repository ISO %s", input.ISOFile)
	}
	if sourceSHA != approved {
		return fmt.Errorf("repository ISO %s is sha256 %s but the source-side record says %s: the approved material is not what would be packaged",
			input.ISOFile, sourceSHA, approved)
	}

	if strings.TrimSpace(input.LooseCopyPath) != "" {
		if strings.TrimSpace(input.ArtifactRoot) == "" {
			return errors.New("--artifact-root is required when --loose-copy is set")
		}
		loose, err := safeArtifactPath(input.ArtifactRoot, input.LooseCopyPath)
		if err != nil {
			return errors.Wrapf(err, "loose ISO copy %s", input.LooseCopyPath)
		}
		if filepath.Base(loose) != filepath.Base(input.ISOFile) {
			return fmt.Errorf("the loose ISO copy %s must keep the approved file name %s",
				input.LooseCopyPath, filepath.Base(input.ISOFile))
		}
		if err := os.MkdirAll(filepath.Dir(loose), 0o755); err != nil {
			return errors.Wrapf(err, "create the directory for %s", input.LooseCopyPath)
		}
		landed, err := copyFileMode(input.ISOFile, loose, 0o644)
		if err != nil {
			return errors.Wrapf(err, "copy the ISO to %s", input.LooseCopyPath)
		}
		if landed != approved {
			return fmt.Errorf("%s was written as sha256 %s instead of the approved %s", loose, landed, approved)
		}
		fmt.Fprintf(out, "repository ISO placed at %s and re-read: sha256 %s matches the record\n",
			input.LooseCopyPath, approved)
	}

	entries, err := tarEntriesFor(input.ArtifactTar, input.EntryPath)
	if err != nil {
		return errors.Wrapf(err, "inspect %s", input.ArtifactTar)
	}
	if len(entries) == 1 && entries[0].Digest == approved {
		fmt.Fprintf(out, "repository ISO already inside %s: exactly one entry, sha256 %s matches the approved record\n",
			input.ArtifactTar, approved)
		return nil
	}
	switch {
	case len(entries) == 0:
		fmt.Fprintf(out, "%s carries no %s entry; injecting the approved ISO\n", input.ArtifactTar, input.EntryPath)
	case len(entries) > 1:
		shadows := make([]string, 0, len(entries))
		for _, entry := range entries {
			shadows = append(shadows, fmt.Sprintf("%s=%s", entry.Name, shortDigest("sha256:"+entry.Digest)))
		}
		fmt.Fprintf(out, "%s carries %d entries for %s (%s); one file must be one entry, rewriting the tarball with only the approved ISO\n",
			input.ArtifactTar, len(entries), input.EntryPath, strings.Join(shadows, ", "))
	default:
		fmt.Fprintf(out, "the ISO inside %s is sha256 %s, not the approved %s; replacing that entry\n",
			input.ArtifactTar, entries[0].Digest, approved)
	}
	if err := rewriteTarballWithISO(ctx, input.ArtifactTar, input.ISOFile, input.EntryPath); err != nil {
		return err
	}
	entries, err = tarEntriesFor(input.ArtifactTar, input.EntryPath)
	if err != nil {
		return errors.Wrapf(err, "re-read %s", input.ArtifactTar)
	}
	found := len(entries) == 1
	var inside string
	if found {
		inside = entries[0].Digest
	}
	if !found || inside != approved {
		return fmt.Errorf("after injection %s still does not carry the approved ISO at %s (found %v, digest %q)",
			input.ArtifactTar, input.EntryPath, found, inside)
	}
	fmt.Fprintf(out, "verified: %s carries %s with sha256 %s\n", input.ArtifactTar, input.EntryPath, approved)
	return nil
}

// hashStream digests everything read from r.
func hashStream(r io.Reader) (string, error) {
	digester := sha256.New()
	if _, err := io.Copy(digester, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(digester.Sum(nil)), nil
}

// tarEntryDigest reads one entry of a (possibly gzipped) tarball and returns its
// content digest.
// tarEntry is one member of a tarball that names the same logical file.
type tarEntry struct {
	Name   string
	Digest string
}

// normalizeTarName folds the "./x" and "x" spellings tar writers mix together, so
// two entries for one file can never be mistaken for two different files.
func normalizeTarName(name string) string {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimPrefix(name, `.\`)
	return strings.TrimPrefix(name, `/`)
}

// tarEntriesFor reports every regular entry naming entryPath, hashing each one.
// Reporting all of them, not the first, is the point: a tarball that carries a
// second copy under the same path lets an extractor choose either one, so "the
// first entry is the approved ISO" proves nothing about what a node will unpack.
func tarEntriesFor(tarball, entryPath string) ([]tarEntry, error) {
	reader, closer, err := openTarball(tarball)
	if err != nil {
		return nil, err
	}
	defer closer()
	archive := tar.NewReader(reader)
	entries := []tarEntry{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		if normalizeTarName(header.Name) != normalizeTarName(entryPath) || header.Typeflag != tar.TypeReg {
			continue
		}
		digest, err := hashStream(archive)
		if err != nil {
			return nil, errors.Wrapf(err, "read %s from %s", header.Name, tarball)
		}
		entries = append(entries, tarEntry{Name: header.Name, Digest: digest})
	}
}

// tarEntryDigest reports the first entry naming a path, for callers that only
// need a single-content answer; the ISO step uses tarEntriesFor instead.
func tarEntryDigest(tarball, entryPath string) (string, bool, error) {
	reader, closer, err := openTarball(tarball)
	if err != nil {
		return "", false, err
	}
	defer closer()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if header.Name != entryPath || header.Typeflag != tar.TypeReg {
			continue
		}
		digest, err := hashStream(archive)
		if err != nil {
			return "", false, errors.Wrapf(err, "read %s from %s", entryPath, tarball)
		}
		return digest, true, nil
	}
}

// rewriteTarballWithISO streams every entry into a new tarball, dropping the old
// ISO entry and appending the approved ISO, then puts the result in place. The
// compression form of the input is preserved.
func rewriteTarballWithISO(ctx context.Context, tarball, isoFile, entryPath string) error {
	source, sourceCloser, err := openTarball(tarball)
	if err != nil {
		return err
	}
	defer sourceCloser()

	destinationPath := tarball + ".injected"
	destinationFile, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return errors.Wrapf(err, "create the rewritten tarball %s", destinationPath)
	}
	abort := func(cause error) error {
		destinationFile.Close()
		os.Remove(destinationPath)
		return cause
	}

	var gzipWriter *gzip.Writer
	var bodyWriter io.Writer = destinationFile
	if gzipped(tarball) {
		gzipWriter = gzip.NewWriter(destinationFile)
		bodyWriter = gzipWriter
	}
	archiveWriter := tar.NewWriter(bodyWriter)
	archiveReader := tar.NewReader(source)
	for {
		header, err := archiveReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return abort(errors.Wrapf(err, "read %s", tarball))
		}
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		if normalizeTarName(header.Name) == normalizeTarName(entryPath) {
			// Drop every spelling of this path, not just the one that matches
			// byte-for-byte: otherwise a shadow entry survives and an extractor
			// could still pick the ISO nobody verified.
			continue
		}
		if err := archiveWriter.WriteHeader(header); err != nil {
			return abort(errors.Wrapf(err, "write header for %s", header.Name))
		}
		if _, err := io.Copy(archiveWriter, archiveReader); err != nil {
			return abort(errors.Wrapf(err, "copy entry %s", header.Name))
		}
	}
	isoFileHandle, err := os.Open(isoFile)
	if err != nil {
		return abort(errors.Wrapf(err, "open the approved ISO %s", isoFile))
	}
	info, err := isoFileHandle.Stat()
	if err != nil {
		isoFileHandle.Close()
		return abort(errors.Wrapf(err, "stat the approved ISO %s", isoFile))
	}
	if err := archiveWriter.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     entryPath,
		Size:     info.Size(),
		Mode:     0o644,
		ModTime:  info.ModTime(),
		Format:   tar.FormatPAX,
	}); err != nil {
		isoFileHandle.Close()
		return abort(errors.Wrapf(err, "write the ISO entry header"))
	}
	if _, err := io.Copy(archiveWriter, isoFileHandle); err != nil {
		isoFileHandle.Close()
		return abort(errors.Wrapf(err, "write the ISO entry"))
	}
	isoFileHandle.Close()
	if err := archiveWriter.Close(); err != nil {
		return abort(errors.Wrap(err, "close the rewritten tar stream"))
	}
	if gzipWriter != nil {
		if err := gzipWriter.Close(); err != nil {
			return abort(errors.Wrap(err, "close the rewritten gzip stream"))
		}
	}
	if err := destinationFile.Close(); err != nil {
		return abort(errors.Wrap(err, "close the rewritten tarball"))
	}
	if err := os.Rename(destinationPath, tarball); err != nil {
		return abort(errors.Wrapf(err, "replace %s with the rewritten tarball", tarball))
	}
	return nil
}

// openTarball opens a tarball, transparently unzipping it when it is gzipped.
func openTarball(path string) (io.Reader, func(), error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "open %s", path)
	}
	if !gzipped(path) {
		return file, func() { file.Close() }, nil
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, nil, errors.Wrapf(err, "read %s as a gzipped tarball", path)
	}
	return reader, func() { reader.Close(); file.Close() }, nil
}

// gzipped sniffs the first two bytes for the gzip magic number.
func gzipped(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	var magic [2]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return false
	}
	return magic[0] == 0x1f && magic[1] == 0x8b
}

// ---------------------------------------------------------------------------
// The packaged image store, checked through the install's own content gate.
// ---------------------------------------------------------------------------

// MaterialsRegistryVerifyInput describes one verification of a packaged image
// store, served locally by the same command the installer runs on the node.
type MaterialsRegistryVerifyInput struct {
	RegistryAddress string
	ImagesTSVPath   string
	LockPath        string
	// EvidenceDirs are the roots holding the approved source manifests, so a
	// package whose object is the pinned index's amd64 child (or an approved
	// media-type re-description of the pinned bytes) can be verified offline.
	EvidenceDirs []string
	// VerifyBlobBytes makes the gate download and hash every config/layer blob
	// instead of only asking whether the registry has one under that name.
	VerifyBlobBytes bool
}

// RunMaterialsVerifyRegistry reads every images.tsv row out of the served store
// and applies the install's content gate to it. Packaging therefore cannot
// finish with an archive that only looks complete: each manifest is resolved to
// this platform, every referenced blob has to answer, and the digests must match
// both the table pin and the approved lock.
func RunMaterialsVerifyRegistry(ctx context.Context, input MaterialsRegistryVerifyInput, stdout io.Writer) error {
	if strings.TrimSpace(input.RegistryAddress) == "" {
		return errors.New("--registry-address is required")
	}
	data, err := os.ReadFile(input.ImagesTSVPath)
	if err != nil {
		return errors.Wrapf(err, "read the image table %s", input.ImagesTSVPath)
	}
	table, err := LoadImageTable(strings.Split(string(data), "\n"))
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return fmt.Errorf("the image table %s has no rows", input.ImagesTSVPath)
	}
	lock, err := LoadMaterialsLock(input.LockPath)
	if err != nil {
		return err
	}
	// The table and the lock must describe the same approved images before a single
	// row is trusted: a spelling drift lets a locked image be approved as if the
	// lock said nothing about it.
	if err := lock.VerifyImageTableAgainstLock(table); err != nil {
		return err
	}
	if err := refuseLockNamedOtherReference(lock, table); err != nil {
		return err
	}
	client := &http.Client{Timeout: 90 * time.Second}
	gate := NewRegistryContentChecker(client, input.RegistryAddress, lock, input.EvidenceDirs, input.VerifyBlobBytes)
	originals := make([]string, 0, len(table))
	for original := range table {
		originals = append(originals, original)
	}
	sort.Strings(originals)
	out := materialWriter(stdout)
	verdicts := map[string]int{}
	for _, original := range originals {
		approval, err := gate.Verify(ctx, table[original])
		if err != nil {
			return errors.Wrapf(err, "packaged image %s", original)
		}
		verdicts[verdictClass(approval)]++
		fmt.Fprintf(out, "packaged image %s: %s\n", original, approval)
	}
	total := 0
	for _, count := range verdicts {
		total += count
	}
	if total != len(table) {
		return fmt.Errorf("the verdict tally covers %d of %d rows; every row must report exactly one verdict", total, len(table))
	}
	fmt.Fprintf(out, "packaged image store verified: %d image(s) = %s (rule %s)\n",
		len(table), formatVerdicts(verdicts), imageLandingRuleVersion)
	return nil
}

// verdictClass names the one way a row was approved, so the report can never
// count a row twice or leave one unreported.
func verdictClass(approval string) string {
	switch {
	case strings.Contains(approval, "landing platform-selected"):
		return "platform-selected from the pinned index"
	case strings.Contains(approval, "landing converted"):
		return "converted under the landing rule"
	case strings.HasPrefix(approval, "approved materials lock and images.tsv pin"):
		return "byte-exact with lock and pin"
	case strings.Contains(approval, "no materials lock available"):
		return "byte-exact by pin, no lock available"
	default:
		return "byte-exact by pin only"
	}
}

func formatVerdicts(verdicts map[string]int) string {
	keys := make([]string, 0, len(verdicts))
	for key := range verdicts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", verdicts[key], key))
	}
	return strings.Join(parts, ", ")
}

// refuseLockNamedOtherReference catches the drift VerifyImageTableAgainstLock
// cannot see: a lock entry that names the same repository:tag under a different
// original reference, which would leave the row looking unapproved while the
// package is approved against some other spelling.
func refuseLockNamedOtherReference(lock *MaterialsLock, table ImageTable) error {
	byHaulerRef := map[string]Image{}
	for _, row := range table {
		byHaulerRef[row.HaulerRef] = row
	}
	for _, entry := range lock.Images {
		if entry.Disabled {
			continue
		}
		row, ok := byHaulerRef[entry.HaulerRef]
		if !ok {
			continue
		}
		if row.Original != entry.Original {
			return fmt.Errorf("locked image %q approves hauler_ref %q, which images.tsv gives to %q; the table and the lock disagree about one packaged object",
				entry.Original, entry.HaulerRef, row.Original)
		}
	}
	return nil
}

// MaterialsEvidenceInput records the approved source manifests for an image
// table, so the packaged store can be verified against the original bytes.
type MaterialsEvidenceInput struct {
	ImagesTSVPath string
	// Sources are content-addressed roots (a preserved store, an artifact's
	// evidence directory, or plain <digest>.json files) holding the approved
	// manifests. They are searched, never trusted by file name.
	Sources []string
	// Out receives one file per approved object, named by its own digest.
	Out string
	// LockPath, when set, cross-checks every recorded pair against the lock.
	LockPath string
}

// RunMaterialsRecordEvidence copies the bytes a pin names into the package's
// evidence directory and writes the source -> linux/amd64 relationship next to
// them. Every byte it stores is hashed on the way in and on the way out, and a
// row whose approved bytes cannot be produced fails the whole step: evidence that
// silently covers 47 of 50 rows would just move the guesswork later.
//
// The records file is an operator report. The gate never reads it: it re-derives
// everything from the bytes and the pin, so a hand-edited "pass" line here
// approves nothing.
func RunMaterialsRecordEvidence(input MaterialsEvidenceInput, stdout io.Writer) error {
	if strings.TrimSpace(input.ImagesTSVPath) == "" || strings.TrimSpace(input.Out) == "" {
		return errors.New("--images-tsv and --out are required")
	}
	data, err := os.ReadFile(input.ImagesTSVPath)
	if err != nil {
		return errors.Wrapf(err, "read the image table %s", input.ImagesTSVPath)
	}
	table, err := LoadImageTable(strings.Split(string(data), "\n"))
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return fmt.Errorf("the image table %s has no rows", input.ImagesTSVPath)
	}
	var lock *MaterialsLock
	if strings.TrimSpace(input.LockPath) != "" {
		lock, err = LoadMaterialsLock(input.LockPath)
		if err != nil {
			return err
		}
	}
	out := materialWriter(stdout)
	evidence := ImageEvidence{Roots: input.Sources}
	if err := os.MkdirAll(input.Out, 0o755); err != nil {
		return errors.Wrapf(err, "create the evidence directory %s", input.Out)
	}
	originals := make([]string, 0, len(table))
	for original := range table {
		originals = append(originals, original)
	}
	sort.Strings(originals)
	lines := []string{"original_ref\toriginal_pin\tpin_kind\tplatform_digest\tplatform_bytes\tevidence_files\trule"}
	recorded := 0
	for _, original := range originals {
		row := table[original]
		approved, err := evidence.resolveApproved(row)
		if err != nil {
			return errors.Wrapf(err, "record evidence for %s", original)
		}
		stored := []string{}
		name, writeErr := writeEvidence(input.Out, row.Digest, approved.PinBytes)
		if writeErr != nil {
			return errors.Wrapf(writeErr, "image %s: store the pinned manifest", original)
		}
		stored = append(stored, name)
		if approved.PinIsIndex {
			name, writeErr := writeEvidence(input.Out, approved.PlatformDigest, approved.PlatformBytes)
			if writeErr != nil {
				return errors.Wrapf(writeErr, "image %s: store the amd64 manifest the pinned index points at", original)
			}
			stored = append(stored, name)
		}
		if lock != nil {
			if entry, locked := lock.ImageByOriginal(original); locked {
				if err := checkLockAgainstApproved(row, approved, *entry, false); err != nil {
					return err
				}
			}
		}
		pinKind := "manifest"
		if approved.PinIsIndex {
			pinKind = "index"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\t%s",
			original, row.Digest, pinKind, approved.PlatformDigest, len(approved.PlatformBytes),
			strings.Join(stored, ","), imageLandingRuleVersion))
		recorded++
		fmt.Fprintf(out, "evidence %s: %s pin %s, platform %s stored as %s\n",
			original, pinKind, shortDigest(row.Digest), shortDigest(approved.PlatformDigest), strings.Join(stored, " + "))
	}
	if err := os.WriteFile(filepath.Join(input.Out, "records.tsv"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return errors.Wrap(err, "write the evidence records")
	}
	fmt.Fprintf(out, "recorded approved source evidence for %d/%d image rows under %s (rule %s)\n",
		recorded, len(table), input.Out, imageLandingRuleVersion)
	return nil
}

// writeEvidence stores bytes under their own digest and re-hashes the landed file,
// so a partial or tampered write cannot pass as approved evidence.
func writeEvidence(dir, digest string, body []byte) (string, error) {
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if len(hexDigest) != 64 {
		return "", errors.Errorf("refusing to store evidence under a non-digest name %q", digest)
	}
	name := hexDigest + ".json"
	path := filepath.Join(dir, name)
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hexDigest {
		return "", errors.Errorf("bytes offered as %s hash to sha256:%s", digest, hex.EncodeToString(sum[:]))
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	landed, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if string(landed) != string(body) {
		return "", errors.Errorf("%s did not land byte-for-byte", path)
	}
	return name, nil
}
