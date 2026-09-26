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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cockroachdb/errors"
	"golang.org/x/sys/unix"
)

// Run-state phases, in the order the install reaches them. The state file is
// the accurate record of what completed: a failure leaves the last completed
// phase in place and never rewrites a partially changed system back to
// "nothing happened" (R09/A11).
const (
	PhasePreflightFailed      = "preflight_failed"
	PhasePreflightPassed      = "preflight_passed"
	PhaseInstalling           = "installing"
	PhaseArtifactVerified     = "artifact_verified"
	PhaseRegistryReady        = "registry_ready"
	PhaseRegistryVerified     = "registry_content_verified"
	PhaseClusterBuilt         = "cluster_built"
	PhaseSucceeded            = "succeeded"
	PhaseInstallFailed        = "install_failed"
	RemoteResultUnknown       = "remote_result_unknown"
	RemoteResultDeterministic = "remote_result_deterministic"
)

// Install results are the FINAL outcome of a run, kept separate from Phase
// (the last completed stage). A run can reach a late phase and still fail or
// be cancelled; verification and component additions key on Result, never on
// a mid-flight phase alone (F04).
const (
	ResultRunning       = "running"
	ResultSucceeded     = "succeeded"
	ResultFailed        = "failed"
	ResultCancelled     = "cancelled"
	ResultRemoteUnknown = "remote_result_unknown"
)

// InstallState is the on-disk run record. It is written atomically before the
// first system-changing operation and updated after every completed step.
type InstallState struct {
	SchemaVersion  int      `json:"schemaVersion"`
	RunID          string   `json:"runId"`
	StartedAt      string   `json:"startedAt"`
	FinishedAt     string   `json:"finishedAt,omitempty"`
	Phase          string   `json:"phase"`
	Result         string   `json:"result"`
	ChangesStarted bool     `json:"changesStarted"`
	RemoteResult   string   `json:"remoteResult"`
	ClusterName    string   `json:"clusterName"`
	Targets        []string `json:"targets"`

	// Identity fields each carry a distinct, real source (F04). The old code
	// stuffed the artifact package.yaml digest into both SourceTreeFp and
	// ConfigDigest, breaking the source → kk → site → live chain.
	SourceTreeFingerprint string `json:"sourceTreeFingerprint,omitempty"`
	CodeCommit            string `json:"codeCommit,omitempty"`
	CodeBinaryDigest      string `json:"codeBinaryDigest,omitempty"`
	SiteConfigDigest      string `json:"siteConfigDigest,omitempty"`
	PackageConfigDigest   string `json:"packageConfigDigest,omitempty"`
	KKPath                string `json:"kkPath"`
	ArtifactLock          string `json:"artifactLockDigest"`

	// ConfigDigest is kept as the site configuration digest so existing readers
	// of run-state.json keep working; it is no longer the package.yaml digest.
	ConfigDigest string `json:"configDigest"`
}

const InstallStateSchemaVersion = 1

// WriteRunStateAtomic writes the state file through a temporary file and rename,
// so a reader never sees a half-written record.
func WriteRunStateAtomic(path string, state InstallState) error {
	state.SchemaVersion = InstallStateSchemaVersion
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode the run state")
	}
	encoded = append(encoded, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return errors.Wrapf(err, "write %s", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Wrapf(err, "rename %s to %s", tmp, path)
	}
	return nil
}

// ReadRunState parses an existing state file.
func ReadRunState(path string) (*InstallState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state InstallState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, errors.Wrapf(err, "parse %s", path)
	}
	return &state, nil
}

// CheckStartAllowed decides whether a new install may start given the previous
// run's state (R09):
//   - no state, or a preflight-only state (changesStarted=false): a corrected
//     input allows a fresh run;
//   - changesStarted=true: the previous run changed the machines. A retry is
//     refused unless the operator acknowledges that specific run id — the lab
//     flow decides what to do with the changed machines, never the installer.
func CheckStartAllowed(state *InstallState, ackRunID string) error {
	if state == nil {
		return nil
	}
	if !state.ChangesStarted {
		return nil
	}
	if ackRunID != "" && ackRunID == state.RunID {
		return nil
	}
	return fmt.Errorf("previous run %s (phase %s, changesStarted=true, remoteResult=%q) changed the target machines; "+
		"handle it through the lab flow and re-run with ANI_ACK_PREVIOUS_RUN=%s to take over deliberately — "+
		"it will never be treated as an unchanged retry",
		state.RunID, state.Phase, state.RemoteResult, state.RunID)
}

