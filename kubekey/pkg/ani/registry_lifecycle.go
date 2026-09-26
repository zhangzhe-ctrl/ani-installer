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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// ---------------------------------------------------------------------------
// R14/A15: read-only lifecycle inspection of the bootstrap registry service.
//
// The bootstrap registry is a single-node convenience source on the installer
// node. It is explicitly NOT production HA, and a later Harbor migration is a
// separate plan: nothing in this file stops, disables or removes the service.
//
// Reporting rule (T-R14-04): `systemctl is-enabled`/`is-active` prove the
// unit configuration and the current state — they can never prove that the
// service survived a REAL reboot, and a cold pull is only proven by an actual
// blob transfer recorded by the manual experiment. Until such evidence exists
// the report says coldStart=not_verified.
// ---------------------------------------------------------------------------

// RegistryColdStartValues are the only values RegistryLifecycleReport.ColdStart
// may carry.
const (
	RegistryColdStartNotVerified = "not_verified"
	RegistryColdStartVerified    = "verified"
)

// RegistryRebootEvidenceFile names the file the (manual, live-only) reboot
// experiment writes after a real restart with a cold-pull log. InspectRegistry
// Lifecycle never writes it.
var RegistryRebootEvidenceFile = filepath.Join(
	"/var/lib/ani-installer", "registry-reboot-evidence.json")

// RegistryRebootEvidence is what the manual, live-only reboot experiment must
// record (F11). The existence of a file proves nothing: an empty file, a file
// from an older host, an older boot or an older build would all otherwise read
// as "the registry survived a reboot". Every field below is checked against the
// unit and the machine the report is being produced for.
type RegistryRebootEvidence struct {
	SchemaVersion       int                    `json:"schemaVersion"`
	Unit                string                 `json:"unit"`
	Host                string                 `json:"host"`
	BootIDBefore        string                 `json:"bootIdBefore"`
	BootIDAfter         string                 `json:"bootIdAfter"`
	RegistryAddress     string                 `json:"registryAddress"`
	ClusterName         string                 `json:"clusterName,omitempty"`
	ConfigDigest        string                 `json:"configDigest,omitempty"`
	MaterialsLockDigest string                 `json:"materialsLockDigest,omitempty"`
	ColdPull            RegistryColdPullResult `json:"coldPull"`
	RecordedAt          string                 `json:"recordedAt"`
}

// RegistryColdPullResult is the actual cold fetch the experiment observed after
// the restart: a real blob transfer, not a service that merely answered.
type RegistryColdPullResult struct {
	Image       string `json:"image"`
	Digest      string `json:"digest"`
	BytesPulled int64  `json:"bytesPulled"`
	Result      string `json:"result"`
	LogPath     string `json:"logPath,omitempty"`
}

// RegistryColdStartSchemaVersion is the only evidence schema understood.
const RegistryColdStartSchemaVersion = 1

// readBootID reads this boot's id; injectable so the behaviour tests can model a
// stale boot without pretending to reboot the machine.
var readBootID = func() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", errors.Wrap(err, "read the current boot id")
	}
	return strings.TrimSpace(string(data)), nil
}

