package ani

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
)

// runtimeBaseDir is the canonical per-cluster runtime root. It is a variable
// only so behaviour tests can point the component-role fragment contract at a
// scratch directory (the production default is the path below).
var runtimeBaseDir = "/var/lib/ani-installer"

// systemdUnitDir is where the bootstrap registry unit is written. It is a
// variable only so behaviour tests can point it at a scratch directory
// without root.
var systemdUnitDir = "/etc/systemd/system"

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
	// Every installed component must have its fixed Chart material in the
	// artifact before deployment starts, so a missing chart fails here and not
	// mid-install. The base profile cuts the chain after the network stack and
	// the playbook skips every component role, so their charts are not part of
	// a base artifact and must not be demanded here (failure a2 on 2026-09-22:
	// the kubeovn-base artifact ships no charts, and the site config keeps the
	// component switches from its base copy, so the preflight died on
	// charts/cert-manager/v1.21.2.tgz before the cluster install even began).
	selection := map[string]bool{}
	for _, row := range effectiveSelection(cluster) {
		selection[row.Name] = row.Enabled
	}
	for name, relative := range componentChartMaterials {
		if selection[name] {
			required = append(required, filepath.Join(paths.ArtifactRoot, filepath.FromSlash(relative)))
		}
	}
	// R09: every read-only check (config, digests, required materials, port,
	// unit, disk space) runs BEFORE the first write. A failure writes a fresh
	// preflight report with changesStarted=false and touches nothing else.
	runID := fmt.Sprintf("ani-%s-%s", cluster.Name, time.Now().Format("20060102-150405"))
	targets := make([]string, 0, len(cluster.Nodes))
	for _, node := range cluster.Nodes {
		targets = append(targets, node.Name)
	}
	report, err := RunPreflight(PreflightInput{
		RunID:         runID,
		Cluster:       &cluster,
		PackageRoot:   input.PackageRoot,
		ArtifactRoot:  paths.ArtifactRoot,
		ReportBaseDir: runtimeBaseDir,
		HelmPath:      filepath.Join(paths.ArtifactRoot, "bin", "helm"),
	})
	if err != nil {
		return err
	}

	// Single-writer lock: a second installer process returns immediately. The
	// lock file is never deleted and no process is ever killed.
	releaseLock, err := AcquireInstallFlock(filepath.Join(runtimeBaseDir, "ani-install.lock"))
	if err != nil {
		return err
	}
	defer releaseLock()

	// A previous run that already changed the machines blocks a blind retry.
	statePath := filepath.Join(runtimeBaseDir, cluster.Name, "run-state.json")
	if previous, readErr := ReadRunState(statePath); readErr == nil {
		if err := CheckStartAllowed(previous, os.Getenv("ANI_ACK_PREVIOUS_RUN")); err != nil {
			return err
		}
	}

	state := InstallState{
		RunID:          runID,
		StartedAt:      time.Now().Format(time.RFC3339),
		Phase:          PhaseInstalling,
		ChangesStarted: true,
		RemoteResult:   RemoteResultDeterministic,
		ClusterName:    cluster.Name,
		Targets:        targets,
		SourceTreeFp:   report.ConfigDigest,
		KKPath:         kkPath,
		ArtifactLock:   report.ArtifactLock,
		ConfigDigest:   report.ConfigDigest,
	}
	// Atomic state record BEFORE the first write, then keep it accurate after
	// every completed step (R09: a failure leaves the last completed phase).
	// R15.3 field fix: the runtime root is created BEFORE the state record —
	// the state file lives inside the runtime root, so writing the state
	// first made createRuntimeRoot always hit fs.ErrExist and every FIRST
	// install fail. The R09 safety order is preserved where it matters: the
	// state record still precedes every remote/system-changing operation.
	if err := createRuntimeRoot(paths.RuntimeRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return errors.Wrap(err, "create state directory")
	}
	if err := WriteRunStateAtomic(statePath, state); err != nil {
		return err
	}
	defer func() {
		_ = WriteRunStateAtomic(statePath, state)
	}()
	if err := os.MkdirAll(paths.WorkRoot, 0o700); err != nil {
		return errors.Wrap(err, "create work directory")
	}
	if err := os.MkdirAll(paths.LogRoot, 0o700); err != nil {
		return errors.Wrap(err, "create log directory")
	}
	if err := writeComponentSelection(filepath.Join(paths.WorkRoot, "components-selection.tsv"), configSHA256(configData), effectiveSelection(cluster)); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.LogRoot, "install.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrap(err, "open install log")
	}
	defer logFile.Close()
	logger := newInstallLogger(logFile)
	fmt.Fprintf(logger, "ANI install started at %s; artifact=%s kk=%s config=%s runtime=%s run=%s\n",
		time.Now().Format(time.RFC3339), paths.ArtifactRoot, kkPath, configPath, paths.RuntimeRoot, runID)

	if err := verifyArtifactChecksums(ctx, logger, paths.ArtifactRoot); err != nil {
		state.Phase = PhaseInstallFailed
		_ = WriteRunStateAtomic(statePath, state)
		return err
	}
	state.Phase = PhaseArtifactVerified
	_ = WriteRunStateAtomic(statePath, state)
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
	// R14/A15: the unit was freshly written by THIS run (writeRegistryService
	// refuses a pre-existing one), so enabling it for boot is this run's own
	// lifecycle action — never a takeover of a foreign service.
	if err := startRegistryService(ctx, logger); err != nil {
		return err
	}
	if err := waitRegistry(ctx, logger, registryAddress); err != nil {
		state.Phase = PhaseInstallFailed
		state.RemoteResult = RemoteResultDeterministic
		_ = WriteRunStateAtomic(statePath, state)
		return err
	}
	state.Phase = PhaseRegistryReady
	_ = WriteRunStateAtomic(statePath, state)
	lock, err := LoadMaterialsLock(filepath.Join(paths.ArtifactRoot, "config", "components.lock.yaml"))
	if err != nil {
		return err
	}
	if err := verifyRegistryImages(ctx, logger, registryAddress, imageTable, lock, &cluster); err != nil {
		state.Phase = PhaseInstallFailed
		state.RemoteResult = RemoteResultDeterministic
		_ = WriteRunStateAtomic(statePath, state)
		return err
	}
	state.Phase = PhaseRegistryVerified
	_ = WriteRunStateAtomic(statePath, state)
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
	if err := writeConnections(filepath.Join(paths.RuntimeRoot, "connections.md"), paths.WorkRoot, effectiveSelection(cluster)); err != nil {
		return err
	}
	fmt.Fprintf(logger, "ANI install completed at %s\n", time.Now().Format(time.RFC3339))
	return nil
}