// productLockPath is the ONE product-level mutex every change-bearing operation
// shares: the first install, `components execute`, and Pod-recreation
// acceptance. It lives in the canonical runtime root (never an --output dir),
// so a second changer returns immediately instead of interleaving writes.
// ANI_INSTALL_LOCK overrides the base for behaviour tests only.
func productLockPath() string {
	if v := strings.TrimSpace(os.Getenv("ANI_INSTALL_LOCK")); v != "" {
		return v
	}
	return filepath.Join(runtimeBaseDir, "ani-install.lock")
}

// AcquireInstallFlock takes a non-blocking exclusive flock on lockPath, so a
// second installer process returns immediately instead of corrupting the run.
// The lock file is never deleted and no process is ever killed: releasing the
// fd (process exit or explicit release) is the only way the lock goes away.
func AcquireInstallFlock(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, errors.Wrapf(err, "create lock directory for %s", lockPath)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Wrapf(err, "open lock file %s", lockPath)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.Wrapf(err, "another installer process holds %s", lockPath)
	}
	release := func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}
	return release, nil
}

// CheckDiskSpace verifies that the filesystem holding dir has at least
// requiredBytes free (R09: insufficient space fails before the Hauler import,
// and nothing ever deletes old images to make room).
func CheckDiskSpace(dir string, requiredBytes uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return errors.Wrapf(err, "stat filesystem for %s", dir)
	}
	free := stat.Bavail * uint64(stat.Bsize)
	if free < requiredBytes {
		return fmt.Errorf("insufficient disk space at %s: %d bytes free, %d required; "+
			"free space deliberately — this installer never deletes images to make room", dir, free, requiredBytes)
	}
	return nil
}

// CheckPortFree verifies that a TCP port is not already listening on this host
// (the installer node's registry port must be free before the service starts).
func CheckPortFree(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("port %d is already in use: %w", port, err)
	}
	return listener.Close()
}