// validateRegistryRebootEvidence reads the evidence file and checks it against
// the unit and host being inspected. It returns why the cold start is NOT
// verified whenever it is not, so the report never collapses "absent", "stale"
// and "broken" into one word.
func validateRegistryRebootEvidence(unit, execStart, clusterName, configDigest, materialsLockDigest string) (string, bool) {
	data, err := os.ReadFile(RegistryRebootEvidenceFile)
	if err != nil {
		return fmt.Sprintf("no reboot evidence recorded (%v)", err), false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return "the reboot evidence file is empty", false
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var evidence RegistryRebootEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return fmt.Sprintf("the reboot evidence file does not parse as the expected schema: %v", err), false
	}
	if remainder := decoder.More(); remainder {
		return "the reboot evidence file holds more than one JSON document", false
	}
	fail := func(reason string) (string, bool) { return reason, false }
	if evidence.SchemaVersion != RegistryColdStartSchemaVersion {
		return fail(fmt.Sprintf("reboot evidence schemaVersion %d is not supported (this build reads %d)",
			evidence.SchemaVersion, RegistryColdStartSchemaVersion))
	}
	if evidence.Unit != unit {
		return fail(fmt.Sprintf("reboot evidence names unit %q, not the inspected unit %q", evidence.Unit, unit))
	}
	host, err := os.Hostname()
	if err != nil {
		return fail(fmt.Sprintf("this host's name cannot be read to check the reboot evidence: %v", err))
	}
	if evidence.Host != host {
		return fail(fmt.Sprintf("reboot evidence was recorded on host %q, not on this host %q", evidence.Host, host))
	}
	if strings.TrimSpace(evidence.BootIDBefore) == "" || strings.TrimSpace(evidence.BootIDAfter) == "" {
		return fail("reboot evidence records no boot id before and after the restart")
	}
	if evidence.BootIDBefore == evidence.BootIDAfter {
		return fail("reboot evidence records the same boot id before and after: no restart happened")
	}
	currentBoot, err := readBootID()
	if err != nil {
		return fail(err.Error())
	}
	if evidence.BootIDAfter != currentBoot {
		return fail(fmt.Sprintf("reboot evidence is from an older boot (%s); this system booted as %s",
			evidence.BootIDAfter, currentBoot))
	}
	recordedAt, err := time.Parse(time.RFC3339, evidence.RecordedAt)
	if err != nil {
		return fail(fmt.Sprintf("reboot evidence recordedAt %q is not an RFC3339 timestamp", evidence.RecordedAt))
	}
	if recordedAt.After(time.Now()) {
		return fail(fmt.Sprintf("reboot evidence is dated in the future (%s)", evidence.RecordedAt))
	}
	port, err := registryPortFromExecStart(execStart)
	if err != nil {
		return fail(err.Error())
	}
	_, addressPort, err := net.SplitHostPort(evidence.RegistryAddress)
	if err != nil {
		return fail(fmt.Sprintf("reboot evidence registry address %q is not host:port", evidence.RegistryAddress))
	}
	if addressPort != strconv.Itoa(port) {
		return fail(fmt.Sprintf("reboot evidence names registry port %s but the inspected unit serves --port %d",
			addressPort, port))
	}
	if evidence.ColdPull.Result != "ok" {
		return fail(fmt.Sprintf("the recorded cold pull result is %q, not ok", evidence.ColdPull.Result))
	}
	if strings.TrimSpace(evidence.ColdPull.Image) == "" || !isSHA256Digest(evidence.ColdPull.Digest) {
		return fail("the recorded cold pull names no image or no sha256 manifest digest")
	}
	if evidence.ColdPull.BytesPulled <= 0 {
		return fail(fmt.Sprintf("the recorded cold pull transferred %d bytes: nothing proves a real fetch",
			evidence.ColdPull.BytesPulled))
	}
	if clusterName != "" && evidence.ClusterName != "" && evidence.ClusterName != clusterName {
		return fail(fmt.Sprintf("reboot evidence belongs to cluster %q, not %q", evidence.ClusterName, clusterName))
	}
	if configDigest != "" && evidence.ConfigDigest != "" && evidence.ConfigDigest != configDigest {
		return fail("reboot evidence was recorded against a different site config")
	}
	if materialsLockDigest != "" && evidence.MaterialsLockDigest != "" && evidence.MaterialsLockDigest != materialsLockDigest {
		return fail("reboot evidence was recorded against different approved materials")
	}
	return fmt.Sprintf("verified against the reboot evidence in %s (host %s, boot %s, cold pull %s of %s in %d byte(s))",
		RegistryRebootEvidenceFile, host, currentBoot, evidence.ColdPull.Image, evidence.ColdPull.Digest,
		evidence.ColdPull.BytesPulled), true
}

// registryPortFromExecStart reads the --port the unit actually starts with.
func registryPortFromExecStart(execStart string) (int, error) {
	fields := strings.Fields(execStart)
	for index, field := range fields {
		if field == "--port" && index+1 < len(fields) {
			port, err := strconv.Atoi(fields[index+1])
			if err != nil || port <= 0 || port > 65535 {
				return 0, fmt.Errorf("the inspected unit's ExecStart carries an unusable --port %q", fields[index+1])
			}
			return port, nil
		}
		if value, found := strings.CutPrefix(field, "--port="); found {
			port, err := strconv.Atoi(value)
			if err != nil || port <= 0 || port > 65535 {
				return 0, fmt.Errorf("the inspected unit's ExecStart carries an unusable --port %q", value)
			}
			return port, nil
		}
	}
	return 0, errors.New("the inspected unit's ExecStart names no --port, so the cold pull cannot be bound to it")
}

// RegistryLifecycleReport is the read-only fact sheet for one service unit.
type RegistryLifecycleReport struct {
	Unit             string `json:"unit"`
	Enabled          string `json:"enabled"`
	Active           string `json:"active"`
	ExecStart        string `json:"execStart"`
	WorkingDirectory string `json:"workingDirectory"`
	// ColdStart is not_verified until the manual reboot experiment has
	// written its evidence file. It is NEVER derived from is-enabled.
	ColdStart      string `json:"coldStart"`
	PathsPermanent bool   `json:"pathsPermanent"`
	// ColdStartDetail says exactly why the cold start is verified or not: which
	// evidence was read, and what about it matched or failed.
	ColdStartDetail string `json:"coldStartDetail,omitempty"`
}

// RegistryLifecycleInput is the read-only inspection request. The identity
// fields are optional: when a run record is known, the reboot evidence must
// agree with it as well.
type RegistryLifecycleInput struct {
	SystemctlBin        string
	Unit                string
	ClusterName         string
	ConfigDigest        string
	MaterialsLockDigest string
}

