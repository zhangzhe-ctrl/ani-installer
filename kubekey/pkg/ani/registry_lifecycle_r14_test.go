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
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R14 behaviour tests: the bootstrap registry lifecycle. A state-driven fake
// systemctl stands in for the real machine; the cold-start contract is that
// is-enabled/is-active can never be reported as a verified reboot.
// ---------------------------------------------------------------------------

// r14FakeSystemctl writes a fake systemctl that records every invocation and
// answers the three read-only queries the inspector makes.
func r14FakeSystemctl(t *testing.T, binDir string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
log="${FAKE_SYSTEMCTL_LOG:?}"
printf '%s\n' "$*" >> "$log"
args="$*"
case "$args" in
  "is-enabled "*) echo "${FAKE_IS_ENABLED:-enabled}"; exit 0 ;;
  "is-active "*) echo "${FAKE_IS_ACTIVE:-active}"; exit 0 ;;
  *"show"*"--value"*)
    printf '%s\n' "${FAKE_EXEC_START:-/var/lib/ani-installer/ani-lab/bin/hauler store serve registry}"
    printf '%s\n' "${FAKE_WORKDIR:-/var/lib/ani-installer/ani-lab/work}"
    exit 0 ;;
esac
# Any other verb (start/stop/enable/disable/restart...) is recorded so tests
# can prove the inspector never mutates.
echo "MUTATING: $args" >> "$log"
exit 0
`
	path := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	return path
}

func r14LifecycleSetup(t *testing.T) (binDir, logPath string) {
	t.Helper()
	baseDir := t.TempDir()
	binDir = filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath = filepath.Join(baseDir, "systemctl-calls.log")
	t.Setenv("FAKE_SYSTEMCTL_LOG", logPath)
	r14FakeSystemctl(t, binDir)
	return binDir, logPath
}

func r14ReadLog(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}
	return string(data)
}

// T-R14-01: the install lifecycle enables the unit for boot, and the enable
// only ever targets the unit THIS run wrote. The unit itself carries
// permanent paths.
func TestRegistryLifecycleOwnership(t *testing.T) {
	t.Run("enable sequence targets only this run's unit after a fresh write", func(t *testing.T) {
		binDir, logPath := r14LifecycleSetup(t)
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

		unitDir := t.TempDir()
		oldUnitDir := systemdUnitDir
		systemdUnitDir = unitDir
		t.Cleanup(func() { systemdUnitDir = oldUnitDir })

		workRoot := "/var/lib/ani-installer/ani-lab/work"
		hauler := "/var/lib/ani-installer/ani-lab/bin/hauler"
		store := filepath.Join(workRoot, "hauler-store")
		registryData := filepath.Join(workRoot, "registry-data")
		// The unit file must not pre-exist: fresh-run ownership.
		unitPath := "/etc/systemd/system/" + serviceUnitName
		if _, err := os.Stat(unitPath); err == nil {
			t.Skipf("the real unit %s exists on this machine; ownership test needs a clean host", unitPath)
		}
		if err := writeRegistryService(workRoot, hauler, store, registryData, 5000); err != nil {
			t.Fatalf("fresh unit write failed: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(unitPath) })

		if err := startRegistryService(context.Background(), os.Stdout); err != nil {
			t.Fatalf("lifecycle sequence failed: %v", err)
		}
		log := r14ReadLog(t, logPath)
		var seq []string
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			// The fake echoes "MUTATING:" for every fall-through verb; the
			// first line of each call is the recorded argv.
			if line != "" && !strings.HasPrefix(line, "MUTATING:") {
				seq = append(seq, line)
			}
		}
		// daemon-reload, enable <unit>, restart <unit> — in that order, and
		// every service-targeted verb names exactly this run's unit.
		if len(seq) != 3 ||
			seq[0] != "daemon-reload" ||
			seq[1] != "enable "+serviceUnitName ||
			seq[2] != "restart "+serviceUnitName {
			t.Fatalf("lifecycle sequence wrong:\n%s", log)
		}

		unit, err := os.ReadFile(filepath.Join(unitDir, serviceUnitName))
		if err != nil {
			t.Fatalf("read written unit: %v", err)
		}
		for _, want := range []string{
			"ExecStart=" + hauler + " store serve registry --port 5000 --directory " + registryData,
			"WorkingDirectory=" + workRoot,
			"--readonly=true",
			"WantedBy=multi-user.target",
		} {
			if !strings.Contains(string(unit), want) {
				t.Fatalf("written unit missing %q:\n%s", want, unit)
			}
		}
	})

	t.Run("a foreign unit with the same name is never adopted", func(t *testing.T) {
		binDir, logPath := r14LifecycleSetup(t)
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
		unitDir := t.TempDir()
		oldUnitDir := systemdUnitDir
		systemdUnitDir = unitDir
		t.Cleanup(func() { systemdUnitDir = oldUnitDir })
		unitPath := filepath.Join(unitDir, serviceUnitName)
		if _, err := os.Stat(unitPath); err == nil {
			t.Skipf("the real unit %s exists on this machine; ownership test needs a clean host", unitPath)
		}
		if err := os.WriteFile(unitPath, []byte("# a foreign unit\n"), 0o644); err != nil {
			t.Fatalf("write foreign unit: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(unitPath) })

		err := writeRegistryService("/var/lib/ani-installer/other/work",
			"/var/lib/ani-installer/other/bin/hauler",
			"/var/lib/ani-installer/other/work/hauler-store",
			"/var/lib/ani-installer/other/work/registry-data", 5000)
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("a foreign unit must be refused, got %v", err)
		}
		// The refusal happens before any lifecycle action: the fake systemctl
		// was never invoked, so a foreign service is never enabled/restarted.
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("no systemctl action may run for a foreign unit:\n%s", r14ReadLog(t, logPath))
		}
	})

	t.Run("registry paths under temporary directories are refused", func(t *testing.T) {
		for _, tc := range []struct{ field, path string }{
			{"hauler binary", "/tmp/ani/bin/hauler"},
			{"registry data", "/var/tmp/ani/registry-data"},
			{"hauler store", "/run/ani/hauler-store"},
			{"working directory", "/dev/shm/ani/work"},
		} {
			err := writeRegistryService(tc.path, tc.path, tc.path, tc.path, 5000)
			if err == nil || !strings.Contains(err.Error(), "permanent storage") {
				t.Fatalf("%s under a temporary directory must be refused, got %v", tc.field, err)
			}
		}
	})

	// T-R14-03 (half): nothing in the codebase stops, disables or removes the
	// bootstrap service — the inspector is read-only by construction.
	t.Run("lifecycle inspection never mutates the service", func(t *testing.T) {
		binDir, logPath := r14LifecycleSetup(t)
		report, err := InspectRegistryLifecycle(context.Background(), filepath.Join(binDir, "systemctl"), serviceUnitName)
		if err != nil {
			t.Fatalf("inspect failed: %v", err)
		}
		log := r14ReadLog(t, logPath)
		if strings.Contains(log, "MUTATING:") {
			t.Fatalf("the inspector must never mutate the service:\n%s", log)
		}
		for _, want := range []string{"is-enabled ", "is-active ", "show "} {
			if !strings.Contains(log, want) {
				t.Fatalf("the inspector must run the documented read-only queries (missing %q):\n%s", want, log)
			}
		}
		if report.Enabled != "enabled" || report.Active != "active" {
			t.Fatalf("unexpected state answers: %+v", report)
		}
	})
}

// T-R14-02/04: the report carries the real unit state, but the cold start is
// not_verified until the manual reboot experiment writes its evidence —
// is-enabled/is-active can never be substituted for it.
func TestRegistryColdStartReporting(t *testing.T) {
	t.Run("enabled and active still report cold start as not_verified", func(t *testing.T) {
		binDir, _ := r14LifecycleSetup(t)
		t.Setenv("RegistryRebootEvidenceFile", "")
		t.Setenv("FAKE_IS_ENABLED", "enabled")
		t.Setenv("FAKE_IS_ACTIVE", "active")
		report, err := InspectRegistryLifecycle(context.Background(), filepath.Join(binDir, "systemctl"), serviceUnitName)
		if err != nil {
			t.Fatalf("inspect failed: %v", err)
		}
		if report.ColdStart != RegistryColdStartNotVerified {
			t.Fatalf("without real reboot evidence the cold start must be %q, got %q",
				RegistryColdStartNotVerified, report.ColdStart)
		}
		if !report.PathsPermanent {
			t.Fatalf("permanent unit paths must be recognised: %+v", report)
		}
		if report.ExecStart == "" || report.WorkingDirectory == "" {
			t.Fatalf("the report must carry the unit's ExecStart/WorkingDirectory: %+v", report)
		}
	})

	t.Run("evidence file is the only path to verified", func(t *testing.T) {
		binDir, _ := r14LifecycleSetup(t)
		t.Setenv("FAKE_IS_ENABLED", "enabled")
		t.Setenv("FAKE_IS_ACTIVE", "active")
		evidenceFile := filepath.Join(t.TempDir(), "registry-reboot-evidence.json")
		oldEvidence := RegistryRebootEvidenceFile
		RegistryRebootEvidenceFile = evidenceFile
		t.Cleanup(func() { RegistryRebootEvidenceFile = oldEvidence })
		// No evidence file: not_verified.
		report, err := InspectRegistryLifecycle(context.Background(), filepath.Join(binDir, "systemctl"), serviceUnitName)
		if err != nil {
			t.Fatalf("inspect failed: %v", err)
		}
		if report.ColdStart != RegistryColdStartNotVerified {
			t.Fatalf("cold start must start as not_verified, got %q", report.ColdStart)
		}
		// The manual experiment writes its evidence file; only then verified.
		if err := os.WriteFile(evidenceFile, []byte(`{"unit":"ani-image-registry.service","rebootedAt":"2026-09-24T00:00:00Z","coldPullBytes":1048576}`), 0o600); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		report, err = InspectRegistryLifecycle(context.Background(), filepath.Join(binDir, "systemctl"), serviceUnitName)
		if err != nil {
			t.Fatalf("inspect failed: %v", err)
		}
		if report.ColdStart != RegistryColdStartVerified {
			t.Fatalf("with reboot evidence the cold start must be verified, got %q", report.ColdStart)
		}
	})

	t.Run("a unit pointing into temporary storage is flagged", func(t *testing.T) {
		binDir, _ := r14LifecycleSetup(t)
		t.Setenv("FAKE_EXEC_START", "/tmp/ani/bin/hauler store serve registry")
		t.Setenv("FAKE_WORKDIR", "/tmp/ani/work")
		report, err := InspectRegistryLifecycle(context.Background(), filepath.Join(binDir, "systemctl"), serviceUnitName)
		if err != nil {
			t.Fatalf("inspect failed: %v", err)
		}
		if report.PathsPermanent {
			t.Fatalf("temporary paths must flag PathsPermanent=false: %+v", report)
		}
	})

	// T-R14-03: the documented boundary — Harbor selection (none exists in
	// code) has no implicit stop/delete of the bootstrap service, and the
	// network exposure contract is spelled out.
	t.Run("boundary contracts are explicit", func(t *testing.T) {
		boundary := RegistryNetworkBoundary()
		for _, want := range []string{
			"UNAUTHENTICATED HTTP",
			"authorized internal experiment network",
			"must never be exposed to a public network",
			"not production HA",
			"separate plan",
		} {
			if !strings.Contains(boundary, want) {
				t.Fatalf("the network boundary contract must state %q: %s", want, boundary)
			}
		}
		ownership := RegistryOwnershipNote()
		for _, want := range []string{
			"current install run only",
			"refuses a pre-existing unit",
			"never adopted",
		} {
			if !strings.Contains(ownership, want) {
				t.Fatalf("the ownership contract must state %q: %s", want, ownership)
			}
		}
	})
}
