/*
Copyright 2026 The KubeSphere Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ani

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// F-remediation G3 regressions (F04 success record, F05 real preflight).
// These exercise the REAL functions with real subprocess boundaries (fake
// systemctl/kubectl on PATH or injected), never an echo stand-in for the
// checker under test. Adapted from the audit's review_regression_test.go.example
// to the current interfaces.
// ---------------------------------------------------------------------------

func fWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// T26 / F05: CheckUnitInactive must actually query and must not invert.
func TestFRemediation_ActiveUnitMustBeRejected(t *testing.T) {
	// exit 0 (nil error) == active == refuse.
	if err := CheckUnitInactive("fixture.service", func(string, ...string) error { return nil }); err == nil {
		t.Fatal("systemctl is-active exit=0 must be interpreted as active, not safe to install")
	}
}

func TestFRemediation_InactiveUnitMustNotBeCalledActive(t *testing.T) {
	err := CheckUnitInactive("fixture.service", func(string, ...string) error { return fmt.Errorf("exit status 3") })
	if err != nil && strings.Contains(err.Error(), "is active") {
		t.Fatalf("inactive outcome wrongly reported as active: %v", err)
	}
}

func TestFRemediation_CheckUnitInactiveRejectsEmptyUnit(t *testing.T) {
	if err := CheckUnitInactive("", func(string, ...string) error { return nil }); err == nil {
		t.Fatal("an empty unit name must be refused")
	}
}

// T26 / F05: the full classifier must distinguish not-found (allow), active
// (refuse), external-loaded-inactive (refuse, no adopt), and query failure
// (refuse). Driven with a real fake systemctl binary.
func fWriteFakeSystemctl(t *testing.T, dir, loadState, activeState string, exitCode int) string {
	t.Helper()
	bin := filepath.Join(dir, "systemctl")
	body := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s\\n%%s\\n' %q %q\nexit %d\n", loadState, activeState, exitCode)
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestFRemediation_CheckRegistryUnitAbsent(t *testing.T) {
	cases := []struct {
		name        string
		load, act   string
		exit        int
		wantErr     bool
		errFragment string
	}{
		{"absent is allowed", "not-found", "inactive", 0, false, ""},
		{"active refused", "loaded", "active", 0, true, "is active"},
		{"external inactive refused (no adopt)", "loaded", "inactive", 0, true, "will not adopt"},
		{"empty load state refused", "", "", 0, true, "unproven"},
		{"query failure refused", "junk", "junk", 1, true, "query systemd state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := fWriteFakeSystemctl(t, t.TempDir(), tc.load, tc.act, tc.exit)
			err := checkRegistryUnitAbsent("fixture.service", bin)
			if tc.wantErr && err == nil {
				t.Fatalf("expected refusal, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected allow, got %v", err)
			}
			if tc.wantErr && tc.errFragment != "" && !strings.Contains(err.Error(), tc.errFragment) {
				t.Fatalf("error %q must contain %q", err.Error(), tc.errFragment)
			}
		})
	}
}

// T24 / F04: verification must refuse config-validation, non-success, mismatched
// and malformed records, and a short digest must return an error, not panic.
func TestFRemediation_LoadVerifyRunRejectsBadRecords(t *testing.T) {
	t.Run("config-validation record refused", func(t *testing.T) {
		dir := t.TempDir()
		fWriteJSON(t, filepath.Join(dir, "run.json"), RunManifest{
			ClusterName: "audit", ConfigDigest: strings.Repeat("a", 64),
			RecordKind: RecordKindConfigValidation,
		})
		if _, _, err := loadVerifyRun(context.Background(), VerifyInput{RunFile: filepath.Join(dir, "run.json")}); err == nil {
			t.Fatal("a config-validation record was accepted for verification")
		}
	})
	t.Run("install_failed phase refused", func(t *testing.T) {
		dir := t.TempDir()
		digest := strings.Repeat("a", 64)
		fWriteJSON(t, filepath.Join(dir, "run.json"), RunManifest{
			ClusterName: "audit", ConfigDigest: digest, RecordKind: RecordKindInstallSuccess,
			Result: ResultFailed, RunID: "r1", Identity: ManifestIdentity{SiteConfigDigest: digest, ClusterUID: "u", MaterialsLockDigest: strings.Repeat("b", 64)},
		})
		if _, _, err := loadVerifyRun(context.Background(), VerifyInput{RunFile: filepath.Join(dir, "run.json")}); err == nil {
			t.Fatal("a failed install result was accepted")
		}
	})
	t.Run("registry_content_verified is not a success marker", func(t *testing.T) {
		// F04: the mid-flight phase must no longer read as a verifiable install.
		dir := t.TempDir()
		digest := strings.Repeat("a", 64)
		fWriteJSON(t, filepath.Join(dir, "run.json"), RunManifest{
			ClusterName: "audit", ConfigDigest: digest, RecordKind: RecordKindInstallSuccess,
			Result: ResultRunning, RunID: "r1", Identity: ManifestIdentity{SiteConfigDigest: digest, ClusterUID: "u", MaterialsLockDigest: strings.Repeat("b", 64)},
		})
		fWriteJSON(t, filepath.Join(dir, "run-state.json"), InstallState{
			RunID: "r1", ClusterName: "audit", Phase: PhaseRegistryVerified, Result: ResultRunning, ConfigDigest: digest, SiteConfigDigest: digest,
		})
		if _, _, err := loadVerifyRun(context.Background(), VerifyInput{RunFile: filepath.Join(dir, "run.json")}); err == nil {
			t.Fatal("registry_content_verified phase was accepted as a successful install")
		}
	})
	t.Run("short digest returns error not panic", func(t *testing.T) {
		dir := t.TempDir()
		fWriteJSON(t, filepath.Join(dir, "run.json"), RunManifest{ClusterName: "audit", ConfigDigest: "x"})
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("malformed digest caused a panic instead of an input error: %v", r)
			}
		}()
		if _, _, err := loadVerifyRun(context.Background(), VerifyInput{RunFile: filepath.Join(dir, "run.json")}); err == nil {
			t.Fatal("malformed short digest was accepted")
		}
	})
	t.Run("state run mismatch refused", func(t *testing.T) {
		dir := t.TempDir()
		digest := strings.Repeat("a", 64)
		id := ManifestIdentity{SiteConfigDigest: digest, ClusterUID: "u", MaterialsLockDigest: strings.Repeat("b", 64)}
		fWriteJSON(t, filepath.Join(dir, "run.json"), RunManifest{
			ClusterName: "audit", ConfigDigest: digest, RecordKind: RecordKindInstallSuccess,
			Result: ResultSucceeded, RunID: "run-A", Identity: id,
		})
		fWriteJSON(t, filepath.Join(dir, "run-state.json"), InstallState{
			RunID: "run-B", ClusterName: "audit", Phase: PhaseSucceeded, Result: ResultSucceeded, ConfigDigest: digest,
		})
		if _, _, err := loadVerifyRun(context.Background(), VerifyInput{RunFile: filepath.Join(dir, "run.json")}); err == nil {
			t.Fatal("a state with a different runID was accepted")
		}
	})
}

// F04: a valid success record round-trips through ValidateSuccessRecord.
func TestFRemediation_SuccessRecordPositive(t *testing.T) {
	dir := t.TempDir()
	digest := strings.Repeat("a", 64)
	id := ManifestIdentity{SiteConfigDigest: digest, ClusterUID: "uid-kube-system", MaterialsLockDigest: strings.Repeat("b", 64), NodeCount: 3, ReadyNodes: []string{"n1", "n2", "n3"}}
	m := RunManifest{
		SchemaVersion: RunManifestSchemaVersion, ClusterName: "audit", ConfigDigest: digest,
		RecordKind: RecordKindInstallSuccess, Result: ResultSucceeded, RunID: "run-A", Identity: id,
	}
	fWriteJSON(t, filepath.Join(dir, "run.json"), m)
	fWriteJSON(t, filepath.Join(dir, "run-state.json"), InstallState{
		RunID: "run-A", ClusterName: "audit", Phase: PhaseSucceeded, Result: ResultSucceeded, ConfigDigest: digest,
	})
	manifest, runID, err := loadVerifyRun(context.Background(), VerifyInput{RunFile: filepath.Join(dir, "run.json")})
	if err != nil {
		t.Fatalf("a genuine install-success record must be consumable: %v", err)
	}
	if runID != "run-A" || manifest.Result != ResultSucceeded {
		t.Fatalf("unexpected consumable record: %+v runID=%s", manifest, runID)
	}
}

// F04: BuildInstallSuccessManifest refuses to forge identity.
func TestFRemediation_BuildInstallSuccessManifestGuards(t *testing.T) {
	base := RunManifest{ClusterName: "c", ConfigDigest: strings.Repeat("a", 64)}
	if _, err := BuildInstallSuccessManifest(base, InstallState{}, ManifestIdentity{}); err == nil {
		t.Fatal("an empty run id must not yield a success record")
	}
	st := InstallState{RunID: "r", Result: ResultSucceeded}
	if _, err := BuildInstallSuccessManifest(base, st, ManifestIdentity{SiteConfigDigest: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("a success record without a cluster uid must be refused")
	}
}

// T27 / F05: preflight refuses bad material BEFORE any change, with accurate
// failedCheck, and does not claim changes started.
func TestFRemediation_PreflightRejectsMalformedISOREcord(t *testing.T) {
	base := t.TempDir()
	artifact := filepath.Join(base, "artifact")
	r09ArtifactFixture(t, artifact, "helm\n", "iso\n")
	// Corrupt the ISO record to 3 fields (not exactly one "<name> <hash>").
	if err := os.WriteFile(filepath.Join(artifact, "config", "repository-iso-checksums.txt"),
		[]byte("a b c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := validConfig()
	report, err := RunPreflight(PreflightInput{
		RunID: "fr-iso", Cluster: &config, PackageRoot: artifact, ArtifactRoot: artifact,
		ReportBaseDir: filepath.Join(base, "reports"), HelmPath: filepath.Join(artifact, "bin", "helm"),
	})
	if err == nil {
		t.Fatal("a malformed ISO record must fail the preflight, not skip")
	}
	if report == nil || report.FailedCheck != "repository ISO record" {
		t.Fatalf("failedCheck = %q, want repository ISO record", func() string {
			if report == nil {
				return "<nil>"
			}
			return report.FailedCheck
		}())
	}
	if report.ChangesStarted {
		t.Fatal("a preflight material failure must not claim changesStarted")
	}
}

func TestFRemediation_PreflightRejectsMissingHauler(t *testing.T) {
	base := t.TempDir()
	artifact := filepath.Join(base, "artifact")
	r09ArtifactFixture(t, artifact, "helm\n", "iso\n")
	if err := os.Remove(filepath.Join(artifact, "bin", "hauler")); err != nil {
		t.Fatal(err)
	}
	config := validConfig()
	report, err := RunPreflight(PreflightInput{
		RunID: "fr-hauler", Cluster: &config, PackageRoot: artifact, ArtifactRoot: artifact,
		ReportBaseDir: filepath.Join(base, "reports"), HelmPath: filepath.Join(artifact, "bin", "helm"),
		HaulerPath: filepath.Join(artifact, "bin", "hauler"),
	})
	if err == nil {
		t.Fatal("a missing hauler must fail preflight before any change")
	}
	if report == nil || report.FailedCheck != "required artifact files" {
		t.Fatalf("failedCheck = %q, want required artifact files", func() string {
			if report == nil {
				return "<nil>"
			}
			return report.FailedCheck
		}())
	}
	if report.ChangesStarted {
		t.Fatal("preflight must never claim changesStarted")
	}
}

// F04: captureClusterIdentity binds the record to the real cluster and refuses
// a partially-Ready node set, so "config unchanged" can never masquerade as
// "cluster verified".
func TestFRemediation_CaptureClusterIdentityRequiresReadyNodes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
args="$*"
case "$args" in
  *"get namespace kube-system"*"metadata.uid"*) echo "uid-cluster-real"; exit 0 ;;
  *"get nodes"*"-o json"*)
    if [ "${FR_ONE_NOTREADY:-}" = "1" ]; then
      printf '{"items":[{"metadata":{"name":"n1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"n2"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
    else
      printf '{"items":[{"metadata":{"name":"n1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"n2"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    fi
    exit 0 ;;
esac
echo "unsupported $args" >&2; exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANI_VERIFY_KUBECTL", bin)

	id := ManifestIdentity{}
	if err := captureClusterIdentity(context.Background(), filepath.Join(dir, "kubeconfig"), &id); err != nil {
		t.Fatalf("all-ready nodes must bind: %v", err)
	}
	if id.ClusterUID != "uid-cluster-real" || id.NodeCount != 2 || len(id.ReadyNodes) != 2 {
		t.Fatalf("identity not captured correctly: %+v", id)
	}
	t.Setenv("FR_ONE_NOTREADY", "1")
	id2 := ManifestIdentity{}
	if err := captureClusterIdentity(context.Background(), filepath.Join(dir, "kubeconfig"), &id2); err == nil {
		t.Fatal("a partially-Ready node set must be refused for the success record")
	}
}
