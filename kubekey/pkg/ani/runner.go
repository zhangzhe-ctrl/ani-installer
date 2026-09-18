package ani

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"gopkg.in/yaml.v3"
)

// InstallInput identifies the fixed artifact and the site-specific cluster
// input. PackageRoot retains the CLI spelling for compatibility, but it is the
// root of the independently released artifact.
type InstallInput struct {
	ConfigFile  string
	PackageRoot string
}

const (
	serviceUnitName = "ani-image-registry.service"
	runtimeBaseDir  = "/var/lib/ani-installer"
)

type installPaths struct {
	ArtifactRoot   string
	RuntimeRoot    string
	WorkRoot       string
	LogRoot        string
	ArtifactPath   string
	ArchivePath    string
	HaulerPath     string
	ImageTablePath string
	RepositoryISO  string
	StoreDir       string
	RegistryDir    string
	KubeKeyWorkdir string
}

func resolveInstallPaths(artifactRoot string, c ClusterConfig) (installPaths, error) {
	if err := Validate(c); err != nil {
		return installPaths{}, err
	}
	root, err := filepath.Abs(artifactRoot)
	if err != nil {
		return installPaths{}, errors.Wrap(err, "resolve artifact root")
	}
	if root == "" {
		return installPaths{}, errors.New("artifact root is required")
	}
	if strings.ContainsAny(root, " \t") {
		return installPaths{}, errors.New("artifact root must not contain spaces (systemd unit safety)")
	}

	runtimeRoot := filepath.Join(runtimeBaseDir, c.Name)
	return installPaths{
		ArtifactRoot:   root,
		RuntimeRoot:    runtimeRoot,
		WorkRoot:       filepath.Join(runtimeRoot, "work"),
		LogRoot:        filepath.Join(runtimeRoot, "logs"),
		ArtifactPath:   filepath.Join(root, "packages", "kubekey-artifact.tgz"),
		ArchivePath:    filepath.Join(root, "images", "images.haul.tar.zst"),
		HaulerPath:     filepath.Join(root, "bin", "hauler"),
		ImageTablePath: filepath.Join(root, "images", "images.tsv"),
		RepositoryISO:  filepath.Join(root, "repository", "ubuntu-24.04-debs-amd64.iso"),
		StoreDir:       filepath.Join(runtimeRoot, "work", "hauler-store"),
		RegistryDir:    filepath.Join(runtimeRoot, "work", "registry-data"),
		KubeKeyWorkdir: filepath.Join(runtimeRoot, "work", "kubekey"),
	}, nil
}

func createRuntimeRoot(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.Wrapf(err, "create runtime parent %s", filepath.Dir(path))
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.Wrapf(err, "runtime root %s already exists; restore the clean snapshot before a new install", path)
		}
		return errors.Wrapf(err, "create runtime root %s", path)
	}
	return nil
}

