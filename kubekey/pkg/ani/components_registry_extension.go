package ani

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Distribution's pinned filesystem backend exposes new content immediately.
// All writes are exclusive additions; its existing readonly process, unit,
// store, tags and resources are left in place. The layout is the one shipped by
// the approved Hauler v2.0.3, and tested through that binary's actual HTTP API.
type registryAppendFile struct {
	path, source, digest string
	data                 []byte
}

func validateLWSAdditionMaterial(packageRoot string, lock *MaterialsLock) error {
	chart, ok := lock.ChartByArtifactPath(LWSChartPath)
	if !ok || chart.Name != "lws" || chart.ChartVersion != LWSChartVersion || strings.TrimPrefix(chart.SHA256, "sha256:") != LWSChartSHA256 {
		return fmt.Errorf("LWS addition requires the official source-approved chart")
	}
	path := filepath.Join(packageRoot, LWSChartPath)
	if sha256FileHex(path) != LWSChartSHA256 {
		return fmt.Errorf("LWS chart %s does not carry the source-approved digest", path)
	}
	return verifyChartIdentity(path, *chart)
}

func approvedAdditionImage(component string, row Image) bool {
	if component == "higress" {
		approved, ok := higressImageApproval[row.Original]
		return ok && row.Digest == approved.platform
	}
	return (component == "lws" && row.Original == LWSImageReference && row.Digest == LWSAMD64ManifestDigest) ||
		(component == "kserve" && row.Original == KServeLLMImageReference && row.Digest == KServeLLMAMD64ManifestDigest)
}

func validateAdditionMaterial(component, packageRoot string, lock *MaterialsLock) error {
	if component == "higress" {
		if err := verifyHigressAssets(packageRoot); err != nil {
			return err
		}
		chart, ok := lock.ChartByArtifactPath(HigressChartPath)
		if !ok || chart.Name != "higress" || chart.ChartVersion != HigressVersion || strings.TrimPrefix(chart.SHA256, "sha256:") != HigressChartSHA256 || sha256FileHex(filepath.Join(packageRoot, HigressChartPath)) != HigressChartSHA256 {
			return fmt.Errorf("Higress requires the source-approved official Chart")
		}
		if err := verifyChartIdentity(filepath.Join(packageRoot, HigressChartPath), *chart); err != nil {
			return err
		}
		if err := verifyHigressWasmArtifact(packageRoot); err != nil {
			return err
		}
		for original, pin := range higressImageApproval {
			approved, err := (ImageEvidence{Roots: EvidenceRoots(packageRoot)}).resolveApproved(Image{Original: original, Digest: pin.index})
			if err != nil || approved.Platform() != pin.platform {
				return fmt.Errorf("Higress original index evidence differs: %s: %v", original, err)
			}
		}
		return nil
	}
	if component == "lws" {
		return validateLWSAdditionMaterial(packageRoot, lock)
	}
	if component != "kserve" {
		return fmt.Errorf("component %s has no approved additive material path", component)
	}
	approved, err := (ImageEvidence{Roots: EvidenceRoots(packageRoot)}).resolveApproved(Image{Original: KServeLLMImageReference, Digest: KServeLLMSourceIndexDigest})
	if err != nil || approved.Platform() != KServeLLMAMD64ManifestDigest {
		return fmt.Errorf("LLMI source index evidence does not select the approved amd64 manifest: %v", err)
	}
	for _, expected := range []struct{ path, name, digest string }{
		{KServeLLMCRDChartPath, "kserve-llmisvc-crd", KServeLLMCRDChartSHA256},
		{KServeLLMChartPath, "kserve-llmisvc-resources", KServeLLMChartSHA256},
	} {
		chart, ok := lock.ChartByArtifactPath(expected.path)
		if !ok || chart.Name != expected.name || chart.ChartVersion != KServeLLMVersion || strings.TrimPrefix(chart.SHA256, "sha256:") != expected.digest {
			return fmt.Errorf("LLMI addition requires source-approved official Chart %s", expected.name)
		}
		path := filepath.Join(packageRoot, expected.path)
		if sha256FileHex(path) != expected.digest {
			return fmt.Errorf("LLMI Chart differs from source approval: %s", path)
		}
		if err := verifyChartIdentity(path, *chart); err != nil {
			return err
		}
	}
	return nil
}