// defaultSystemctlRunner executes systemctl through the shell; tests replace it
// via PATH or by injecting a runner.
func defaultSystemctlRunner(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// CheckUnitInactive verifies that a systemd unit is NOT active on this host.
// The runner must actually execute `systemctl is-active`: exit 0 (active) is a
// refusal, a non-zero exit means the unit is not running and is safe. The old
// implementation inverted this (treated exit 0 as safe) and substituted a no-op
// when the runner was nil, so production never queried systemd at all (F05).
//
// For full not-found / external-existing / query-error discrimination the
// install path uses checkRegistryUnitAbsent; this narrow predicate is the
// active/inactive gate used by tests and callers that pass a runner.
func CheckUnitInactive(unit string, runner func(string, ...string) error) error {
	if strings.TrimSpace(unit) == "" {
		return errors.New("CheckUnitInactive needs a unit name")
	}
	if runner == nil {
		runner = defaultSystemctlRunner
	}
	if err := runner("systemctl", "is-active", "--quiet", unit); err == nil {
		// exit 0 → the unit IS active; it must be stopped deliberately first.
		return fmt.Errorf("unit %s is active; stop and disable it deliberately before installing", unit)
	}
	return nil
}

// checkRegistryUnitAbsent classifies the registry unit's real state before the
// install may import images or write its own unit. It uses `systemctl show` to
// read LoadState and ActiveState and refuses anything other than a unit that
// simply does not exist yet:
//   - not-found / masked-off absent → nil (a fresh install may proceed);
//   - active → refuse;
//   - loaded but inactive (a foreign unit of the same name) → refuse, never
//     adopt an existing unit even before import (F05);
//   - query failure or unparseable output → refuse (absence is unproven).
func checkRegistryUnitAbsent(unit, systemctlBin string) error {
	if strings.TrimSpace(unit) == "" {
		return errors.New("checkRegistryUnitAbsent needs a unit name")
	}
	if strings.TrimSpace(systemctlBin) == "" {
		systemctlBin = "systemctl"
	}
	out, err := exec.Command(systemctlBin, "show", unit, "-p", "LoadState", "-p", "ActiveState", "--value").Output()
	if err != nil {
		// `systemctl show` on a not-found unit still exits 0 and prints
		// LoadState=not-found; a non-zero exit is a genuine query failure.
		return fmt.Errorf("query systemd state for %s: %w", unit, err)
	}
	// `--value` prints one bare value per requested property, in query order:
	// LoadState on the first line, ActiveState on the second.
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 1 {
		return fmt.Errorf("systemctl returned no systemd state for %s; refusing to proceed on an unproven unit state", unit)
	}
	loadState := strings.TrimSpace(lines[0])
	activeState := ""
	if len(lines) >= 2 {
		activeState = strings.TrimSpace(lines[1])
	}
	switch loadState {
	case "not-found", "masked":
		// The unit genuinely does not exist: a fresh install may proceed.
		return nil
	case "loaded", "static", "alias", "linked", "bad":
		if activeState == "active" || activeState == "activating" || activeState == "reloading" {
			return fmt.Errorf("unit %s is active (ActiveState=%s); a same-named foreign unit must be stopped and removed deliberately first", unit, activeState)
		}
		return fmt.Errorf("unit %s already exists (LoadState=%s); this install will not adopt a foreign unit of the same name, remove it deliberately first", unit, loadState)
	case "":
		return fmt.Errorf("systemctl returned an empty LoadState for %s; refusing to proceed on an unproven unit state", unit)
	default:
		return fmt.Errorf("unit %s has unexpected systemd LoadState %q; refusing to proceed", unit, loadState)
	}
}

// PreflightReport is the machine-readable record of a failed preflight run.
// It is written to a fresh per-run directory (never the runtime root), so a
// corrected input allows a new run without snapshot rituals.
type PreflightReport struct {
	SchemaVersion  int    `json:"schemaVersion"`
	RunID          string `json:"runId"`
	StartedAt      string `json:"startedAt"`
	ChangesStarted bool   `json:"changesStarted"`
	// ConfigDigest is the normalized SITE config digest (its original meaning;
	// it was previously wrongly the package.yaml digest).
	ConfigDigest string `json:"configDigest"`
	// PackageConfigDigest is the artifact config/package.yaml digest, kept
	// distinct so it can never masquerade as the site or source identity.
	PackageConfigDigest string   `json:"packageConfigDigest,omitempty"`
	ArtifactLock        string   `json:"artifactLockDigest"`
	FailedCheck         string   `json:"failedCheck"`
	Error               string   `json:"error"`
	RequiredMissing     []string `json:"requiredMissing,omitempty"`
}

// WritePreflightReport writes the preflight report into a fresh directory.
func WritePreflightReport(baseDir string, report PreflightReport) (string, error) {
	report.SchemaVersion = InstallStateSchemaVersion
	dir := filepath.Join(baseDir, "preflight-"+report.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errors.Wrapf(err, "create preflight report directory %s", dir)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "encode the preflight report")
	}
	encoded = append(encoded, '\n')
	target := filepath.Join(dir, "preflight.json")
	if err := os.WriteFile(target, encoded, 0o644); err != nil {
		return "", errors.Wrapf(err, "write %s", target)
	}
	return dir, nil
}

// VerifyFileMaterialDigest is the sha256 gate used by the preflight for every
// required file: the digest comes from the caller's approved record, never from
// the file being checked.
func VerifyFileMaterialDigest(path, approved string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.Wrapf(err, "read %s", path)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	want := strings.TrimPrefix(approved, "sha256:")
	if actual != want {
		return fmt.Errorf("%s: sha256 %s does not match the approved %s", path, actual, want)
	}
	return nil
}

// PreflightInput carries everything RunPreflight needs. The cluster config must
// already be parsed and validated (Validate is the caller's first gate).
type PreflightInput struct {
	RunID         string
	Cluster       *ClusterConfig
	PackageRoot   string
	ArtifactRoot  string
	ReportBaseDir string
	HelmPath      string
	HaulerPath    string
	SystemctlBin  string
	// SourceTreeFingerprint and CodeCommit are the real build-time identities
	// of the running kk, passed in so preflight/records never reuse an
	// unrelated digest as a stand-in.
	SourceTreeFingerprint string
	CodeCommit            string
	CodeBinaryDigest      string
}