func RunInstall(ctx context.Context, input InstallInput) error {
	if os.Geteuid() != 0 {
		return errors.New("kk ani install must run as root; use install.sh or sudo")
	}
	configPath, err := filepath.Abs(input.ConfigFile)
	if err != nil {
		return errors.Wrap(err, "resolve cluster config")
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return errors.Wrapf(err, "read cluster config %s", configPath)
	}
	cluster, err := LoadClusterConfig(configPath)
	if err != nil {
		return err
	}
	if err := Validate(cluster); err != nil {
		return errors.Wrap(err, "validate cluster config")
	}
	paths, err := resolveInstallPaths(input.PackageRoot, cluster)
	if err != nil {
		return err
	}
	kkPath, err := os.Executable()
	if err != nil {
		return errors.Wrap(err, "locate kk executable")
	}
	if err := VerifyInstallerInterface(cluster); err != nil {
		return err
	}
	if err := VerifySSHAuth(cluster); err != nil {
		return err
	}

	required := []string{
		paths.ArtifactPath,
		paths.ArchivePath,
		paths.HaulerPath,
		paths.ImageTablePath,
		paths.RepositoryISO,
		filepath.Join(paths.ArtifactRoot, "SHA256SUMS"),
		filepath.Join(paths.ArtifactRoot, "config", "package.yaml"),
		filepath.Join(paths.ArtifactRoot, "config", "versions.yaml"),
		filepath.Join(paths.ArtifactRoot, "config", "runtime-checksums.txt"),
		filepath.Join(paths.ArtifactRoot, "config", "repository-iso-checksums.txt"),
		filepath.Join(paths.ArtifactRoot, "config", "components.lock.yaml"),
		kkPath,
	}
	// Every enabled component must have its fixed Chart material in the artifact
	// before deployment starts, so a missing chart fails here and not mid-install.
	selection := map[string]bool{}
	for _, row := range cluster.Components.Selection() {
		selection[row.Name] = row.Enabled
	}
	for name, relative := range componentChartMaterials {
		if selection[name] {
			required = append(required, filepath.Join(paths.ArtifactRoot, filepath.FromSlash(relative)))
		}
	}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return errors.Wrapf(err, "required artifact file %s", path)
		}
	}

	if err := createRuntimeRoot(paths.RuntimeRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.WorkRoot, 0o700); err != nil {
		return errors.Wrap(err, "create work directory")
	}
	if err := os.MkdirAll(paths.LogRoot, 0o700); err != nil {
		return errors.Wrap(err, "create log directory")
	}
	if err := writeComponentSelection(filepath.Join(paths.WorkRoot, "components-selection.tsv"), configSHA256(configData), cluster.Components.Selection()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.LogRoot, "install.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrap(err, "open install log")
	}
	defer logFile.Close()
	logger := newInstallLogger(logFile)
	fmt.Fprintf(logger, "ANI install started at %s; artifact=%s kk=%s config=%s runtime=%s\n",
		time.Now().Format(time.RFC3339), paths.ArtifactRoot, kkPath, configPath, paths.RuntimeRoot)

	if err := verifyArtifactChecksums(ctx, logger, paths.ArtifactRoot); err != nil {
		return err
	}
	imageRows, err := readLines(paths.ImageTablePath)
	if err != nil {
		return errors.Wrap(err, "read images.tsv")
	}
	imageTable, err := LoadImageTable(imageRows)
	if err != nil {
		return err
	}
	fmt.Fprintf(logger, "loaded %d images from images.tsv\n", len(imageTable))

	spec, err := KubeKeyConfig(cluster, paths.ArtifactPath, paths.ArtifactRoot, imageTable)
	if err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(paths.WorkRoot, "config.yaml"), map[string]any{
		"apiVersion": "kubekey.kubesphere.io/v1",
		"kind":       "Config",
		"spec":       spec,
	}); err != nil {
		return err
	}
	inventorySpec, err := KubeKeyInventory(cluster)
	if err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(paths.WorkRoot, "inventory.yaml"), map[string]any{
		"apiVersion": "kubekey.kubesphere.io/v1",
		"kind":       "Inventory",
		"metadata":   map[string]any{"name": "default"},
		"spec":       inventorySpec,
	}); err != nil {
		return err
	}

	registryAddress, err := cluster.RegistryAddress()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.StoreDir, 0o700); err != nil {
		return errors.Wrap(err, "create Hauler store")
	}
	if err := os.MkdirAll(paths.RegistryDir, 0o700); err != nil {
		return errors.Wrap(err, "create registry backend")
	}
	if err := runLogged(ctx, logger, paths.HaulerPath, "store", "load", "--filename", paths.ArchivePath, "--store", paths.StoreDir); err != nil {
		return errors.Wrap(err, "load Hauler archive")
	}
	if err := writeRegistryService(paths.WorkRoot, paths.HaulerPath, paths.StoreDir, paths.RegistryDir, cluster.RegistryConfig.Port); err != nil {
		return errors.Wrap(err, "write Hauler service")
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"restart", serviceUnitName},
	} {
		if err := runLogged(ctx, logger, "systemctl", args...); err != nil {
			return errors.Wrapf(err, "systemctl %v", args)
		}
	}
	if err := waitRegistry(ctx, logger, registryAddress); err != nil {
		return err
	}
	if err := verifyRegistryImages(ctx, logger, registryAddress, imageTable); err != nil {
		return err
	}
	fmt.Fprintf(logger, "Hauler registry %s is ready and all %d image manifests are present\n", registryAddress, len(imageTable))

	if err := os.MkdirAll(paths.KubeKeyWorkdir, 0o700); err != nil {
		return errors.Wrap(err, "create KubeKey workdir")
	}
	fmt.Fprintf(logger, "starting KubeKey create cluster\n")
	err = runKubeKeyLogged(ctx, logger, kkPath,
		"create", "cluster",
		"--inventory", filepath.Join(paths.WorkRoot, "inventory.yaml"),
		"--config", filepath.Join(paths.WorkRoot, "config.yaml"),
		"--artifact", paths.ArtifactPath,
		"--workdir", paths.KubeKeyWorkdir,
	)
	if err != nil {
		fmt.Fprintf(logger, "KubeKey failed: %v\n", err)
		return errors.Wrap(err, "KubeKey create cluster")
	}
	fmt.Fprintf(logger, "ANI install completed at %s\n", time.Now().Format(time.RFC3339))
	return nil
}

