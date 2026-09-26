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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R13 behaviour tests: the verify dispatcher is driven end to end with a run
// record, stub component verify scripts and a state-driven fake kubectl.
// Nothing touches a real cluster.
// ---------------------------------------------------------------------------

// r13RunRecord writes run.json (and optionally run-state.json) for a run.
// Under the F04 contract a record is consumable only if it is an
// install-success record with a 64-hex site digest and a populated identity;
// the install state must report the terminal succeeded phase/result. `phase`
// drives the state: PhaseSucceeded → a consumable success; PhaseInstallFailed or
// a mid-flight phase → refused.
func r13RunRecord(t *testing.T, dir string, withState bool, phase string, components ...string) (string, string) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	runID := "ani-ani-lab-20260924-130000"
	// A record's consumable result mirrors the install state's terminal phase.
	recordResult := ResultSucceeded
	statePhase := phase
	if statePhase == "" {
		statePhase = PhaseSucceeded
	}
	switch statePhase {
	case PhaseSucceeded:
		recordResult = ResultSucceeded
	case PhaseInstallFailed:
		recordResult = ResultFailed
	default:
		// installing / registry_content_verified / preflight_failed: never a
		// success marker under F04.
		recordResult = ResultRunning
	}
	manifest := RunManifest{
		SchemaVersion:      RunManifestSchemaVersion,
		RecordKind:         RecordKindInstallSuccess,
		RunID:              runID,
		Result:             recordResult,
		ConfigDigest:       digest,
		ClusterName:        "ani-lab",
		Profile:            "full",
		NetworkStack:       "kcn",
		Components:         components,
		StorageClass:       "ani-block",
		MaterialsValidated: true,
		Identity: ManifestIdentity{
			SiteConfigDigest:    digest,
			MaterialsLockDigest: strings.Repeat("b", 64),
			ClusterUID:          "uid-kube-system-audit",
			NodeCount:           3,
			ReadyNodes:          []string{"node1", "node2", "node3"},
		},
	}
	if len(manifest.Components) == 0 {
		manifest.Components = nil
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	runFile := filepath.Join(dir, "run.json")
	if err := os.WriteFile(runFile, encoded, 0o600); err != nil {
		t.Fatalf("write run.json: %v", err)
	}
	if !withState {
		return runFile, ""
	}
	state := InstallState{
		SchemaVersion:    InstallStateSchemaVersion,
		RunID:            runID,
		StartedAt:        "2026-09-24T13:00:00Z",
		Phase:            statePhase,
		Result:           recordResult,
		ChangesStarted:   true,
		RemoteResult:     RemoteResultDeterministic,
		ClusterName:      "ani-lab",
		Targets:          []string{"node1", "node2", "node3"},
		SiteConfigDigest: digest,
		ConfigDigest:     digest,
	}
	encodedState, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}
	stateFile := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(stateFile, encodedState, 0o600); err != nil {
		t.Fatalf("write run-state.json: %v", err)
	}
	return runFile, stateFile
}