// connectionsDirName is where each enabled component role renders its own
// connection facts fragment. The installer only concatenates them; it never
// duplicates service names or Secret references.
const connectionsDirName = "connections.d"

// writeConnections assembles connections.md from the per-component fragments
// the roles rendered. A component that was enabled but produced no fragment is
// an error: silently omitting it would hand out an incomplete connection
// document. The file is root-only because it lists Secret names and namespaces.
func writeConnections(dest, workRoot string, rows []ComponentRow) error {
	dir := filepath.Join(workRoot, connectionsDirName)
	var b strings.Builder
	b.WriteString("# ANI component connection facts\n\n")
	b.WriteString("Generated by the ANI installer after a successful install.\n")
	b.WriteString("Every name below exists in the cluster described by this install run.\n")
	b.WriteString("Secret entries are references only; this file contains no credentials.\n")
	enabled := 0
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		enabled++
		data, err := os.ReadFile(filepath.Join(dir, row.Name+".md"))
		if err != nil {
			return errors.Wrapf(err, "read connection facts for enabled component %s in %s", row.Name, dir)
		}
		b.WriteString("\n")
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteString("\n")
		}
	}
	if enabled == 0 {
		b.WriteString("\nNo components are enabled for this install run.\n")
	}
	if err := os.WriteFile(dest, []byte(b.String()), 0o600); err != nil {
		return errors.Wrapf(err, "write %s", dest)
	}
	return nil
}

