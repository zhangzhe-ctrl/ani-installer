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
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// RegistryLifecycleReport is the read-only fact sheet for one service unit.
type RegistryLifecycleReport struct {
	Unit            string `json:"unit"`
	Enabled         string `json:"enabled"`
	Active          string `json:"active"`
	ExecStart       string `json:"execStart"`
	WorkingDirectory string `json:"workingDirectory"`
	// ColdStart is not_verified until the manual reboot experiment has
	// written its evidence file. It is NEVER derived from is-enabled.
	ColdStart      string `json:"coldStart"`
	PathsPermanent bool   `json:"pathsPermanent"`
}

// InspectRegistryLifecycle runs the read-only systemctl queries the manual
// documents and assembles the report. systemctlBin is injectable so behaviour
// tests can drive this without a real machine; the invocation set is fixed:
// is-enabled, is-active, show -p ExecStart -p WorkingDirectory --value.
// Nothing here mutates the service.
func InspectRegistryLifecycle(ctx context.Context, systemctlBin, unit string) (RegistryLifecycleReport, error) {
	if strings.TrimSpace(unit) == "" {
		return RegistryLifecycleReport{}, errors.New("a service unit name is required")
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

	// Cold-start proof comes only from the manual reboot experiment's
	// evidence file — never from the unit being enabled.
	if _, err := os.Stat(RegistryRebootEvidenceFile); err == nil {
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