// componentChartMaterials maps a component to the fixed Chart path inside the
// offline artifact. Paths are relative so the artifact can be relocated.
var componentChartMaterials = map[string]string{
	"cert-manager": "charts/cert-manager/v1.21.2.tgz",
}

// writeComponentSelection records this run's effective component selection for
// verify.sh. The first line pins this exact site config; every supported
// component follows in the fixed order so a missing, duplicated or malformed
// file can never be read as "everything disabled".
func writeComponentSelection(path, configSHA string, rows []ComponentRow) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# config_sha256=%s\n", configSHA)
	for _, row := range rows {
		fmt.Fprintf(&b, "%s\t%t\n", row.Name, row.Enabled)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return errors.Wrapf(err, "write component selection %s", path)
	}
	return nil
}

func configSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func verifyArtifactChecksums(ctx context.Context, logger io.Writer, artifactRoot string) error {
	cmd := exec.CommandContext(ctx, "sha256sum", "--check", "--quiet", "SHA256SUMS")
	cmd.Dir = artifactRoot
	cmd.Stdout = logger
	cmd.Stderr = logger
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "verify artifact checksums in %s", artifactRoot)
	}
	return nil
}

func writeYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return errors.Wrap(err, "marshal YAML")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errors.Wrapf(err, "write %s", path)
	}
	return nil
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines, nil
}

type installLogger struct {
	io.Writer
	file io.Writer
}

func newInstallLogger(file io.Writer) installLogger {
	return installLogger{Writer: io.MultiWriter(file, os.Stdout), file: file}
}

func (l installLogger) Write(p []byte) (int, error) {
	return l.Writer.Write(p)
}

func runLogged(ctx context.Context, logger io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = logger
	cmd.Stderr = logger
	return cmd.Run()
}

// Installation must not select a caller's kubeconfig through HOME or KUBECONFIG.
// kubeadm creates this file before any Kubernetes API tasks run.
func runKubeKeyLogged(ctx context.Context, logger io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG=/etc/kubernetes/admin.conf")
	cmd.Stdout = logger
	cmd.Stderr = logger
	return cmd.Run()
}

func writeRegistryService(runtimeWorkRoot, haulerPath, storeDir, registryDir string, port int) error {
	if port <= 0 || port > 65535 {
		return errors.New("invalid registry port")
	}
	unitPath := "/etc/systemd/system/" + serviceUnitName
	if _, err := os.Stat(unitPath); err == nil {
		return errors.Errorf("%s already exists; restore the clean snapshot before a new install", unitPath)
	} else if !os.IsNotExist(err) {
		return errors.Wrapf(err, "check %s", unitPath)
	}
	unit := fmt.Sprintf(`[Unit]
Description=ANI offline image registry
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s store serve registry --port %d --directory %s --readonly=true --store %s
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`, runtimeWorkRoot, haulerPath, port, registryDir, storeDir)
	return os.WriteFile(unitPath, []byte(unit), 0o644)
}

func waitRegistry(ctx context.Context, logger io.Writer, registryAddress string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s/v2/", registryAddress)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = runLogged(context.WithoutCancel(ctx), logger, "systemctl", "status", serviceUnitName, "--no-pager", "-l")
	return errors.New("timed out waiting for Hauler registry /v2/")
}

func verifyRegistryImages(ctx context.Context, logger io.Writer, registryAddress string, table ImageTable) error {
	client := &http.Client{Timeout: 5 * time.Second}
	for original, image := range table {
		path, err := ManifestURL(image.HaulerRef)
		if err != nil {
			return errors.Wrap(err, "build manifest URL")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s%s", registryAddress, path), nil)
		if err != nil {
			return errors.Wrap(err, "build image request")
		}
		req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
		resp, err := client.Do(req)
		if err != nil {
			return errors.Wrapf(err, "request image %s", original)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_ = runLogged(context.WithoutCancel(ctx), logger, "journalctl", "-u", serviceUnitName, "--no-pager", "-n", "100")
			return errors.Errorf("image %s manifest returned HTTP %d", original, resp.StatusCode)
		}
	}
	return nil
}