// effectiveSelection applies the profile cut to the site's component switches.
// The base profile ends the install with the base cluster (kubernetes + the
// CNI network stack): the playbook gates every component role on
// `ne .ani.profile "base"`, so no component is installed even when the site
// config keeps its switch on. Everything downstream of the site config (the
// chart preflight, components-selection.tsv, connections.md) must see the same
// effective result, or the install demands material a base artifact never
// ships.
func effectiveSelection(c ClusterConfig) []ComponentRow {
	rows := c.Components.Selection()
	if installProfile(c.Profile) == "base" {
		for i := range rows {
			rows[i].Enabled = false
		}
	}
	return rows
}

// componentChartMaterials maps a component to the fixed Chart path inside the
// offline artifact. Paths are relative so the artifact can be relocated. Every
// component that installs from a Chart must be listed, so a missing Chart fails
// before deployment instead of mid-install. The foundation batch lists
// cert-manager and NATS; the observability batch lists its four Charts.
var componentChartMaterials = map[string]string{
	"cert-manager": "charts/cert-manager/v1.21.2.tgz",
	"nats":         "charts/nats/2.14.6.tgz",
	"metrics":      "charts/kube-prometheus-stack/85.4.0.tgz",
	"loki":         "charts/loki/18.13.3.tgz",
	"opensearch":   "charts/opensearch/3.8.0.tgz",
	"fluent-bit":   "charts/fluent-bit/0.58.2.tgz",
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
	// R14/A15: everything the unit references must live on permanent storage —
	// a registry started from /tmp or similar dies with the cleanup and the
	// reboot loses the only image source.
	for field, path := range map[string]string{
		"working directory": runtimeWorkRoot,
		"hauler binary":     haulerPath,
		"registry data":     registryDir,
		"hauler store":      storeDir,
	} {
		if err := ensurePermanentPath(field, path); err != nil {
			return err
		}
	}
	unitPath := filepath.Join(systemdUnitDir, serviceUnitName)
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

// ensurePermanentPath refuses paths under temporary directories: the
// bootstrap registry and its data must survive reboots (R14/A15 step 2).
func ensurePermanentPath(field, path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return errors.Errorf("%s %q must be an absolute path", field, path)
	}
	for _, tmp := range []string{"/tmp", "/var/tmp", "/run", "/var/run", "/dev/shm"} {
		if clean == tmp || strings.HasPrefix(clean, tmp+"/") {
			return errors.Errorf("%s %q is under the temporary directory %s; the registry must run from permanent storage or it will not survive a reboot", field, path, tmp)
		}
	}
	return nil
}

// startRegistryService is the exact lifecycle sequence a fresh install runs
// for the unit writeRegistryService just created: reload, enable for boot
// (R14/A15 step 1), then start. It is only ever called right after THIS run
// created the unit, so enable never touches a foreign service.
func startRegistryService(ctx context.Context, logger io.Writer) error {
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", serviceUnitName},
		{"restart", serviceUnitName},
	} {
		if err := runLogged(ctx, logger, "systemctl", args...); err != nil {
			return errors.Wrapf(err, "systemctl %v", args)
		}
	}
	return nil
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

