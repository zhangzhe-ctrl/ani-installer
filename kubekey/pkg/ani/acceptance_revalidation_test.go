package ani

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real dispatcher, durable ledger, protocol and UID-conditioned API delete
// run unchanged; only the external Kubernetes endpoint is substituted.
func TestVerifyExplicitPreservationRevalidationIsOncePerBinary(t *testing.T) {
	dir, state := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, dir, true, PhaseSucceeded, "postgresql")
	raw, err := os.ReadFile(runFile)
	if err != nil {
		t.Fatal(err)
	}
	var manifest RunManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Identity.CodeBinaryDigest = strings.Repeat("c", 64)
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(runFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	input := VerifyInput{RunFile: runFile, StateFile: stateFile, Level: VerifyLevelAcceptance, Only: []string{"postgresql"}, AllowPodRecreate: true, Kubeconfig: filepath.Join(dir, "kubeconfig"), Output: filepath.Join(dir, "original")}
	if err = RunVerify(context.Background(), input, io.Discard); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "acceptance-state", "ledger-ani-ani-lab-20260924-130000-postgresql.json")
	parentBytes, err := os.ReadFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	input.RevalidatePreservation = true
	input.Output = filepath.Join(dir, "revalidation")
	if err = RunVerify(context.Background(), input, io.Discard); err != nil {
		t.Fatalf("explicit candidate revalidation must run after prior successful acceptance: %v", err)
	}
	if got := c04DeleteCount(t, state); got != 2 {
		t.Fatalf("want original and one new UID-conditioned delete, got %d", got)
	}
	after, err := os.ReadFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parentBytes, after) {
		t.Fatal("revalidation rewrote the original acceptance ledger")
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if report.Overall != VerifyStatusPass || report.VerifierCode == report.InstallerCode {
		t.Fatalf("report must name current verifier separately from original installer: %+v", report)
	}
	sum := sha256.Sum256(parentBytes)
	if !report.RevalidatePreservation || report.Results[0].Evidence["originalLedgerSHA256"] != hex.EncodeToString(sum[:]) || report.Results[0].Evidence["revalidationVerifierCodeBinaryDigest"] != report.VerifierCode {
		t.Fatalf("the public report must bind the immutable original ledger and actual verifier binary: %+v", report)
	}
	input.Output = filepath.Join(dir, "another-output")
	if err = RunVerify(context.Background(), input, io.Discard); err == nil {
		t.Fatal("changing output must not rearm this binary's revalidation")
	}
	if got := c04DeleteCount(t, state); got != 2 {
		t.Fatalf("spent revalidation issued another delete: %d", got)
	}
}

