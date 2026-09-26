package ani

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/kubesphere/kubekey/v4/version"
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

// SourceTreeFingerprint is the real source-tree identity of the code release,
// embedded at build time by scripts/build-code.sh via -ldflags -X. It is empty
// for a plain `go build`, in which case RunInstall records "not-embedded" rather
// than reusing an unrelated digest (F04: each identity from its own source).
var SourceTreeFingerprint = ""

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

func RunInstall(ctx context.Context, input InstallInput) (retErr error) {
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

	// R09/F05: every read-only check (config, required materials, chart/ISO/tool
	// digests, in-package SHA256SUMS, port, unit state, disk space) runs inside
	// RunPreflight BEFORE the first write, so a corrupt artifact or a foreign
	// unit fails without ever setting changesStarted. The runner no longer keeps
	// a second, un-consumed required list.
	runID := fmt.Sprintf("ani-%s-%s", cluster.Name, time.Now().Format("20060102-150405"))
	targets := make([]string, 0, len(cluster.Nodes))
	for _, node := range cluster.Nodes {
		targets = append(targets, node.Name)
	}
	// Real build/code identities, each from its own source (F04). The source
	// tree fingerprint is embedded at build time (empty for a non-release build,
	// never a stand-in digest); the binary digest is computed from THIS running
	// kk; the git commit comes from the version package.
	binaryDigest := fileSHA256Hex(kkPath)
	gitCommit := version.Get().GitCommit
	siteDigest, err := ConfigDigest(cluster)
	if err != nil {
		return err
	}
	sourceFingerprint := SourceTreeFingerprint
	if sourceFingerprint == "" {
		sourceFingerprint = "not-embedded"
	}
	report, err := RunPreflight(PreflightInput{
		RunID:                 runID,
		Cluster:               &cluster,
		PackageRoot:           input.PackageRoot,
		ArtifactRoot:          paths.ArtifactRoot,
		ReportBaseDir:         runtimeBaseDir,
		HelmPath:              filepath.Join(paths.ArtifactRoot, "bin", "helm"),
		HaulerPath:            paths.HaulerPath,
		SourceTreeFingerprint: sourceFingerprint,
		CodeCommit:            gitCommit,
		CodeBinaryDigest:      binaryDigest,
	})
	if err != nil {
		return err
	}

	// F05: verify the artifact's own in-package SHA256SUMS here — after the
	// read-only preflight and BEFORE the lock, the runtime root and
	// changesStarted — so a corrupt artifact fails without ever leaving a
	// "changes started" run to clean up. (The old code ran this only after the
	// state record had already set changesStarted=true.)
	if err := VerifyArtifactChecksums(paths.ArtifactRoot); err != nil {
		return err
	}

	// Single-writer lock shared with components execute and acceptance: a
	// second changer returns immediately. The lock file is never deleted and no
	// process is ever killed.
	releaseLock, err := AcquireInstallFlock(productLockPath())
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
		Result:         ResultRunning,
		ChangesStarted: true,
		RemoteResult:   RemoteResultDeterministic,
		ClusterName:    cluster.Name,
		Targets:        targets,

		SourceTreeFingerprint: sourceFingerprint,
		CodeCommit:            gitCommit,
		CodeBinaryDigest:      binaryDigest,
		SiteConfigDigest:      siteDigest,
		PackageConfigDigest:   report.PackageConfigDigest,
		KKPath:                kkPath,
		ArtifactLock:          report.ArtifactLock,
		// ConfigDigest is the site digest (its correct meaning), not package.yaml.
		ConfigDigest: siteDigest,
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
	// Finalizer: the last completed Phase is kept as-is (R09), but the Result
	// is written accurately on EVERY exit path — running→succeeded/failed/
	// cancelled — so a run that reached a late phase yet failed can never be
	// read as a success (F04). A failed persist is surfaced, not swallowed.
	installSucceeded := false
	defer func() {
		// C09: the outcome that gets persisted is settled from what this function
		// actually returned — never from a value assigned earlier in the body. The
		// previous guard (`if state.Result != ResultSucceeded`) let a run that had
		// already stamped itself succeeded keep that stamp even when the success
		// record then failed to land and the command exited non-zero, so the disk
		// said "succeeded" while the process said "error".
		//
		// R09 still holds: the last COMPLETED phase is left intact rather than
		// rewritten to install_failed; the accurate outcome lives in Result.
		settleTerminalResult(&state, retErr, ctx.Err(), installSucceeded)
		state.FinishedAt = time.Now().Format(time.RFC3339)
		if werr := WriteRunStateAtomic(statePath, state); werr != nil {
			// A terminal state that cannot be persisted is not a completed run.
			// This is reported, and a run that was about to be called successful
			// is not left to be read that way from a stale file.
			fmt.Fprintf(os.Stderr, "WARNING: failed to persist final run-state %s: %v\n", statePath, werr)
			if retErr == nil {
				retErr = errors.Wrapf(werr, "the install finished but its terminal run-state could not be persisted to %s; read that file before drawing any conclusion from this exit code", statePath)
			}
		}
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

	// The artifact's in-package SHA256SUMS were already verified in preflight,
	// before changesStarted; record the completed stage.
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
		return err
	}
	state.Phase = PhaseRegistryReady
	_ = WriteRunStateAtomic(statePath, state)
	lock, err := LoadMaterialsLock(filepath.Join(paths.ArtifactRoot, "config", "components.lock.yaml"))
	if err != nil {
		return err
	}
	if err := verifyRegistryImages(ctx, logger, registryAddress, imageTable, lock, &cluster, paths.ArtifactRoot); err != nil {
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
	state.Phase = PhaseClusterBuilt
	_ = WriteRunStateAtomic(statePath, state)
	if err := writeConnections(filepath.Join(paths.RuntimeRoot, "connections.md"), paths.WorkRoot, effectiveSelection(cluster)); err != nil {
		return err
	}

	// F04: capture the real live cluster identity and emit the trusted
	// install-success record that `kk ani verify` and `kk ani components`
	// consume as --base-run. The kubeconfig is pinned to the admin.conf this
	// install created — never inherited from HOME/KUBECONFIG.
	identity := ManifestIdentity{
		SourceTreeFingerprint: sourceFingerprint,
		CodeCommit:            gitCommit,
		CodeBinaryDigest:      binaryDigest,
		SiteConfigDigest:      siteDigest,
		MaterialsLockDigest:   report.ArtifactLock,
		PackageConfigDigest:   report.PackageConfigDigest,
	}
	if err := captureClusterIdentity(ctx, "/etc/kubernetes/admin.conf", &identity); err != nil {
		return errors.Wrap(err, "capture the live cluster identity for the success record")
	}
	baseManifest, err := BuildRunManifest(cluster)
	if err != nil {
		return err
	}
	baseManifest.PackageRoot = strings.TrimSpace(input.PackageRoot)
	// C09: success is published only after it is durable — record first, then
	// the terminal run-state, and only then may this command say "succeeded".
	recordPath, err := publishInstallSuccess(&state, statePath, paths.RuntimeRoot, baseManifest, identity)
	if err != nil {
		return err
	}
	installSucceeded = true
	fmt.Fprintf(logger, "ANI install completed at %s; install-success record=%s (run=%s clusterUid=%s nodes=%d)\n",
		time.Now().Format(time.RFC3339), recordPath, runID, identity.ClusterUID, identity.NodeCount)
	fmt.Fprintf(os.Stderr, "install succeeded: trusted base run.json written to %s\n", recordPath)
	return nil
}

// settleTerminalResult decides the outcome a finished run persists. It is a
// function rather than an inline switch so the C09 rule — the persisted result
// follows what the command actually returned, and a run that returned an error is
// never recorded as succeeded — is testable on the exact code the finalizer runs.
//
// R09 is preserved: only Result is settled here; Phase keeps the last stage that
// genuinely completed, so a late failure does not rewrite history.
func settleTerminalResult(state *InstallState, retErr, ctxErr error, installSucceeded bool) {
	switch {
	case retErr != nil && (errors.Is(retErr, context.Canceled) || errors.Is(retErr, context.DeadlineExceeded) || ctxErr != nil):
		state.Result = ResultCancelled
	case retErr != nil:
		state.Result = ResultFailed
	case installSucceeded:
		state.Result = ResultSucceeded
	case state.Result == ResultRunning:
		state.Result = ResultFailed
	}
}

// publishInstallSuccess is the whole C09 tail: the success record is written
// first, the terminal run-state second, and the caller's live state is moved to
// succeeded only after both landed. Anything that fails on the way returns an
// error describing what did happen, so the command can never exit non-zero while
// the disk claims an install nobody was told about.
//
// It is a function of its own because RunInstall needs root and a live cluster:
// this way the write-failure injections below run against the production
// sequence instead of a re-derivation of it.
func publishInstallSuccess(state *InstallState, statePath, recordDir string, baseManifest RunManifest, identity ManifestIdentity) (string, error) {
	pending := *state
	pending.Phase = PhaseSucceeded
	pending.Result = ResultSucceeded
	pending.FinishedAt = time.Now().Format(time.RFC3339)
	successManifest, err := BuildInstallSuccessManifest(baseManifest, pending, identity)
	if err != nil {
		return "", errors.Wrap(err, "build the install-success record")
	}
	if err := WriteInstallSuccessRecord(recordDir, successManifest); err != nil {
		return "", errors.Wrap(err, "write the install-success record")
	}
	if err := WriteRunStateAtomic(statePath, pending); err != nil {
		return filepath.Join(recordDir, RunManifestFileName), errors.Wrapf(err,
			"the install succeeded and its record is at %s, but the terminal run-state could not be written; re-read the state file before deciding what this run did",
			filepath.Join(recordDir, RunManifestFileName))
	}
	*state = pending
	return filepath.Join(recordDir, RunManifestFileName), nil
}

// fileSHA256Hex returns the 64-hex sha256 of a file's bytes, or "" on any read
// error so callers can refuse rather than record a fabricated digest.
func fileSHA256Hex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// captureClusterIdentity reads the stable live identity (kube-system UID plus
// the Ready node set) that binds later verification/extension operations to this
// exact cluster, so "same config" can never be mistaken for "same cluster" (F03).
func captureClusterIdentity(ctx context.Context, kubeconfig string, identity *ManifestIdentity) error {
	runner := kubectlRunner{bin: kubectlBin(), kubeconfig: kubeconfig}
	uid, err := runner.jsonpath(ctx, "namespace", "kube-system", "", "{.metadata.uid}")
	if err != nil {
		return errors.Wrapf(err, "read the kube-system namespace uid")
	}
	if strings.TrimSpace(uid) == "" {
		return errors.New("the kube-system namespace uid came back empty; refusing to write an unbound success record")
	}
	identity.ClusterUID = strings.TrimSpace(uid)
	nodesJSON, err := runner.run(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return errors.Wrap(err, "read the live node set")
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return errors.Wrap(err, "parse the live node set")
	}
	if len(nodes.Items) == 0 {
		return errors.New("the live cluster reports zero nodes; refusing to write a success record")
	}
	identity.NodeCount = len(nodes.Items)
	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				identity.ReadyNodes = append(identity.ReadyNodes, node.Metadata.Name)
			}
		}
	}
	if len(identity.ReadyNodes) != identity.NodeCount {
		return fmt.Errorf("only %d/%d nodes are Ready; a success record requires the full declared node set healthy", len(identity.ReadyNodes), identity.NodeCount)
	}
	sort.Strings(identity.ReadyNodes)
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

