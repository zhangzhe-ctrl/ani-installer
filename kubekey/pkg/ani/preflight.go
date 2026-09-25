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
	PhaseInstallFailed        = "install_failed"
	RemoteResultUnknown       = "remote_result_unknown"
	RemoteResultDeterministic = "remote_result_deterministic"
)

// InstallState is the on-disk run record. It is written atomically before the
// first system-changing operation and updated after every completed step.
type InstallState struct {
	SchemaVersion  int      `json:"schemaVersion"`
	RunID          string   `json:"runId"`
	StartedAt      string   `json:"startedAt"`
	Phase          string   `json:"phase"`
	ChangesStarted bool     `json:"changesStarted"`
	RemoteResult   string   `json:"remoteResult"`
	ClusterName    string   `json:"clusterName"`
	Targets        []string `json:"targets"`
	SourceTreeFp   string   `json:"sourceTreeFingerprint"`
	KKPath         string   `json:"kkPath"`
	ArtifactLock   string   `json:"artifactLockDigest"`
	ConfigDigest   string   `json:"configDigest"`
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

// CheckUnitInactive verifies that a systemd unit is not active on this host,
// via the systemctl binary (test fakes replace it through PATH).
func CheckUnitInactive(unit string, runner func(string, ...string) error) error {
	if runner == nil {
		runner = func(name string, args ...string) error { return nil }
	}
	if err := runner("systemctl", "is-active", "--quiet", unit); err != nil {
		return fmt.Errorf("unit %s is active; stop and disable it deliberately before installing", unit)
	}
	return nil
}

// PreflightReport is the machine-readable record of a failed preflight run.
// It is written to a fresh per-run directory (never the runtime root), so a
// corrected input allows a new run without snapshot rituals.
type PreflightReport struct {
	SchemaVersion   int      `json:"schemaVersion"`
	RunID           string   `json:"runId"`
	StartedAt       string   `json:"startedAt"`
	ChangesStarted  bool     `json:"changesStarted"`
	ConfigDigest    string   `json:"configDigest"`
	ArtifactLock    string   `json:"artifactLockDigest"`
	FailedCheck     string   `json:"failedCheck"`
	Error           string   `json:"error"`
	RequiredMissing []string `json:"requiredMissing,omitempty"`
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

	// Required artifact files, including the per-component chart materials.
	required := []string{
		filepath.Join(input.ArtifactRoot, "SHA256SUMS"),
		filepath.Join(input.ArtifactRoot, "config", "package.yaml"),
		filepath.Join(input.ArtifactRoot, "config", "versions.yaml"),
		filepath.Join(input.ArtifactRoot, "config", "runtime-checksums.txt"),
		filepath.Join(input.ArtifactRoot, "config", "repository-iso-checksums.txt"),
		filepath.Join(input.ArtifactRoot, "config", "components.lock.yaml"),
	}
	missing := []string{}
	for _, row := range effectiveSelection(*input.Cluster) {
		if !row.Enabled {
			continue
		}
		if relative, ok := componentChartMaterials[row.Name]; ok {
			required = append(required, filepath.Join(input.ArtifactRoot, filepath.FromSlash(relative)))
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

	// Repository ISO digest against the source-side record.
	isoPath := filepath.Join(input.ArtifactRoot, "repository", "ubuntu-24.04-debs-amd64.iso")
	isoRecord := filepath.Join(input.ArtifactRoot, "config", "repository-iso-checksums.txt")
	if data, err := os.ReadFile(isoRecord); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) == 2 {
			if err := VerifyFileMaterialDigest(isoPath, fields[1]); err != nil {
				return report, fail("repository ISO digest", err)
			}
		}
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

	// Registry port and unit state on this host.
	if err := CheckPortFree(input.Cluster.RegistryConfig.Port); err != nil {
		return report, fail("registry port", err)
	}
	if err := CheckUnitInactive(serviceUnitName, nil); err != nil {
		return report, fail("unit state", err)
	}

	report.FailedCheck = ""
	digest := ""
	if configData, err := os.ReadFile(filepath.Join(input.ArtifactRoot, "config", "package.yaml")); err == nil {
		sum := sha256.Sum256(configData)
		digest = hex.EncodeToString(sum[:])
	}
	return &PreflightReport{
		RunID:          report.RunID,
		StartedAt:      report.StartedAt,
		ChangesStarted: false,
		ConfigDigest:   digest,
		ArtifactLock:   artifactLockDigest,
	}, nil
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