func TestVerifyPreservationRevalidationRefusesUnsafeOriginalState(t *testing.T) {
	for _, scenario := range []string{"unknown", "failed", "corrupt", "missing", "foreign-target", "replaced-pvc", "same-installer", "smoke", "no-scope", "no-recreate-permission"} {
		t.Run(scenario, func(t *testing.T) {
			dir, state := r13Prepare(t)
			runFile, stateFile := r13RunRecord(t, dir, true, PhaseSucceeded, "postgresql")
			raw, _ := os.ReadFile(runFile)
			var manifest RunManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Identity.CodeBinaryDigest = strings.Repeat("c", 64)
			raw, _ = json.Marshal(manifest)
			if err := os.WriteFile(runFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			input := VerifyInput{RunFile: runFile, StateFile: stateFile, Level: VerifyLevelAcceptance, Only: []string{"postgresql"}, AllowPodRecreate: true, Kubeconfig: filepath.Join(dir, "kubeconfig"), Output: filepath.Join(dir, "original")}
			if err := RunVerify(context.Background(), input, io.Discard); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(dir, "acceptance-state", "ledger-ani-ani-lab-20260924-130000-postgresql.json")
			raw, err := os.ReadFile(parent)
			if err != nil {
				t.Fatal(err)
			}
			var original acceptanceLedger
			if err = json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "unknown":
				original.State = ledgerStateUnknown
			case "failed":
				original.Outcome = "readback-failed"
			case "foreign-target":
				original.Target = "nats"
			case "corrupt":
				raw = []byte("{not-json")
			case "missing":
				if err = os.Remove(parent); err != nil {
					t.Fatal(err)
				}
			case "replaced-pvc":
				if err = os.WriteFile(filepath.Join(state, "pvc-uid"), []byte("new-storage"), 0600); err != nil {
					t.Fatal(err)
				}
			case "same-installer":
				manifest.Identity.CodeBinaryDigest = selfBinaryDigest()
				b, _ := json.Marshal(manifest)
				if err = os.WriteFile(runFile, b, 0600); err != nil {
					t.Fatal(err)
				}
			case "smoke":
				input.Level = VerifyLevelSmoke
			case "no-scope":
				input.Only = nil
			case "no-recreate-permission":
				input.AllowPodRecreate = false
			}
			if scenario == "unknown" || scenario == "failed" || scenario == "foreign-target" {
				raw, _ = json.Marshal(original)
			}
			if scenario != "missing" {
				if err = os.WriteFile(parent, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			input.RevalidatePreservation = true
			input.Output = filepath.Join(dir, "new-output")
			if err = RunVerify(context.Background(), input, io.Discard); err == nil {
				t.Fatal("unsafe original state must refuse revalidation")
			}
			if got := c04DeleteCount(t, state); got != 1 {
				t.Fatalf("refused revalidation issued a delete: %d", got)
			}
			if scenario != "missing" {
				after, err := os.ReadFile(parent)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, raw) {
					t.Fatal("refusal modified the original ledger")
				}
			}
		})
	}
}

func TestVerifyFailedPreservationRevalidationCannotBeReplayed(t *testing.T) {
	dir, state := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, dir, true, PhaseSucceeded, "postgresql")
	raw, _ := os.ReadFile(runFile)
	var manifest RunManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Identity.CodeBinaryDigest = strings.Repeat("c", 64)
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(runFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	input := VerifyInput{RunFile: runFile, StateFile: stateFile, Level: VerifyLevelAcceptance, Only: []string{"postgresql"}, AllowPodRecreate: true, Kubeconfig: filepath.Join(dir, "kubeconfig"), Output: filepath.Join(dir, "original")}
	if err := RunVerify(context.Background(), input, io.Discard); err != nil {
		t.Fatal(err)
	}
	input.RevalidatePreservation = true
	input.Output = filepath.Join(dir, "failed-revalidation")
	t.Setenv("FAKE_DATA_LOST", "1")
	if err := RunVerify(context.Background(), input, io.Discard); err == nil {
		t.Fatal("lost original committed data must fail")
	}
	if got := c04DeleteCount(t, state); got != 2 {
		t.Fatalf("the failed new check must issue exactly one UID-conditioned delete, got %d", got)
	}
	t.Setenv("FAKE_DATA_LOST", "")
	input.Output = filepath.Join(dir, "changed-output")
	if err := RunVerify(context.Background(), input, io.Discard); err == nil {
		t.Fatal("failed revalidation must not be replayable")
	}
	if got := c04DeleteCount(t, state); got != 2 {
		t.Fatalf("replayed failure deleted again: %d", got)
	}
}

func TestVerifyPreservationRevalidationRejectsComponentsRecords(t *testing.T) {
	for _, release := range []string{"ours", "absent"} {
		t.Run(release, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("ANI_ACCEPTANCE_STATE_DIR", state)
			execution := runR15Execution(t, release)
			if execution.recordPath == "" {
				t.Fatal("production execution did not produce a record")
			}
			input := verifyInputFor(t, execution.recordPath, VerifyLevelAcceptance, "nats", t.TempDir(), t.TempDir())
			input.Kubeconfig = execution.execute.Kubeconfig
			input.AllowPodRecreate = true
			input.RevalidatePreservation = true
			if err := RunVerify(context.Background(), input, io.Discard); err == nil {
				t.Fatal("a components execution must not grant a revalidation budget")
			}
			files, err := os.ReadDir(state)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Fatalf("refused record touched acceptance state: %v", files)
			}
		})
	}
}