// verifyRegistryImages runs the shared content gate (F06) over every image row
// this run needs: the served manifest is resolved down to the linux/amd64
// object, every referenced blob must exist, the images.tsv pin must match, and
// an image the materials lock lists is additionally checked against its
// approved digests. Rows without a lock entry are verified against the table
// pin and logged as exactly that — never as lock-verified.
func verifyRegistryImages(ctx context.Context, logger io.Writer, registryAddress string, table ImageTable, lock *MaterialsLock, cluster *ClusterConfig, artifactRoot string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	gate := NewRegistryContentChecker(client, registryAddress, lock, EvidenceRoots(artifactRoot), true)
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
		// One shared content gate for every row (F06): the approved hauler_ref is
		// fetched, an index is followed down to this platform's manifest, each
		// referenced blob must exist, the images.tsv pin must hold, and the
		// materials lock rules apply wherever a lock entry exists.
		approval, err := gate.Verify(ctx, image)
		if err != nil {
			// A refused HTTP status usually means the registry or its store is
			// broken, so the unit's journal is captured for the operator.
			if registryHTTPStatus(err) != 0 {
				_ = runLogged(context.WithoutCancel(ctx), logger, "journalctl", "-u", serviceUnitName, "--no-pager", "-n", "100")
			}
			return errors.Wrapf(err, "verify image %s against the packaged content", original)
		}
		if lock != nil {
			if _, locked := lock.ImageByOriginal(original); locked {
				verifiedAgainstLock++
			}
		}
		fmt.Fprintf(logger, "image %s: %s\n", original, approval)
	}
	fmt.Fprintf(logger, "%d of %d images verified against the approved materials lock\n", verifiedAgainstLock, len(table))
	return nil
}