func componentRegistryFiles(store, dataRoot string, row Image, lock *MaterialsLock) ([]registryAppendFile, error) {
	entry, ok := lock.ImageByOriginal(row.Original)
	if !ok || entry.Disabled || entry.HaulerRef != row.HaulerRef {
		return nil, fmt.Errorf("no approval for component image %s", row.Original)
	}
	if err := checkRepositoryTag(row.HaulerRef); err != nil {
		return nil, err
	}
	_, repo, tag, err := SplitReference(row.HaulerRef)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(repo) || filepath.Clean(repo) != repo || strings.ContainsAny(repo+tag, "\\ \t\r\n") || strings.Contains(tag, "/") || tag == "." || tag == ".." {
		return nil, fmt.Errorf("unsafe registry reference %s", row.HaulerRef)
	}
	for _, segment := range strings.Split(repo, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return nil, fmt.Errorf("unsafe repository %s", repo)
		}
	}
	index, err := readStoreIndex(store)
	if err != nil {
		return nil, err
	}
	matches := 0
	for _, desc := range index.Manifests {
		if desc.Annotations[ociRefNameAnnotation] == repo+":"+tag {
			if desc.Digest != entry.PlatformDigest {
				return nil, fmt.Errorf("component store tag is not the approved platform manifest")
			}
			matches++
		}
	}
	if matches != 1 {
		return nil, fmt.Errorf("component store must name exactly one approved tag %s:%s", repo, tag)
	}
	manifestPath, err := findBlob(entry.PlatformDigest, []string{store})
	if err != nil {
		return nil, err
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	approved := approvedObject{PlatformDigest: entry.PlatformDigest, PlatformBytes: manifest}
	if err := checkLockAgainstApproved(row, approved, *entry, false); err != nil {
		return nil, err
	}
	parsed, err := ParseServedImageManifest(manifest)
	if err != nil || parsed.IsIndex {
		return nil, fmt.Errorf("component image must carry its approved platform manifest: %v", err)
	}
	configPath, err := findBlob(parsed.ConfigDigest, []string{store})
	if err != nil {
		return nil, err
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	if err := json.Unmarshal(configBytes, &platform); err != nil {
		return nil, err
	}
	if platform.OS != "linux" || platform.Architecture != "amd64" {
		return nil, fmt.Errorf("component config is not linux/amd64")
	}
	digests := append([]string{entry.PlatformDigest, parsed.ConfigDigest}, parsed.LayerDigests...)
	base := filepath.Join(dataRoot, "docker", "registry", "v2")
	files := []registryAppendFile{}
	link := func(rel, digest string) {
		files = append(files, registryAppendFile{path: filepath.Join(base, rel), data: []byte(digest)})
	}
	for _, digest := range digests {
		if !isSHA256Digest(digest) {
			return nil, fmt.Errorf("malformed component digest %s", digest)
		}
		source, err := findBlob(digest, []string{store})
		if err != nil {
			return nil, err
		}
		hexDigest := strings.TrimPrefix(digest, "sha256:")
		files = append(files, registryAppendFile{path: filepath.Join(base, "blobs", "sha256", hexDigest[:2], hexDigest, "data"), source: source, digest: digest})
		if digest != entry.PlatformDigest {
			link(filepath.Join("repositories", repo, "_layers", "sha256", hexDigest, "link"), digest)
		}
	}
	hexDigest := strings.TrimPrefix(entry.PlatformDigest, "sha256:")
	link(filepath.Join("repositories", repo, "_manifests", "revisions", "sha256", hexDigest, "link"), entry.PlatformDigest)
	link(filepath.Join("repositories", repo, "_manifests", "tags", tag, "index", "sha256", hexDigest, "link"), entry.PlatformDigest)
	// Publish the tag last, only after every blob and repository link exists.
	link(filepath.Join("repositories", repo, "_manifests", "tags", tag, "current", "link"), entry.PlatformDigest)
	for _, file := range files {
		if err := checkRegistryAppendFile(file); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func checkRegistryAppendFile(file registryAppendFile) error {
	// No existing symlink may redirect either content or repository links.
	for path := file.path; path != "/" && path != "."; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("registry append refuses symlink %s", path)
		}
		if path != file.path && !info.IsDir() {
			return fmt.Errorf("registry parent is not a directory: %s", path)
		}
	}
	info, err := os.Lstat(file.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("registry file is not regular: %s", file.path)
	}
	if file.digest != "" {
		if !blobPresent(file.path, file.digest) {
			return fmt.Errorf("existing registry blob conflicts: %s", file.path)
		}
	} else {
		data, err := os.ReadFile(file.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, file.data) {
			return fmt.Errorf("existing registry reference conflicts: %s", file.path)
		}
	}
	return nil
}

func installRegistryAppendFiles(files []registryAppendFile) error {
	for _, file := range files {
		if err := checkRegistryAppendFile(file); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := checkRegistryAppendFile(file); err != nil {
			return err
		}
		if _, err := os.Lstat(file.path); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file.path), 0755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(file.path), ".ani-component-append-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		writeErr := func() error {
			if file.source != "" {
				src, err := os.Open(file.source)
				if err != nil {
					return err
				}
				defer src.Close()
				hash := sha256.New()
				if _, err := io.Copy(io.MultiWriter(tmp, hash), src); err != nil {
					return err
				}
				if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != file.digest {
					return fmt.Errorf("source blob changed before registry append: %s", file.source)
				}
			} else {
				if _, err := tmp.Write(file.data); err != nil {
					return err
				}
			}
			if err := tmp.Chmod(0644); err != nil {
				return err
			}
			if err := tmp.Sync(); err != nil {
				return err
			}
			if err := tmp.Close(); err != nil {
				return err
			}
			if err := os.Link(name, file.path); err != nil {
				if os.IsExist(err) {
					return checkRegistryAppendFile(file)
				}
				return err
			}
			return nil
		}()
		tmp.Close()
		os.Remove(name)
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

func componentRegistryAppendTarget(cluster ClusterConfig, base RunManifest) (string, error) {
	dataRoot := filepath.Join(runtimeBaseDir, cluster.Name, "work", "registry-data")
	store := filepath.Join(runtimeBaseDir, cluster.Name, "work", "hauler-store")
	unit, err := os.ReadFile(filepath.Join(systemdUnitDir, serviceUnitName))
	if err != nil {
		return "", err
	}
	expected := fmt.Sprintf("ExecStart=%s store serve registry --port %d --directory %s --readonly=true --store %s", filepath.Join(base.PackageRoot, "bin", "hauler"), cluster.RegistryConfig.Port, dataRoot, store)
	for _, line := range strings.Split(string(unit), "\n") {
		if line == expected {
			return dataRoot, nil
		}
	}
	return "", fmt.Errorf("existing registry unit does not match original readonly service; refusing material additions")
}

func publishMissingComponentImages(ctx context.Context, cluster ClusterConfig, base RunManifest, packageRoot string, lock *MaterialsLock, scope []string) error {
	registry, err := cluster.RegistryAddress()
	if err != nil {
		return err
	}
	table, err := LoadImageTable(strings.Split(readImageTableOrEmpty(packageRoot), "\n"))
	if err != nil {
		return err
	}
	gate := NewRegistryContentChecker(&http.Client{Timeout: registryContentRequestTimeout}, registry, lock, EvidenceRoots(packageRoot), true)
	var files []registryAppendFile
	for _, component := range scope {
		for _, key := range imagesForComponent(cluster, component) {
			row := table[key.Original]
			if _, err := gate.Verify(ctx, row); err == nil {
				continue
			} else if !absentInRegistry(err) {
				return err
			}
			dataRoot, err := componentRegistryAppendTarget(cluster, base)
			if err != nil {
				return err
			}
			add, err := componentRegistryFiles(filepath.Join(packageRoot, "images", "component-stores", component, "store"), dataRoot, row, lock)
			if err != nil {
				return err
			}
			files = append(files, add...)
		}
	}
	return installRegistryAppendFiles(files)
}
