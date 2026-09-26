/*
Copyright 2026 The KubeSphere Contributors.
Licensed under the Apache License, Version 2.0.
*/

package ani

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F09: an already-cancelled context must prevent smoke from starting the real
// verify script — the cancellation bounds the actual subprocess, not just the
// Go loop.
func TestFRemediation_CancelledSmokeMustNotStartScript(t *testing.T) {
	baseDir, _ := r13Prepare(t)
	scriptDir := filepath.Join(baseDir, "ani-scripts")
	out := filepath.Join(baseDir, "verify-out")
	if err := os.MkdirAll(filepath.Join(scriptDir, "nats"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(baseDir, "executed")
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "nats")
	script := "#!/usr/bin/env bash\nprintf ran > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "nats", "verify.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = RunVerify(ctx, VerifyInput{
		RunFile:    runFile,
		StateFile:  stateFile,
		Level:      VerifyLevelSmoke,
		ScriptDir:  scriptDir,
		Output:     out,
		Kubeconfig: filepath.Join(baseDir, "unused"),
	}, io.Discard)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("smoke executed the script despite an already-cancelled context")
	}
}

// T06/T07 / F09: the one-shot ledger is keyed by (run,target), consumed BEFORE
// the change, and independent of --output. A second claim for the same target
// never re-arms the delete quota; a different target is unaffected.
func TestFRemediation_LedgerIsPerTargetAndAtMostOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance")
	pg := acceptanceTargets["postgresql"]
	nats := acceptanceTargets["nats"]
	rec := acceptanceLedger{State: ledgerStateAttempted, RunID: "run-1", Target: "postgresql"}

	_, created, err := claimAcceptanceLedger(dir, "run-1", pg, rec)
	if err != nil || !created {
		t.Fatalf("first claim must succeed and be created: created=%v err=%v", created, err)
	}
	existing, created2, err := claimAcceptanceLedger(dir, "run-1", pg, rec)
	if err != nil {
		t.Fatalf("second claim errored: %v", err)
	}
	if created2 {
		t.Fatal("a second claim for the same (run,target) must NOT be created (delete quota already consumed)")
	}
	if existing.State != ledgerStateAttempted {
		t.Fatalf("the existing intent must be returned, got %+v", existing)
	}
	// A different target in the same run is independent (T07): accepting
	// PostgreSQL must not block a legitimate NATS acceptance.
	if _, createdN, err := claimAcceptanceLedger(dir, "run-1", nats, acceptanceLedger{State: ledgerStateAttempted, RunID: "run-1", Target: "nats"}); err != nil || !createdN {
		t.Fatalf("a different target must claim independently: created=%v err=%v", createdN, err)
	}
	// A corrupt ledger is treated conservatively as unknown, never re-issued.
	corrupt := filepath.Join(dir, "ledger-run-9-postgresql.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex, createdC, err := claimAcceptanceLedger(dir, "run-9", pg, rec)
	if err != nil || createdC || ex.State != ledgerStateUnknown {
		t.Fatalf("a corrupt ledger must be read as unknown and not re-issued: created=%v state=%q err=%v", createdC, ex.State, err)
	}
}

// T08 / F09: a same-name replacement that appears between capture and delete is
// never deleted. The delete is the only change and it must be pre-checked.
func TestFRemediation_SameNameReplacementIsNeverDeleted(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	t.Setenv("FAKE_REPLACE_UID_ON_REREAD", "1")
	input := VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           filepath.Join(baseDir, "verify-out"),
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}
	if err := RunVerify(context.Background(), input, os.Stdout); err == nil {
		t.Fatal("a same-name replacement must abort before the delete")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
		t.Fatal("no pod may be deleted when the target identity changed")
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if !strings.Contains(report.Results[0].Detail, "same-name replacement") {
		t.Fatalf("the refusal must record the identity change: %+v", report.Results[0])
	}
}
func TestFRemediation_AcceptanceScopeKeyVariesByTarget(t *testing.T) {
	a := acceptanceScopeKey("run-1", []string{"postgresql"})
	b := acceptanceScopeKey("run-1", []string{"nats"})
	if a == b {
		t.Fatal("different target sets must produce different acceptance scope keys")
	}
	// Deterministic and order-independent for the same set.
	if acceptanceScopeKey("run-1", []string{"postgresql", "nats"}) != acceptanceScopeKey("run-1", []string{"nats", "postgresql"}) {
		t.Fatal("the scope key must be order-independent")
	}
}

// T11 / F09: cancellation DURING the recreation wait (not just before start)
// must bound the wait, record the remote result as unknown in the ledger, and
// stop without replaying.
func TestFRemediation_CancelledDuringAcceptanceWaitRecordsUnknown(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	t.Setenv("FAKE_STICKY_POD_UID", "1")
	t.Setenv("ANI_VERIFY_POD_RECREATE_TIMEOUT", "30s")
	input := VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           filepath.Join(baseDir, "verify-out"),
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	err := RunVerify(ctx, input, os.Stdout)
	cancel()
	if err == nil {
		t.Fatal("a cancelled acceptance wait must fail")
	}
	reportPath := r13FindAcceptanceReport(t, input.Output)
	if !strings.Contains(reportPath, "verify-acceptance") {
		t.Fatalf("unexpected report path %s", reportPath)
	}
	report := r13ReadReport(t, reportPath)
	if !strings.Contains(report.Results[0].Detail, "cancelled") ||
		!strings.Contains(report.Results[0].Detail, "unknown") {
		t.Fatalf("the cancel must be recorded with remote-unknown semantics: %+v", report.Results[0])
	}
	// The ledger must show the delete was consumed; a re-run must not re-delete.
	deleted := filepath.Join(stateDir, "deleted")
	before, _ := os.ReadFile(deleted)
	retryErr := RunVerify(context.Background(), input, os.Stdout)
	if retryErr == nil {
		t.Fatal("cancelled acceptance must not be replayable")
	}
	after, _ := os.ReadFile(deleted)
	if string(before) != string(after) {
		t.Fatal("a cancelled (unknown) result must never auto-replay the delete")
	}
}

// T11 / F09: cancelling mid-run must also terminate the already-started smoke
// subprocess (CommandContext), not just refuse to start the next one.
func TestFRemediation_CancelledSmokeKillsStartedScript(t *testing.T) {
	baseDir, _ := r13Prepare(t)
	scriptDir := filepath.Join(baseDir, "ani-scripts")
	if err := os.MkdirAll(filepath.Join(scriptDir, "nats"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(baseDir, "started")
	finished := filepath.Join(baseDir, "finished")
	script := "#!/usr/bin/env bash\ntouch \"" + started + "\"\nsleep 30\ntouch \"" + finished + "\"\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "nats", "verify.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "nats")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel after the script surely started.
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(started); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	_ = RunVerify(ctx, VerifyInput{
		RunFile:    runFile,
		StateFile:  stateFile,
		Level:      VerifyLevelSmoke,
		ScriptDir:  scriptDir,
		Output:     filepath.Join(baseDir, "verify-out"),
		Kubeconfig: filepath.Join(baseDir, "unused"),
	}, io.Discard)
	cancel()
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(started); err != nil {
		t.Fatal("the smoke script should have started before cancellation")
	}
	if _, err := os.Stat(finished); err == nil {
		t.Fatal("cancellation must terminate the running smoke subprocess; it ran to completion")
	}
}