// r13FakeKubectl writes a state-driven fake kubectl. Knobs (all env):
//
//	FAKE_STICKY_POD_UID=1  delete does NOT change the pod UID (no recreation)
//	FAKE_NEW_PVC_UID=x     delete also replaces the PVC object with UID x
//	FAKE_NOT_READY=1       the recreated pod reports Ready=False (Running but
//	                       not Ready, which the acceptance must reject)
//	FAKE_DATA_LOST=1       the committed SQL read-back returns empty (the row
//	                       did not survive the recreation)
//	FAKE_PG_MISMATCH=x     the read-back returns x instead of the token
func r13FakeKubectl(t *testing.T, binDir, stateDir string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
state="${FAKE_STATE_DIR:?}"
printf '%s\n' "$*" >> "$state/kubectl-calls.log"
args="$*"
case "$args" in
  *"get namespace kube-system"*)
    # Consuming a components execution record re-reads the live cluster
    # fingerprint before it trusts the record's base binding.
    printf '%s\n' "${FAKE_CLUSTER_UID:-uid-kube-system-audit}"; exit 0 ;;
  *"get nodes"*"-o json"*)
    printf '%s\n' '{"items":[{"metadata":{"name":"node1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node2"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node3"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    exit 0 ;;
  *"get pod"*"ownerReferences"*)
    # derive the owning controller from the pod name (postgresql-0 -> postgresql)
    name="$(printf '%s' "$args" | sed -n 's/.*get pod \([a-z0-9-]*\).*/\1/p')"
    printf '%s\n' "${name%-0}"; exit 0 ;;
  *"get pod"*"metadata.uid"*)
    n=0; [ -f "$state/uid-reads" ] && n="$(cat "$state/uid-reads")"
    n=$((n+1)); printf '%s\n' "$n" > "$state/uid-reads"
    if [ -n "${FAKE_REPLACE_UID_ON_REREAD:-}" ] && [ "$n" -ge 2 ]; then
      echo "uid-SOMEONE-ELSE-replaced"; exit 0
    fi
    cat "$state/pod-uid" 2>/dev/null; exit 0 ;;
  *"get pod"*"Ready"*status*|"get pod"*conditions*Ready*)
    if [ -n "${FAKE_NOT_READY:-}" ]; then echo "False"; else echo "True"; fi; exit 0 ;;
  *"get pvc"*"metadata.uid"*)
    cat "$state/pvc-uid" 2>/dev/null; exit 0 ;;
  *"get pvc"*"volumeName"*)
    echo "pv-acceptance-0"; exit 0 ;;
  *"delete pod"*)
    if [ -z "${FAKE_STICKY_POD_UID:-}" ]; then
      printf 'uid-new-%s\n' "$(date +%s%N)" > "$state/pod-uid"
    fi
    if [ -n "${FAKE_NEW_PVC_UID:-}" ]; then
      printf '%s\n' "$FAKE_NEW_PVC_UID" > "$state/pvc-uid"
    fi
    printf 'delete\n' >> "$state/deleted"
    exit 0 ;;
  *"psql"*"SELECT"*"ani_acceptance"*)
    tok="$(printf '%s' "$args" | sed -n "s/.*WHERE k='\([a-z0-9-]*\)'.*/\1/p")"
    if [ -n "${FAKE_DATA_LOST:-}" ]; then echo ""; exit 0; fi
    if [ -n "${FAKE_PG_MISMATCH:-}" ]; then echo "$FAKE_PG_MISMATCH"; exit 0; fi
    echo "$tok"; exit 0 ;;
  *"psql"*"INSERT"*|"printf"*">"*"marker"*)
    exit 0 ;;