// RunPreflight performs every read-only check that must pass before the first
// system-changing operation (R09 step 1): required artifact files, helm and ISO
// digests against the lock/record, the images.tsv↔lock cross-check, free disk
// space for the import, the registry port, and the unit state. A failure writes
// a fresh report (changesStarted=false) and returns an error; it never touches
// the runtime root, the lock file or any remote machine.
func RunPreflight(input PreflightInput) (*PreflightReport, error) {
	report := &PreflightReport{
		RunID:          input.RunID,
		ChangesStarted: false,
	}
	fail := func(check string, err error) error {
		report.FailedCheck = check
		report.Error = err.Error()
		if _, writeErr := WritePreflightReport(input.ReportBaseDir, *report); writeErr != nil {
			return fmt.Errorf("%s: %w (report write also failed: %v)", check, err, writeErr)
		}
		return fmt.Errorf("preflight failed at %s: %w (report: %s)", check, err, input.ReportBaseDir)
	}

	lock, err := LoadMaterialsLock(filepath.Join(input.ArtifactRoot, "config", "components.lock.yaml"))
	if err != nil {
		return report, fail("materials lock", err)
	}
	artifactLockDigest := sha256FileHex(filepath.Join(input.ArtifactRoot, "config", "components.lock.yaml"))

	// Required artifact files. Beyond metadata, the binaries the install will
	// actually run must be present: hauler (import+serve), the archive, the
	// artifact and the ISO. A missing one fails here, before any change (F05).
	required := []string{
		filepath.Join(input.ArtifactRoot, "SHA256SUMS"),
		filepath.Join(input.ArtifactRoot, "config", "package.yaml"),
		filepath.Join(input.ArtifactRoot, "config", "versions.yaml"),
		filepath.Join(input.ArtifactRoot, "config", "runtime-checksums.txt"),
		filepath.Join(input.ArtifactRoot, "config", "repository-iso-checksums.txt"),
		filepath.Join(input.ArtifactRoot, "config", "components.lock.yaml"),
		filepath.Join(input.ArtifactRoot, "packages", "kubekey-artifact.tgz"),
		filepath.Join(input.ArtifactRoot, "images", "images.haul.tar.zst"),
		filepath.Join(input.ArtifactRoot, "images", "images.tsv"),
		filepath.Join(input.ArtifactRoot, "repository", "ubuntu-24.04-debs-amd64.iso"),
	}
	if strings.TrimSpace(input.HaulerPath) != "" {
		required = append(required, input.HaulerPath)
	}
	if strings.TrimSpace(input.HelmPath) != "" {
		required = append(required, input.HelmPath)
	}
	missing := []string{}
	// Selected component charts are bound by their lock entry: existence AND
	// the exact approved hash for that component's artifactPath. A chart that
	// merely exists at the right path but carries a different (cross-valid)
	// hash fails here, not in build-offline's whole-YAML grep (F05/F06).
	for _, row := range effectiveSelection(*input.Cluster) {
		if !row.Enabled {
			continue
		}
		relative, ok := componentChartMaterials[row.Name]
		if !ok {
			continue
		}
		path := filepath.Join(input.ArtifactRoot, filepath.FromSlash(relative))
		required = append(required, path)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		chart, found := lock.ChartByArtifactPath(filepath.ToSlash(relative))
		if !found {
			return report, fail("chart lock binding", fmt.Errorf(
				"component %q chart %q has no lock entry binding name/version/artifactPath/hash; refusing an unapproved chart", row.Name, relative))
		}
		if chart.ChartVersion == "" {
			return report, fail("chart lock binding", fmt.Errorf(
				"component %q chart lock entry for %q carries no chartVersion", row.Name, relative))
		}
		if err := VerifyFileMaterialDigest(path, chart.SHA256); err != nil {
			return report, fail("chart content digest", err)
		}
	}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		report.RequiredMissing = missing
		return report, fail("required artifact files", fmt.Errorf("%d required file(s) missing: %s", len(missing), strings.Join(missing, ", ")))
	}

	// Helm binary digest against the lock (tool material, R07.2).
	if input.HelmPath != "" {
		if tool, ok := lock.ToolByArtifactPath("bin/helm"); ok {
			if err := VerifyFileMaterialDigest(input.HelmPath, tool.BinarySHA256); err != nil {
				return report, fail("helm digest", err)
			}
		}
	}

	// Repository ISO digest against the source-side record. A malformed record
	// (not exactly "hash path") or an unreadable one is a hard failure, never
	// a silent skip (F05).
	isoPath := filepath.Join(input.ArtifactRoot, "repository", "ubuntu-24.04-debs-amd64.iso")
	isoRecord := filepath.Join(input.ArtifactRoot, "config", "repository-iso-checksums.txt")
	isoData, err := os.ReadFile(isoRecord)
	if err != nil {
		return report, fail("repository ISO record", errors.Wrapf(err, "read %s", isoRecord))
	}
	isoFields := strings.Fields(string(isoData))
	if len(isoFields) != 2 {
		return report, fail("repository ISO record", fmt.Errorf(
			"%s must contain exactly one '<name> <hash>' pair, got %d field(s); a malformed ISO record is a failure, not a skip", isoRecord, len(isoFields)))
	}
	if err := VerifyFileMaterialDigest(isoPath, isoFields[1]); err != nil {
		return report, fail("repository ISO digest", err)
	}

	// images.tsv ↔ lock cross-check.
	tableRows, err := os.ReadFile(filepath.Join(input.ArtifactRoot, "images", "images.tsv"))
	if err != nil {
		return report, fail("images.tsv", err)
	}
	table, err := LoadImageTable(strings.Split(string(tableRows), "\n"))
	if err != nil {
		return report, fail("images.tsv", err)
	}
	if err := lock.VerifyImageTableAgainstLock(table); err != nil {
		return report, fail("images.tsv cross-check", err)
	}

	// Free disk space on the runtime filesystem: the Hauler import needs the
	// artifact, the archive and the ISO unpacked. Insufficient space fails here
	// and nothing ever deletes images to make room.
	spaceNeeded := uint64(0)
	for _, path := range []string{
		filepath.Join(input.ArtifactRoot, "packages", "kubekey-artifact.tgz"),
		filepath.Join(input.ArtifactRoot, "images", "images.haul.tar.zst"),
		filepath.Join(input.ArtifactRoot, "repository", "ubuntu-24.04-debs-amd64.iso"),
	} {
		if info, err := os.Stat(path); err == nil {
			spaceNeeded += uint64(info.Size())
		}
	}
	spaceNeeded = spaceNeeded + spaceNeeded/2 // unpack headroom
	runtimeParent := filepath.Dir(runtimeBaseDir)
	if err := CheckDiskSpace(runtimeParent, spaceNeeded); err != nil {
		return report, fail("disk space", err)
	}

	// Registry port and unit state on this host. The unit check is real:
	// checkRegistryUnitAbsent refuses active or foreign-existing units and
	// only allows a genuinely absent one, so a same-named external service is
	// never silently adopted or overwritten after the images are imported (F05).
	if err := CheckPortFree(input.Cluster.RegistryConfig.Port); err != nil {
		return report, fail("registry port", err)
	}
	if err := checkRegistryUnitAbsent(serviceUnitName, input.SystemctlBin); err != nil {
		return report, fail("unit state", err)
	}

	// Success: report the real identities. ConfigDigest is the normalized SITE
	// digest (never package.yaml); the package.yaml digest is kept separately so
	// it can never be reused as the site or source identity (F04).
	siteDigest, err := ConfigDigest(*input.Cluster)
	if err != nil {
		return report, fail("site config digest", err)
	}
	report.FailedCheck = ""
	report.Error = ""
	report.ConfigDigest = siteDigest
	report.PackageConfigDigest = sha256FileHex(filepath.Join(input.ArtifactRoot, "config", "package.yaml"))
	report.ArtifactLock = artifactLockDigest
	return report, nil
}

// VerifyArtifactChecksums runs the artifact's own `sha256sum --check
// --quiet SHA256SUMS` from its root. Read-only; used by preflight so a corrupt
// artifact fails before any system change.
func VerifyArtifactChecksums(artifactRoot string) error {
	cmd := exec.Command("sha256sum", "--check", "--quiet", "SHA256SUMS")
	cmd.Dir = artifactRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.Wrapf(err, "verify artifact checksums in %s: %s", artifactRoot, strings.TrimSpace(string(out)))
	}
	return nil
}

// sha256FileHex is the file digest helper the preflight record uses.
func sha256FileHex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