// hashRaw is the raw-byte sha256 used for the served manifest digest.
func hashRaw(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

// verifyRegistryImages checks the actually served content of every image in the
// table against the approved materials lock (R07.3): the served digest, the
// manifest kind (index vs platform manifest), the config/layer digests it
// references, and the blob existence for each of them. Images without a lock
// entry (KubeKey-artifact and base images) keep the presence-only check and are
// logged as not lock-verified — their digests stay unknown instead of being
// fabricated.
func verifyRegistryImages(ctx context.Context, logger io.Writer, registryAddress string, table ImageTable, lock *MaterialsLock, cluster *ClusterConfig) error {
	client := &http.Client{Timeout: 15 * time.Second}
	verifiedAgainstLock := 0
	// R15.3: component-scoped images are only verified when their component
	// is enabled in THIS run. A disabled component's images may legitimately
	// be absent from the registry (the artifact ships the full declared
	// table, but a run never required them). Base/always rows (not scoped to
	// any component) are always verified.
	scoped := map[string]bool{}
	required := map[string]bool{}
	for _, k := range componentImageKeys() {
		scoped[k.Original] = true
	}
	if cluster != nil {
		for _, k := range componentImageKeysForRun(*cluster) {
			required[k.Original] = true
		}
	}
	for original, image := range table {
		if scoped[original] && cluster != nil && !required[original] {
			fmt.Fprintf(logger, "image %s: skipped (component not enabled in this run)\n", original)
			continue
		}
		manifestPath, err := ManifestURL(image.HaulerRef)
		if err != nil {
			return errors.Wrap(err, "build manifest URL")
		}
		repoPath, err := RepositoryPath(image.HaulerRef)
		if err != nil {
			return errors.Wrap(err, "build repository path")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s%s", registryAddress, manifestPath), nil)
		if err != nil {
			return errors.Wrap(err, "build image request")
		}
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
		resp, err := client.Do(req)
		if err != nil {
			return errors.Wrapf(err, "request image %s", original)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_ = runLogged(context.WithoutCancel(ctx), logger, "journalctl", "-u", serviceUnitName, "--no-pager", "-n", "100")
			return errors.Errorf("image %s manifest returned HTTP %d", original, resp.StatusCode)
		}
		servedDigest := fmt.Sprintf("sha256:%s", hex.EncodeToString(hashRaw(body)))

		served, parseErr := ParseServedImageManifest(body)
		if parseErr != nil {
			return errors.Wrapf(parseErr, "image %s", original)
		}

		// The config blob and every layer blob the manifest references must be
		// present in the same repository (HEAD, cheap: no blob download).
		blobs := append([]string{}, served.LayerDigests...)
		if served.ConfigDigest != "" {
			blobs = append([]string{served.ConfigDigest}, blobs...)
		}
		for _, blob := range blobs {
			head, headErr := http.NewRequestWithContext(ctx, http.MethodHead, fmt.Sprintf("http://%s%s/blobs/%s", registryAddress, repoPath, url.PathEscape(blob)), nil)
			if headErr != nil {
				return errors.Wrapf(headErr, "build blob request for %s", original)
			}
			blobResp, err := client.Do(head)
			if err != nil {
				return errors.Wrapf(err, "request blob %s of image %s", blob, original)
			}
			_, _ = io.Copy(io.Discard, blobResp.Body)
			_ = blobResp.Body.Close()
			if blobResp.StatusCode != http.StatusOK {
				return errors.Errorf("image %s: blob %s referenced by the served manifest returned HTTP %d",
					original, blob, blobResp.StatusCode)
			}
		}

		entry, locked := lock.ImageByOriginal(original)
		if !locked {
			fmt.Fprintf(logger, "image %s: served digest %s (no lock entry: verified for presence only, digest stays unknown)\n",
				original, servedDigest)
			continue
		}
		if err := VerifyServedManifest(*entry, servedDigest, served); err != nil {
			return errors.Wrapf(err, "registry content does not match the approved materials lock")
		}
		verifiedAgainstLock++
		kind := "platform manifest"
		if served.IsIndex {
			kind = "multi-arch index"
		}
		fmt.Fprintf(logger, "image %s: served %s digest %s matches the approved lock entry\n", original, kind, servedDigest)
	}
	fmt.Fprintf(logger, "%d of %d images verified against the approved materials lock\n", verifiedAgainstLock, len(table))
	return nil
}