esac
echo "fake kubectl: unsupported $args" >&2
exit 2
`
	path := filepath.Join(binDir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "pod-uid"), []byte("uid-old-postgresql-0"), 0o600); err != nil {
		t.Fatalf("seed pod uid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "pvc-uid"), []byte("uid-pvc-data-postgresql-0"), 0o600); err != nil {
		t.Fatalf("seed pvc uid: %v", err)
	}
	return path
}

// r13StubScript writes a smoke verify script that records its invocation.
func r13StubScript(t *testing.T, scriptDir, component, marker string) {
	t.Helper()
	dir := filepath.Join(scriptDir, component)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	script := "#!/usr/bin/env bash\nmkdir -p \"$ANI_VERIFY_OUTPUT_DIR\"\nprintf '%s\\n' \"$*\" >> \"$FAKE_SCRIPT_LOG\"\nprintf '%s\\n' " + marker + " > \"$ANI_VERIFY_OUTPUT_DIR/result.txt\"\n"
	if err := os.WriteFile(filepath.Join(dir, "verify.sh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
}

func r13ReadReport(t *testing.T, path string) VerifyReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report %s: %v", path, err)
	}
	var report VerifyReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return report
}

// r13Prepare wires a base directory with the fake kubectl on PATH, seeded
// state and the fast recreation timeout.
func r13Prepare(t *testing.T) (baseDir, stateDir string) {
	t.Helper()
	baseDir = t.TempDir()
	stateDir = filepath.Join(baseDir, "state")
	binDir := filepath.Join(baseDir, "bin")
	for _, dir := range []string{stateDir, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	r13FakeKubectl(t, binDir, stateDir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("ANI_VERIFY_POD_RECREATE_TIMEOUT", "3s")
	// The fake kubectl child processes read their state from here.
	t.Setenv("FAKE_STATE_DIR", stateDir)
	// The acceptance ledger + shared product lock live in a canonical state
	// dir that is independent of --output; tests point both at a temp dir.
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", filepath.Join(baseDir, "acceptance-state"))
	t.Setenv("ANI_INSTALL_LOCK", filepath.Join(baseDir, "acceptance-state", "ani-install.lock"))
	return baseDir, stateDir
}

// r13FindAcceptanceReport globs the run-scoped, scope-keyed acceptance report
// (its exact name embeds a hash of the selected targets, so tests must not
// hard-code it).
func r13FindAcceptanceReport(t *testing.T, outDir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(outDir, "verify-acceptance-*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no acceptance report found in %s: %v", outDir, err)
	}
	return matches[0]
}

// T-R13-01: install and smoke trajectories never recreate a component Pod,
// and acceptance without the explicit flag is refused before anything runs.
func TestVerifyDispatcherLevels(t *testing.T) {
	t.Run("smoke runs read-only scripts and never deletes a pod", func(t *testing.T) {
		baseDir, stateDir := r13Prepare(t)
		scriptDir := filepath.Join(baseDir, "ani-scripts")
		outDir := filepath.Join(baseDir, "verify-out")
		r13StubScript(t, scriptDir, "cert-manager", "CERT-OK")
		r13StubScript(t, scriptDir, "valkey", "VALKEY-OK")
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "cert-manager", "valkey")

		input := VerifyInput{
			RunFile:    runFile,
			StateFile:  stateFile,
			Level:      VerifyLevelSmoke,
			ScriptDir:  scriptDir,
			Output:     outDir,
			Kubeconfig: filepath.Join(baseDir, "kubeconfig"),
		}
		if err := RunVerify(context.Background(), input, os.Stdout); err != nil {
			t.Fatalf("smoke verify failed: %v", err)
		}
		report := r13ReadReport(t, filepath.Join(outDir, "verify-smoke-ani-ani-lab-20260924-130000.json"))
		if report.Overall != VerifyStatusPass || len(report.Results) != 2 {
			t.Fatalf("unexpected smoke report: %+v", report)
		}
		// The smoke trajectory must not issue a single kubectl call, let
		// alone a pod deletion.
		if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
			t.Fatal("smoke deleted something")
		}
		if _, err := os.Stat(filepath.Join(stateDir, "kubectl-calls.log")); !os.IsNotExist(err) {
			t.Fatal("smoke called kubectl at all")
		}
		// Smoke evidence is scoped to the subject it verified, so a base run and
		// a components execution can never overwrite each other's evidence.
		for _, component := range []string{"cert-manager", "valkey"} {
			dir := filepath.Join(outDir, "smoke-ani-ani-lab-20260924-130000-"+component)
			if _, err := os.Stat(filepath.Join(dir, "result.txt")); err != nil {
				t.Fatalf("smoke evidence for %s missing at %s: %v", component, dir, err)
			}
		}
	})

	t.Run("acceptance without the flag is refused", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		outDir := filepath.Join(baseDir, "verify-out")
		input := VerifyInput{
			RunFile:   runFile,
			StateFile: stateFile,
			Level:     VerifyLevelAcceptance,
			Output:    outDir,
		}
		err := RunVerify(context.Background(), input, os.Stdout)
		if err == nil {
			t.Fatal("acceptance without --allow-pod-recreate must be refused")
		}
		if !strings.Contains(err.Error(), "--allow-pod-recreate") {
			t.Fatalf("the refusal must name the flag: %v", err)
		}
		entries, _ := os.ReadDir(outDir)
		if len(entries) != 0 {
			t.Fatalf("a refused run must not write a report: %v", entries)
		}
	})

	t.Run("a failed install record refuses verification", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseInstallFailed, "postgresql")
		input := VerifyInput{
			RunFile:   runFile,
			StateFile: stateFile,
			Level:     VerifyLevelSmoke,
			Output:    filepath.Join(baseDir, "out"),
		}
		err := RunVerify(context.Background(), input, os.Stdout)
		if err == nil || !strings.Contains(err.Error(), "requires a successful install") {
			t.Fatalf("a failed install must refuse verification, got %v", err)
		}
	})

	t.Run("only outside the run record is rejected", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, _ := r13RunRecord(t, baseDir, false, "", "cert-manager")
		input := VerifyInput{
			RunFile:   runFile,
			Level:     VerifyLevelSmoke,
			Only:      []string{"milvus"},
			Output:    filepath.Join(baseDir, "out"),
			ScriptDir: filepath.Join(baseDir, "scripts"),
		}
		err := RunVerify(context.Background(), input, os.Stdout)
		if err == nil || !strings.Contains(err.Error(), "outside what this run record attests") {
			t.Fatalf("--only beyond the record must be rejected, got %v", err)
		}
	})
}

// T-R13-02 + T-R13-03: the acceptance engine judges the recreation by UIDs,
// not names, and the PVC/data contracts decide pass or fail.
func TestVerifyAcceptanceRecreation(t *testing.T) {
	t.Run("same name with a new UID is a recreation; committed data and PVC survive", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		outDir := filepath.Join(baseDir, "verify-out")

		input := VerifyInput{
			RunFile:          runFile,
			StateFile:        stateFile,
			Level:            VerifyLevelAcceptance,
			AllowPodRecreate: true,
			Output:           outDir,
			Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
		}
		if err := RunVerify(context.Background(), input, os.Stdout); err != nil {
			t.Fatalf("acceptance should pass on a clean recreation: %v", err)
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, outDir))
		if report.Overall != VerifyStatusPass || len(report.Results) != 1 {
			t.Fatalf("unexpected report: %+v", report)
		}
		result := report.Results[0]
		if result.Status != VerifyStatusPass {
			t.Fatalf("postgresql acceptance must pass: %+v", result)
		}
		if result.Evidence["oldPodUID"] == result.Evidence["newPodUID"] {
			t.Fatalf("the report must record distinct pod UIDs: %+v", result.Evidence)
		}
		if result.Evidence["oldPVCUID"] != result.Evidence["newPVCUID"] {
			t.Fatalf("the PVC object must be unchanged: %+v", result.Evidence)
		}
		if result.Evidence["controller"] != "StatefulSet/postgresql" {
			t.Fatalf("the controller must be recorded: %+v", result.Evidence)
		}
	})

	t.Run("same name and same UID is not a recreation", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		t.Setenv("FAKE_STICKY_POD_UID", "1")
		t.Setenv("ANI_VERIFY_POD_RECREATE_TIMEOUT", "2s")
		input := VerifyInput{
			RunFile:          runFile,
			StateFile:        stateFile,
			Level:            VerifyLevelAcceptance,
			AllowPodRecreate: true,
			Output:           filepath.Join(baseDir, "verify-out"),
			Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
		}
		if err := RunVerify(context.Background(), input, os.Stdout); err == nil {
			t.Fatal("an identical UID must fail the acceptance")
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
		if !strings.Contains(report.Results[0].Detail, "come back Ready with a new UID") {
			t.Fatalf("the failure must be the recreation assertion: %+v", report.Results[0])
		}
	})

	t.Run("a Running-but-not-Ready pod never passes the recreation", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		t.Setenv("FAKE_NOT_READY", "1")
		t.Setenv("ANI_VERIFY_POD_RECREATE_TIMEOUT", "2s")
		input := VerifyInput{
			RunFile:          runFile,
			StateFile:        stateFile,
			Level:            VerifyLevelAcceptance,
			AllowPodRecreate: true,
			Output:           filepath.Join(baseDir, "verify-out"),
			Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
		}
		if err := RunVerify(context.Background(), input, os.Stdout); err == nil {
			t.Fatal("a recreated-but-not-Ready pod must fail the acceptance")
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
		if !strings.Contains(report.Results[0].Detail, "Running-but-not-Ready is never a pass") {
			t.Fatalf("the failure must record the Ready requirement: %+v", report.Results[0])
		}
	})

	t.Run("committed PostgreSQL row lost after the rebuild fails", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		t.Setenv("FAKE_DATA_LOST", "1")
		input := VerifyInput{
			RunFile:          runFile,
			StateFile:        stateFile,
			Level:            VerifyLevelAcceptance,
			AllowPodRecreate: true,
			Output:           filepath.Join(baseDir, "verify-out"),
			Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
		}
		err := RunVerify(context.Background(), input, os.Stdout)
		if err == nil {
			t.Fatal("a lost committed row must fail the acceptance (a marker file never proves persistence)")
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
		if report.Results[0].Status != VerifyStatusFailed ||
			!strings.Contains(report.Results[0].Detail, "did not survive the recreation") {
			t.Fatalf("the data-loss failure must be recorded: %+v", report.Results[0])
		}
	})

	t.Run("a mismatched committed value fails even though the marker file survived", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		t.Setenv("FAKE_PG_MISMATCH", "some-other-value")
		input := VerifyInput{
			RunFile:          runFile,
			StateFile:        stateFile,
			Level:            VerifyLevelAcceptance,
			AllowPodRecreate: true,
			Output:           filepath.Join(baseDir, "verify-out"),
			Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
		}
		if err := RunVerify(context.Background(), input, os.Stdout); err == nil {
			t.Fatal("a mismatched committed value must fail the acceptance")
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
		if !strings.Contains(report.Results[0].Detail, "did not survive the recreation") {
			t.Fatalf("the mismatch must be recorded as a persistence failure: %+v", report.Results[0])
		}
	})

	t.Run("a replaced PVC can never pass", func(t *testing.T) {
		baseDir, _ := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		t.Setenv("FAKE_NEW_PVC_UID", "uid-pvc-REPLACED")
		input := VerifyInput{
			RunFile:          runFile,
			StateFile:        stateFile,
			Level:            VerifyLevelAcceptance,
			AllowPodRecreate: true,
			Output:           filepath.Join(baseDir, "verify-out"),
			Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
		}
		err := RunVerify(context.Background(), input, os.Stdout)
		if err == nil {
			t.Fatal("a replaced PVC must fail the acceptance")
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
		if !strings.Contains(report.Results[0].Detail, "PVC was replaced") {
			t.Fatalf("the PVC failure must be recorded: %+v", report.Results[0])
		}
	})
}

// T-R13-04 + T-R13-05: one failure stops every further mutation, and the
// acceptance result coexists with the install record instead of overwriting it.
func TestVerifyAcceptanceStopAndRecords(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql", "nats")
	// postgresql's committed row is lost after the rebuild; nats must never be
	// touched (first failure stops all further mutation).
	t.Setenv("FAKE_DATA_LOST", "1")
	outDir := filepath.Join(baseDir, "verify-out")
	input := VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           outDir,
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}
	err := RunVerify(context.Background(), input, os.Stdout)
	if err == nil {
		t.Fatal("the failing first acceptance must fail the run")
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, outDir))
	if len(report.Results) != 2 {
		t.Fatalf("both components must be recorded: %+v", report.Results)
	}
	if report.Results[0].Status != VerifyStatusFailed {
		t.Fatalf("postgresql must carry the failure: %+v", report.Results[0])
	}
	if report.Results[1].Status != VerifyStatusNotRun ||
		!strings.Contains(report.Results[1].Detail, "no further mutation") {
		t.Fatalf("nats must be recorded not_run after the first failure: %+v", report.Results[1])
	}
	deleted, err := os.ReadFile(filepath.Join(stateDir, "deleted"))
	if err != nil {
		t.Fatalf("read delete log: %v", err)
	}
	if strings.Count(strings.TrimSpace(string(deleted)), "delete") != 1 {
		t.Fatalf("exactly one planned recreation may happen, log:\n%s", deleted)
	}

	// T-R13-05: the install record keeps its own success while the acceptance
	// failure stands; a second identical acceptance attempt is refused by the
	// durable ledger (independent of --output), not by the report file.
	state, err := ReadRunState(stateFile)
	if err != nil {
		t.Fatalf("read install state: %v", err)
	}
	if state.Phase != PhaseSucceeded || state.Result != ResultSucceeded || state.RemoteResult != RemoteResultDeterministic {
		t.Fatalf("the install record must be untouched by verification: %+v", state)
	}
	// A re-run pointed at a DIFFERENT --output must still be refused: the delete
	// quota lives in the canonical ledger, so changing output does not re-arm it.
	input.Output = filepath.Join(baseDir, "verify-out-2")
	retryErr := RunVerify(context.Background(), input, os.Stdout)
	if retryErr == nil {
		t.Fatal("a prior acceptance attempt must block a second delete for the same run+target regardless of --output")
	}
	retryReport := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if retryReport.Results[0].Status != VerifyStatusFailed ||
		!strings.Contains(retryReport.Results[0].Detail, "already recorded") {
		t.Fatalf("the ledger must refuse a re-issued delete for postgresql: %+v", retryReport.Results[0])
	}
	deleted2, _ := os.ReadFile(filepath.Join(stateDir, "deleted"))
	if string(deleted2) != string(deleted) {
		t.Fatal("the refused re-run must not touch the cluster")
	}

	// A smoke run for the same run still works and writes its own report —
	// it never repeats the acceptance.
	input.Level = VerifyLevelSmoke
	input.Output = filepath.Join(baseDir, "verify-out")
	input.ScriptDir = filepath.Join(baseDir, "scripts")
	r13StubScript(t, input.ScriptDir, "postgresql", "PG-OK")
	r13StubScript(t, input.ScriptDir, "nats", "NATS-OK")
	if err := RunVerify(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("smoke must stay available after an acceptance failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "verify-out", "verify-smoke-ani-ani-lab-20260924-130000.json")); err != nil {
		t.Fatalf("the smoke report must exist: %v", err)
	}
	deleted3, _ := os.ReadFile(filepath.Join(stateDir, "deleted"))
	if string(deleted3) != string(deleted) {
		t.Fatal("smoke must not recreate anything")
	}
}