// InspectRegistryLifecycle runs the read-only systemctl queries the manual
// documents and assembles the report. systemctlBin is injectable so behaviour
// tests can drive this without a real machine; the invocation set is fixed:
// is-enabled, is-active, show -p ExecStart -p WorkingDirectory --value.
// Nothing here mutates the service.
func InspectRegistryLifecycle(ctx context.Context, input RegistryLifecycleInput) (RegistryLifecycleReport, error) {
	unit := strings.TrimSpace(input.Unit)
	if unit == "" {
		return RegistryLifecycleReport{}, errors.New("a service unit name is required")
	}
	systemctlBin := input.SystemctlBin
	if strings.TrimSpace(systemctlBin) == "" {
		systemctlBin = "systemctl"
	}
	report := RegistryLifecycleReport{Unit: unit}

	enabled, err := systemctlOutput(ctx, systemctlBin, "is-enabled", unit)
	if err != nil && enabled == "" {
		return RegistryLifecycleReport{}, errors.Wrapf(err, "systemctl is-enabled %s", unit)
	}
	report.Enabled = strings.TrimSpace(enabled)

	active, err := systemctlOutput(ctx, systemctlBin, "is-active", unit)
	if err != nil && active == "" {
		return RegistryLifecycleReport{}, errors.Wrapf(err, "systemctl is-active %s", unit)
	}
	report.Active = strings.TrimSpace(active)

	show, err := systemctlOutput(ctx, systemctlBin, "show", unit, "-p", "ExecStart", "-p", "WorkingDirectory", "--value")
	if err != nil {
		return RegistryLifecycleReport{}, errors.Wrapf(err, "systemctl show %s", unit)
	}
	// --value prints one property per line in query order: ExecStart, then
	// WorkingDirectory.
	lines := strings.Split(strings.TrimSpace(show), "\n")
	if len(lines) >= 1 {
		report.ExecStart = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		report.WorkingDirectory = strings.TrimSpace(lines[1])
	}

	// The unit only survives a reboot when every path it references is on
	// permanent storage.
	pathsPermanent := true
	for _, path := range []string{report.ExecStart, report.WorkingDirectory} {
		if path == "" {
			pathsPermanent = false
			continue
		}
		if err := ensurePermanentPath("registry unit path", firstWordPath(path)); err != nil {
			pathsPermanent = false
		}
	}
	report.PathsPermanent = pathsPermanent

	// Cold-start proof comes only from the manual reboot experiment's evidence,
	// read back and checked against THIS unit, host and boot — never from the
	// unit being enabled or from a file merely existing.
	detail, verified := validateRegistryRebootEvidence(unit, report.ExecStart,
		input.ClusterName, input.ConfigDigest, input.MaterialsLockDigest)
	report.ColdStartDetail = detail
	if verified {
		report.ColdStart = RegistryColdStartVerified
	} else {
		report.ColdStart = RegistryColdStartNotVerified
	}
	return report, nil
}

// firstWordPath extracts the binary path from a systemd ExecStart value
// ("path args..." — argv0 is quoted with escapes when it contains spaces).
func firstWordPath(execStart string) string {
	value := strings.TrimSpace(execStart)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
		quote := value[0]
		if end := strings.IndexByte(value[1:], quote); end >= 0 {
			return value[1 : end+1]
		}
	}
	if idx := strings.IndexAny(value, " \t"); idx >= 0 {
		return value[:idx]
	}
	return value
}

func systemctlOutput(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	return string(out), err
}

// registryNetworkBoundary is the documented exposure contract of the
// bootstrap registry (R14/A15 step 3). It is data, not enforcement: the node
// sits on the authorized internal experiment network; containerd keeps
// pulling from the approved registry address; exposing the unauthenticated
// HTTP source to a public network is forbidden.
const registryNetworkBoundary = "" +
	"The bootstrap registry is an UNAUTHENTICATED HTTP source bound to the " +
	"installer node on the authorized internal experiment network only. It " +
	"must never be exposed to a public network. containerd image references " +
	"keep the approved address from the run facts. This single-node registry " +
	"is not production HA; a Harbor handover is a separate plan that must not " +
	"stop or remove this service implicitly."

// RegistryNetworkBoundary exposes the contract for reports and docs.
func RegistryNetworkBoundary() string {
	return registryNetworkBoundary
}

// registryOwnershipNote documents the ownership rule the install path
// enforces: the unit is written by THIS run only (writeRegistryService
// refuses a pre-existing unit) and startRegistryService (which enables it)
// runs only immediately after that write. A same-named unit from another
// deployment is never adopted.
const registryOwnershipNote = "" +
	"The service unit is written by the current install run only " +
	"(writeRegistryService refuses a pre-existing unit) and startRegistry" +
	"Service - which enables it for boot - runs only immediately after that " +
	"write. A same-named unit from another deployment is never adopted."

// RegistryOwnershipNote exposes the ownership rule for reports and docs.
func RegistryOwnershipNote() string {
	return registryOwnershipNote
}
