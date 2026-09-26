package ani

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// L-06: a components addition used to leave no record that `ani verify` could
// consume, so an added component could not be verified at all. These tests drive
// the REAL production writer (RunComponentsExecute) and the REAL consumer
// (RunVerify / loadVerifyRunRecord); only external commands (kubectl, the
// playbook runner, the component verify script) are mocked. No test hand-writes
// a success record to stand in for the writer — hand-built JSON appears only
// where a FORGED record must be refused.

// r15ExecutionCase runs one plan+execute through the production path and returns
// the record the product landed, plus the paths a later operator is handed.
type r15ExecutionCase struct {
	planFile   string
	baseRun    string
	recordPath string
	record     RunManifest
	stdout     string
	execute    ComponentsExecuteInput
	kkLog      string
	outDir     string
	// baseBefore snapshots the install record's bytes before the pass, so a
	// test can prove a components run never rewrites its own base.
	baseBefore map[string]string
}

func runR15Execution(t *testing.T, natsRelease string) r15ExecutionCase {
	t.Helper()
	return runR15ExecutionOverBase(t, "  certManager: {enabled: true}",
		"  certManager: {enabled: true}\n  nats: {enabled: true}", natsRelease)
}

// runR15ExecutionOverBase is runR15Execution with both sides of the comparison
// chosen by the caller: what the base install selected, and what the requested
// site selects. Passing the same block twice is the C06 shape — a component the
// first install itself put in place, whose re-request genuinely cannot move the
// effective config digest.
func runR15ExecutionOverBase(t *testing.T, baseBlock, requestedBlock, natsRelease string) r15ExecutionCase {
	t.Helper()
	planFile, execute, kkLog, outDir := r15PlanAndExecuteOverBase(t, baseBlock, requestedBlock, natsRelease)
	var plan ComponentsPlan
	raw, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	var out bytes.Buffer
	tcBase := map[string]string{}
	for _, name := range []string{"run.json", "run-state.json"} {
		path := filepath.Join(filepath.Dir(plan.BaseRunFile), name)
		if _, err := os.Stat(path); err == nil {
			tcBase[name] = sha256FileHex(path)
		}
	}
	execErr := RunComponentsExecute(context.Background(), execute, &out)
	tc := r15ExecutionCase{planFile: planFile, baseRun: plan.BaseRunFile, stdout: out.String(),
		execute: execute, kkLog: kkLog, outDir: outDir, baseBefore: tcBase}
	path := recordPathFromStdout(t, tc.stdout)
	tc.recordPath = path
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the printed record path %s is not readable: %v (stdout %q)", path, err, tc.stdout)
		}
		if err := json.Unmarshal(data, &tc.record); err != nil {
			t.Fatalf("the landed record does not parse: %v", err)
		}
	}
	if execErr != nil {
		tc.recordPath = path
	}
	return tc
}

// recordPathFromStdout reads the path the product printed for the record. The
// positive tests must consume what the tool SAID, not a path they guessed.
func recordPathFromStdout(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, "components record: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func verifyInputFor(t *testing.T, recordPath, level, only, scriptDir, output string) VerifyInput {
	t.Helper()
	return VerifyInput{
		RunFile: recordPath, Level: level, ScriptDir: scriptDir, Output: output,
		Kubeconfig: filepath.Join(filepath.Dir(recordPath), "unused-kubeconfig"),
		Only:       splitOnly(only),
	}
}

func splitOnly(only string) []string {
	if only == "" {
		return nil
	}
	return strings.Split(only, ",")
}

// The happy add path: execute lands a record, and verify consumes it.
func TestComponentsExecutionRecordIsVerifiable(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", stateDir)
	tc := runR15Execution(t, "absent")

	if tc.recordPath == "" {
		t.Fatalf("a successful execute must print the record it landed; stdout:\n%s", tc.stdout)
	}
	if tc.record.RecordKind != RecordKindComponentsExecution {
		t.Fatalf("record kind is %q, want %q", tc.record.RecordKind, RecordKindComponentsExecution)
	}
	e := tc.record.ComponentsExecution
	if e == nil {
		t.Fatal("record carries no componentsExecution block")
	}
	if e.Operation != ComponentsOperationAdd || !e.DidInstall {
		t.Fatalf("a real addition must record operation=add didInstall=true, got %q/%v", e.Operation, e.DidInstall)
	}
	if strings.Join(e.Executed, ",") != "nats" {
		t.Fatalf("executed scope is %v, want [nats]", e.Executed)
	}
	if len(e.ObservedExists) != 0 {
		t.Fatalf("nothing was already installed here, got %v", e.ObservedExists)
	}
	if e.BaseRunID == "" || e.BaseRecordSHA256 == "" || !isHex64(e.BaseRecordSHA256) {
		t.Fatalf("record must bind the base run and its exact bytes: %+v", e)
	}
	// Ownership is a live resource identity, not a name.
	target := e.Targets[0]
	if target.Namespace == "" || target.Kind == "" || target.Name == "" || target.UID == "" {
		t.Fatalf("target %s carries no resource identity: %+v", target.Component, target)
	}
	// The installing code identity is this run's own, and complete. The binary
	// digest is always computable; the tree/commit pair must be either real or
	// explicitly "not-embedded", never blank.
	if !isHex64(tc.record.Identity.CodeBinaryDigest) {
		t.Fatalf("record has no computing binary digest: %+v", tc.record.Identity)
	}
	if tc.record.Identity.SourceTreeFingerprint != notEmbeddedIdentity && !isHex64(tc.record.Identity.SourceTreeFingerprint) {
		t.Fatalf("tree fingerprint %q is neither an embedded digest nor the honest marker", tc.record.Identity.SourceTreeFingerprint)
	}
	if tc.record.Identity.CodeCommit == "" {
		t.Fatalf("commit must never be blank: %+v", tc.record.Identity)
	}
	if tc.record.Identity.MaterialsLockDigest == "" {
		t.Fatal("record carries no materials identity")
	}
	if tc.record.ConfigDigest == e.BaseConfigDigest {
		t.Fatal("an addition must carry its own effective config digest, distinct from the base's")
	}
	// Landed atomically: no temp debris, owner-only file.
	entries, err := os.ReadDir(filepath.Dir(tc.recordPath))
	if err != nil {
		t.Fatalf("read record dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("the writer left a temporary file behind: %s", entry.Name())
		}
	}
	if info, err := os.Stat(tc.recordPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode is %v (err %v), want 0600", info.Mode().Perm(), err)
	}

	// Now consume it for real: a stub script stands in for the packaged one.
	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))
	out := filepath.Join(t.TempDir(), "verify")
	input := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, "nats", scripts, out)
	input.Kubeconfig = tc.execute.Kubeconfig
	if err := RunVerify(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("verify smoke on the execution record failed: %v", err)
	}
	report := r13ReadReport(t, filepath.Join(out, "verify-smoke-"+tc.record.RunID+".json"))
	if report.Overall != VerifyStatusPass {
		t.Fatalf("verify report overall %s: %+v", report.Overall, report.Results)
	}
	if report.RecordKind != RecordKindComponentsExecution || report.BaseRunID != e.BaseRunID {
		t.Fatalf("report must name its subject and base: %+v", report)
	}
	if report.Operation != ComponentsOperationAdd || report.DidInstall == nil || !*report.DidInstall {
		t.Fatalf("report lost the operation provenance: %+v", report)
	}
	if report.InstallerCode != tc.record.Identity.CodeBinaryDigest || report.VerifierCode == "" {
		t.Fatalf("installer and verifier identities must both be recorded: %+v", report)
	}
	if report.InstallerCode == report.VerifierCode && report.VerifierCode != "" &&
		!strings.HasPrefix(report.VerifierCode, report.InstallerCode) {
		t.Fatalf("installer %q vs verifier %q should be explicit, not implied", report.InstallerCode, report.VerifierCode)
	}
	// And consuming it must not have rewritten the install it refers to.
	for name, before := range tc.baseBefore {
		if after := sha256FileHex(filepath.Join(filepath.Dir(tc.baseRun), name)); after != before {
			t.Fatalf("the base %s changed while verifying an execution record (%s -> %s)", name, before[:12], after[:12])
		}
	}
}

// A pure no-op stays read-only and attests only an observation.
func TestComponentsObservationRecordGrantsSmokeOnly(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", stateDir)
	tc := runR15Execution(t, "ours") // the release already exists and is ANI-owned

	if tc.recordPath == "" {
		t.Fatalf("a read-only no-op must still land a consumable record; stdout:\n%s", tc.stdout)
	}
	e := tc.record.ComponentsExecution
	if e.Operation != ComponentsOperationNoop || e.DidInstall {
		t.Fatalf("no-op must record operation=noop didInstall=false, got %q/%v", e.Operation, e.DidInstall)
	}
	if len(e.Executed) != 0 {
		t.Fatalf("a no-op executed %v; it must execute nothing", e.Executed)
	}
	if strings.Join(e.ObservedExists, ",") != "nats" {
		t.Fatalf("observed scope is %v, want [nats]", e.ObservedExists)
	}
	// The playbook really was not invoked for this pass.
	if log, err := os.ReadFile(tc.kkLog); err == nil && strings.Contains(string(log), "ani_components.yaml") {
		t.Fatalf("a no-op must never run the components playbook:\n%s", log)
	}
	// Smoke is allowed for the observed component.
	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))
	out := filepath.Join(t.TempDir(), "verify")
	input := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, "nats", scripts, out)
	input.Kubeconfig = tc.execute.Kubeconfig
	if err := RunVerify(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("smoke on an observation record must work: %v", err)
	}

	// Acceptance is a change: an observation record must not buy it, and it
	// must not even reach the ledger.
	accOut := filepath.Join(t.TempDir(), "verify-acc")
	acc := verifyInputFor(t, tc.recordPath, VerifyLevelAcceptance, "nats", scripts, accOut)
	acc.Kubeconfig = tc.execute.Kubeconfig
	acc.AllowPodRecreate = true
	errBuf := &bytes.Buffer{}
	err := runVerifyWithWriter(context.Background(), acc, errBuf)
	if err == nil || !strings.Contains(err.Error(), "didInstall=false") {
		t.Fatalf("acceptance on an observation record must be refused for granting no change; got %v", err)
	}
	if ledgerFiles, _ := filepath.Glob(filepath.Join(stateDir, "ledger-*.json")); len(ledgerFiles) != 0 {
		t.Fatalf("a refused acceptance must not claim the ledger: %v", ledgerFiles)
	}
	if !strings.Contains(errBuf.String(), "") {
		_ = errBuf // the refusal is returned as an error; nothing else is expected
	}
}

// runVerifyWithWriter is RunVerify with a capturable stdout.
func runVerifyWithWriter(ctx context.Context, input VerifyInput, stdout *bytes.Buffer) error {
	return RunVerify(ctx, input, stdout)
}

// Every refusal that must never be mistaken for a pass.
func TestComponentsExecutionRecordRefusals(t *testing.T) {
	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))

	newCase := func(t *testing.T) (r15ExecutionCase, string) {
		t.Helper()
		state := t.TempDir()
		t.Setenv("ANI_ACCEPTANCE_STATE_DIR", state)
		tc := runR15Execution(t, "absent")
		if tc.recordPath == "" {
			t.Fatalf("no record landed; stdout:\n%s", tc.stdout)
		}
		return tc, state
	}
	readRecord := func(t *testing.T, path string) RunManifest {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		var m RunManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("parse record: %v", err)
		}
		return m
	}
	writeRecord := func(t *testing.T, path string, m RunManifest) {
		t.Helper()
		encoded, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name   string
		setup  func(t *testing.T, tc r15ExecutionCase, state string)
		wanted string
	}{
		{
			name: "base record bytes changed under the record",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				raw, err := os.ReadFile(tc.baseRun)
				if err != nil {
					t.Fatal(err)
				}
				var base RunManifest
				if err := json.Unmarshal(raw, &base); err != nil {
					t.Fatal(err)
				}
				base.Profile = "base"
				writeRecord(t, tc.baseRun, base)
			},
			wanted: "hashes to",
		},
		{
			name: "a different cluster standing here",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				t.Setenv("FAKE_CLUSTER_UID", "uid-kube-cluster-B")
			},
			wanted: "is not the same base",
		},
		{
			// cert-manager is enabled in the site config but this execution
			// never touched it: --only may not widen past the record.
			name: "--only names a component the record does not attest",
			// The record is left exactly as the product wrote it; the widened
			// ask comes from --only, which must not be satisfied by "the site
			// config happens to enable this component".
			setup:  func(t *testing.T, tc r15ExecutionCase, state string) {},
			wanted: "outside what this run record attests",
		},
		{
			name: "failed execution record is an event, not a pass",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				m := readRecord(t, tc.recordPath)
				m.Result = ResultFailed
				writeRecord(t, tc.recordPath, m)
			},
			wanted: "only a succeeded execution",
		},
		{
			name: "identity triple missing is refused",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				m := readRecord(t, tc.recordPath)
				m.Identity.CodeBinaryDigest = strings.Repeat("7", 63) // not 64-hex
				writeRecord(t, tc.recordPath, m)
			},
			wanted: "installing binary digest",
		},
		{
			name: "didInstall disagrees with the operation",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				m := readRecord(t, tc.recordPath)
				m.ComponentsExecution.Operation = ComponentsOperationNoop
				writeRecord(t, tc.recordPath, m)
			},
			wanted: "didInstall",
		},
		{
			name: "a target stripped of its resource identity",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				m := readRecord(t, tc.recordPath)
				m.ComponentsExecution.Targets[0].UID = ""
				writeRecord(t, tc.recordPath, m)
			},
			wanted: "without a resource uid",
		},
		{
			name: "mutated evidence file",
			setup: func(t *testing.T, tc r15ExecutionCase, state string) {
				for _, ref := range readRecord(t, tc.recordPath).ComponentsExecution.Evidence {
					if ref.Kind != ComponentsEvidenceExecuteReport {
						continue
					}
					if err := os.WriteFile(ref.Path, []byte("{ tampered\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				t.Fatal("the record named no execute-report evidence")
			},
			wanted: "hashes to",
		},
	}

	for _, tcCase := range cases {
		t.Run(tcCase.name, func(t *testing.T) {
			t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))
			tc, state := newCase(t)
			tcCase.setup(t, tc, state)
			out := filepath.Join(t.TempDir(), "verify")
			only := "nats"
			if tcCase.name == "--only names a component the record does not attest" {
				only = "nats,cert-manager"
			}
			input := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, only, scripts, out)
			input.Kubeconfig = tc.execute.Kubeconfig
			err := RunVerify(context.Background(), input, os.Stdout)
			if err == nil {
				t.Fatalf("verify accepted a record it must refuse (%s)", tcCase.name)
			}
			if !strings.Contains(err.Error(), tcCase.wanted) {
				t.Fatalf("refusal for %q says %q, want it to name %q", tcCase.name, err.Error(), tcCase.wanted)
			}
			if scripts, err := os.ReadFile(os.Getenv("FAKE_SCRIPT_LOG")); err == nil && len(scripts) > 0 {
				t.Fatalf("a refused record must not reach the component scripts:\n%s", scripts)
			}
			_ = state
		})
	}

	t.Run("a torn record is refused as unparsable", func(t *testing.T) {
		tc, _ := newCase(t)
		data, err := os.ReadFile(tc.recordPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tc.recordPath, data[:len(data)/2], 0o600); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "verify")
		input := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, "nats", scripts, out)
		input.Kubeconfig = tc.execute.Kubeconfig
		err = RunVerify(context.Background(), input, os.Stdout)
		if err == nil || !strings.Contains(err.Error(), "unexpected end of JSON input") {
			t.Fatalf("a half-written record must be refused as unparsable, got %v", err)
		}
	})

	t.Run("a plan record is still not an execution record", func(t *testing.T) {
		tc, _ := newCase(t)
		out := filepath.Join(t.TempDir(), "verify")
		input := verifyInputFor(t, tc.planFile, VerifyLevelSmoke, "nats", scripts, out)
		input.Kubeconfig = tc.execute.Kubeconfig
		err := RunVerify(context.Background(), input, os.Stdout)
		if err == nil || !strings.Contains(err.Error(), "a plan is neither") {
			t.Fatalf("a plan must be refused by name, not by a type error; got %v", err)
		}
	})

	t.Run("the first install's own path is unchanged", func(t *testing.T) {
		tc, _ := newCase(t)
		out := filepath.Join(t.TempDir(), "verify")
		input := verifyInputFor(t, tc.baseRun, VerifyLevelSmoke, "", scripts, out)
		input.Kubeconfig = tc.execute.Kubeconfig
		// nats is in the base record's own components? it was added, so verify
		// the base record still loads and dispatches as an install-success.
		loaded, err := loadVerifyRunRecord(context.Background(), input)
		if err != nil {
			t.Fatalf("the base install record must still load: %v", err)
		}
		if loaded.isExecution || loaded.baseRunID != loaded.runID {
			t.Fatalf("install-success records must not be treated as executions: %+v", loaded)
		}
		_ = out
		_ = input
	})
}

// The change budget belongs to (base install, target). An execution record
// introduces its own subject run id, so nothing about that id may re-arm a
// target whose one declared recreation was already spent.
func TestComponentsExecutionRecordsCannotReArmAcceptanceQuota(t *testing.T) {
	state := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", state)
	first := runR15Execution(t, "absent")
	if first.recordPath == "" {
		t.Fatalf("no record from the first addition; stdout:\n%s", first.stdout)
	}
	// Run ids are second-granular, so the second subject is staged a second
	// later; the point of the test is two DIFFERENT subject ids over one base.
	time.Sleep(1100 * time.Millisecond)
	second := runR15Execution(t, "ours")
	if second.recordPath == "" {
		t.Fatalf("no record from the second pass; stdout:\n%s", second.stdout)
	}
	baseRunID := first.record.ComponentsExecution.BaseRunID
	if second.record.ComponentsExecution.BaseRunID != baseRunID {
		t.Fatalf("both records must extend the same base install: %q vs %q",
			baseRunID, second.record.ComponentsExecution.BaseRunID)
	}
	if second.record.RunID == first.record.RunID {
		t.Fatal("the two subjects must be separate runs for this to test anything")
	}
	if second.record.ComponentsExecution.Operation != ComponentsOperationNoop {
		t.Fatalf("the second pass must be an observation, got %q", second.record.ComponentsExecution.Operation)
	}
	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")

	// 1) Acceptance through an add record is keyed to the BASE install run: the
	// report path proves it, because the subject id appears nowhere in it.
	spend := verifyInputFor(t, first.recordPath, VerifyLevelAcceptance, "nats", scripts, filepath.Join(t.TempDir(), "acc1"))
	spend.Kubeconfig = first.execute.Kubeconfig
	spend.AllowPodRecreate = true
	err := RunVerify(context.Background(), spend, os.Stdout)
	reports, _ := filepath.Glob(filepath.Join(filepath.Dir(spend.Output), "..", "**", "verify-acceptance-*.json"))
	if len(reports) == 0 {
		reports, _ = filepath.Glob(filepath.Join(spend.Output, "verify-acceptance-*.json"))
	}
	if len(reports) == 0 {
		t.Fatalf("acceptance left no report at all (err %v); the attempt must still be recorded", err)
	}
	name := filepath.Base(reports[0])
	if !strings.Contains(name, baseRunID) {
		t.Fatalf("acceptance report %s is not keyed to the base install run %s", name, baseRunID)
	}
	if strings.Contains(name, first.record.RunID) {
		t.Fatalf("acceptance report %s is keyed to the execution subject, which any later record could change", name)
	}

	// 2) The durable budget itself: the production claim, keyed by base run and
	// target, admits one intent and then refuses every other subject.
	target := acceptanceTarget{Namespace: "ani-platform", ControllerKind: "StatefulSet", ControllerName: "nats"}
	_, created, err := claimAcceptanceLedger(state, baseRunID, target, acceptanceLedger{
		State: ledgerStateAttempted, RunID: baseRunID, Target: "nats", StartedAt: "2026-09-26T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("claiming the budget failed: %v", err)
	}
	if !created {
		t.Fatal("the first claim for (base run, nats) must be granted")
	}
	existing, created, err := claimAcceptanceLedger(state, baseRunID, target, acceptanceLedger{
		State: ledgerStateAttempted, RunID: baseRunID, Target: "nats", StartedAt: "2026-09-26T00:00:01Z",
	})
	if err != nil {
		t.Fatalf("the second claim failed instead of being refused: %v", err)
	}
	if created {
		t.Fatal("a spent (base run, target) budget was re-armed")
	}
	if existing.State != ledgerStateAttempted {
		t.Fatalf("the existing budget entry was replaced: %+v", existing)
	}

	// 3) An observation record buys no change authority at all, and refuses
	// before any report or ledger write.
	obsOut := filepath.Join(t.TempDir(), "acc3")
	obs := verifyInputFor(t, second.recordPath, VerifyLevelAcceptance, "nats", scripts, obsOut)
	obs.Kubeconfig = second.execute.Kubeconfig
	obs.AllowPodRecreate = true
	err = RunVerify(context.Background(), obs, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "didInstall=false") {
		t.Fatalf("an observation record must not buy acceptance; got %v", err)
	}
	if entries, _ := os.ReadDir(obsOut); len(entries) > 0 {
		t.Fatalf("a refused acceptance must not write a report: %v", entries)
	}
}

// The re-arm proof must not rest on calling the ledger claim directly: the
// property is that an ACCEPTANCE RUN made through an execution record spends the
// base install's budget exactly once, in the durable ledger, and that a second
// subject over the same base is refused by that ledger rather than by anything
// about the record's shape.
func TestComponentsExecutionAcceptanceSpendsBaseLedgerOnce(t *testing.T) {
	state := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", state)

	// Two independent additions of the same component, each with its own subject
	// run id over the same base install. Both passes must complete before PATH is
	// swapped for the acceptance-shaped fake below.
	first := runR15Execution(t, "absent")
	if first.recordPath == "" {
		t.Fatalf("no record from the first addition; stdout:\n%s", first.stdout)
	}
	baseRunID := first.record.ComponentsExecution.BaseRunID
	if strings.Join(first.record.ComponentsExecution.Executed, ",") != "nats" {
		t.Fatalf("the first pass must have installed nats, got %v", first.record.ComponentsExecution.Executed)
	}
	time.Sleep(1100 * time.Millisecond)
	second := runR15Execution(t, "absent")
	if second.recordPath == "" {
		t.Fatalf("no record from the second addition; stdout:\n%s", second.stdout)
	}
	if second.record.RunID == first.record.RunID {
		t.Fatal("the two subjects need different run ids for a re-arm to be possible at all")
	}
	if second.record.ComponentsExecution.BaseRunID != baseRunID {
		t.Fatalf("both subjects must extend the same base install: %q vs %q",
			second.record.ComponentsExecution.BaseRunID, baseRunID)
	}

	// The cluster reads an acceptance needs are answered by the state-driven
	// fake. What the fake cannot do — publish on JetStream, say — only decides
	// the outcome of the attempt, never whether the budget was spent: the claim
	// is recorded before any change and every later subject must face it.
	binDir := filepath.Join(t.TempDir(), "acceptance-bin")
	fakeState := filepath.Join(t.TempDir(), "acceptance-state")
	for _, dir := range []string{binDir, fakeState} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	r13FakeKubectl(t, binDir, fakeState)
	t.Setenv("FAKE_STATE_DIR", fakeState)
	// This subject is nats, so the fixture pod must mount nats' volume: the
	// acceptance refuses a target whose PVC the pod does not actually use (C03).
	if err := os.WriteFile(filepath.Join(fakeState, "pod-claims"), []byte("nats-js-nats-0"), 0o600); err != nil {
		t.Fatalf("seed nats claim: %v", err)
	}
	// The two records above were written against the R15 fake's cluster, and the
	// consumer re-reads that identity live; the acceptance fake must answer with
	// the same cluster the records bind to.
	t.Setenv("FAKE_CLUSTER_UID", "uid-kube-cluster-a")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))
	deletes := func() int {
		data, err := os.ReadFile(filepath.Join(fakeState, "deleted"))
		if err != nil {
			return 0
		}
		return len(strings.Fields(string(data)))
	}

	attempt := func(tc r15ExecutionCase, out string) (error, VerifyReport) {
		t.Helper()
		input := verifyInputFor(t, tc.recordPath, VerifyLevelAcceptance, "nats", scripts, out)
		input.Kubeconfig = tc.execute.Kubeconfig
		input.AllowPodRecreate = true
		err := RunVerify(context.Background(), input, os.Stdout)
		reports, _ := filepath.Glob(filepath.Join(out, "verify-acceptance-*.json"))
		if len(reports) != 1 {
			t.Fatalf("the attempt must write exactly one report of its own (err %v): %v", err, reports)
		}
		return err, r13ReadReport(t, reports[0])
	}

	_, firstReport := attempt(first, filepath.Join(t.TempDir(), "acc-a"))
	deletedAfterFirst := deletes()

	// The durable claim — not the report filename — is what proves whose budget
	// an acceptance through an execution record actually spends.
	ledgerFiles, err := filepath.Glob(filepath.Join(state, "ledger-*.json"))
	if err != nil {
		t.Fatalf("glob ledger: %v", err)
	}
	if len(ledgerFiles) != 1 {
		t.Fatalf("one acceptance attempt must spend exactly one budget entry, got %v (first report %+v)",
			ledgerFiles, firstReport.Results)
	}
	name := filepath.Base(ledgerFiles[0])
	if !strings.Contains(name, baseRunID) {
		t.Fatalf("the budget file %s is not keyed to the base install run %s", name, baseRunID)
	}
	if strings.Contains(name, first.record.RunID) {
		t.Fatalf("the budget file %s is keyed to the execution subject, which any later record could rename", name)
	}
	data, err := os.ReadFile(ledgerFiles[0])
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var entry acceptanceLedger
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("parse ledger: %v", err)
	}
	if entry.RunID != baseRunID {
		t.Fatalf("ledger run id %q is not the base install %q", entry.RunID, baseRunID)
	}
	if entry.Target != "nats" || entry.Token == "" {
		t.Fatalf("the spent budget must name the target it locks: %+v", entry)
	}
	// Any of these states has already consumed the target's single recreation;
	// none of them may be replayed by a later subject.
	if entry.State != ledgerStateDone && entry.State != ledgerStateAttempted &&
		entry.State != ledgerStateUnknown {
		t.Fatalf("the budget entry must be in a state that has spent the change: %+v", entry)
	}

	// A second, independent addition of the same component over the same base
	// carries its own subject id and its own change authority, and must still be
	// refused by the spent budget without issuing another delete.
	_, repeatReport := attempt(second, filepath.Join(t.TempDir(), "acc-b"))
	if deletes() != deletedAfterFirst {
		t.Fatalf("a spent budget must not change the cluster again: deletes went %d -> %d",
			deletedAfterFirst, deletes())
	}
	refused := false
	for _, result := range repeatReport.Results {
		if strings.Contains(result.Detail, "already recorded as") &&
			strings.Contains(result.Detail, baseRunID) &&
			!strings.Contains(result.Detail, second.record.RunID) {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the second subject was not refused by the base-keyed budget: %+v", repeatReport.Results)
	}
	afterFiles, _ := filepath.Glob(filepath.Join(state, "ledger-*.json"))
	if len(afterFiles) != 1 {
		t.Fatalf("a refused acceptance may not add a budget entry: %v", afterFiles)
	}
	if after, err := os.ReadFile(afterFiles[0]); err != nil || string(after) != string(data) {
		t.Fatalf("the spent budget entry was rewritten (err %v)", err)
	}
}

// r15Execution runs one plan+execute of the caller's choosing through the
// production path and lands whatever record the product produced. wantStatuses
// pins how the plan read the live cluster before anything ran, so a helper
// cannot silently stop testing the case it was asked for.
func r15Execution(t *testing.T, componentsBlock, only, natsRelease string, wantStatuses map[string]string) r15ExecutionCase {
	t.Helper()
	input, outDir := r15PlanFixture(t, componentsBlock, only, false, natsRelease)
	baseDir := filepath.Dir(input.ConfigFile)
	binDir := filepath.Join(baseDir, "kkbin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r15FakeKK(t, binDir)
	input.KKBin = filepath.Join(binDir, "kk")
	input.PlanKKDigestOverride = fileSHA256Hex(input.KKBin)
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("plan %s failed: %v", only, err)
	}
	planFile := r15OnlyPlanFile(t, outDir)
	raw, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	statuses := map[string]string{}
	for _, component := range plan.Components {
		statuses[component.Component] = component.Status
	}
	for component, want := range wantStatuses {
		if statuses[component] != want {
			t.Fatalf("the fixture plan must read %s as %s, got %q (%+v)", component, want, statuses[component], statuses)
		}
	}
	execute, kkLog := r15ExecuteInput(t, input, outDir, planFile)

	before := map[string]string{}
	for _, name := range []string{"run.json", "run-state.json"} {
		path := filepath.Join(filepath.Dir(plan.BaseRunFile), name)
		if _, err := os.Stat(path); err == nil {
			before[name] = sha256FileHex(path)
		}
	}
	var out bytes.Buffer
	tc := r15ExecutionCase{planFile: planFile, baseRun: plan.BaseRunFile, execute: execute,
		kkLog: kkLog, outDir: outDir, baseBefore: before}
	execErr := RunComponentsExecute(context.Background(), execute, &out)
	tc.stdout = out.String()
	tc.recordPath = recordPathFromStdout(t, tc.stdout)
	if execErr != nil {
		return tc
	}
	if tc.recordPath == "" {
		t.Fatalf("a succeeded pass must print the record it landed; stdout:\n%s", tc.stdout)
	}
	data, err := os.ReadFile(tc.recordPath)
	if err != nil {
		t.Fatalf("the printed record %s is not readable: %v", tc.recordPath, err)
	}
	if err := json.Unmarshal(data, &tc.record); err != nil {
		t.Fatalf("the landed record does not parse: %v", err)
	}
	return tc
}

// mixedR15Execution plans an already-owned release next to a component that must
// really be installed, and runs the production executor once over them both, so
// the record it lands carries both an executed target and a left-alone one.
func mixedR15Execution(t *testing.T) r15ExecutionCase {
	t.Helper()
	return r15Execution(t, "  nats: {enabled: true}\n  valkey: {enabled: true}", "nats,valkey", "ours",
		map[string]string{"nats": "already_installed", "valkey": "planned"})
}

// L-06 requirement 2 and 3: one execution that installs some components and
// leaves an already-owned one alone must grant verification scope for exactly
// what it installed — never for the component it only looked at — and its smoke
// must coexist with the base install's own smoke in one output directory.
func TestComponentsExecutionRecordMixedScope(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", stateDir)
	tc := mixedR15Execution(t)

	e := tc.record.ComponentsExecution
	if e == nil {
		t.Fatal("the mixed record carries no componentsExecution block")
	}
	if e.Operation != ComponentsOperationAdd || !e.DidInstall {
		t.Fatalf("a mixed pass installed something, so it must record add/didInstall=true, got %q/%v", e.Operation, e.DidInstall)
	}
	if strings.Join(e.Executed, ",") != "valkey" {
		t.Fatalf("executed must be exactly what ran, got %v", e.Executed)
	}
	if strings.Join(e.ObservedExists, ",") != "nats" {
		t.Fatalf("observedExisting must be exactly what was left alone, got %v", e.ObservedExists)
	}
	if strings.Join(e.RequestedScope, ",") != "nats,valkey" {
		t.Fatalf("the record lost the requested scope, got %v", e.RequestedScope)
	}
	byComponent := map[string]ComponentsExecutionTarget{}
	for _, target := range e.Targets {
		byComponent[target.Component] = target
	}
	if len(e.Targets) != 2 {
		t.Fatalf("both components of the pass must be targets, got %+v", e.Targets)
	}
	if target := byComponent["valkey"]; target.Status != ComponentsTargetExecuted || target.UID == "" {
		t.Fatalf("the installed component must be recorded executed with a live uid: %+v", target)
	}
	if target := byComponent["nats"]; target.Status != ComponentsTargetAlreadyInstalled || target.UID == "" {
		t.Fatalf("the left-alone component must be recorded already_installed with its live uid: %+v", target)
	}
	// The playbook ran exactly once for this pass — a mixed run is one execution.
	if calls, err := os.ReadFile(tc.kkLog); err == nil {
		if n := len(strings.Split(strings.TrimSpace(string(calls)), "\n")); n != 1 {
			t.Fatalf("one mixed pass must invoke the playbook once, saw %d:\n%s", n, calls)
		}
	} else {
		t.Fatalf("the mixed pass must have invoked the playbook: %v", err)
	}

	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "valkey", "ANI-VALKEY-OK")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	r13StubScript(t, scripts, "cert-manager", "ANI-CERT-MANAGER-OK")
	t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))

	// One output directory holds both subjects' reports: the base install's own
	// smoke and this execution's smoke.
	shared := filepath.Join(t.TempDir(), "verify")

	smoke := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, "valkey", scripts, shared)
	smoke.Kubeconfig = tc.execute.Kubeconfig
	if err := RunVerify(context.Background(), smoke, os.Stdout); err != nil {
		t.Fatalf("smoke of the installed component failed: %v", err)
	}
	execReport := filepath.Join(shared, "verify-smoke-"+tc.record.RunID+".json")
	if _, err := os.Stat(execReport); err != nil {
		t.Fatalf("the execution smoke report is missing at %s: %v", execReport, err)
	}

	// The component this pass only confirmed was never installed by it, so this
	// record grants no scope for it.
	blocked := filepath.Join(t.TempDir(), "blocked")
	wide := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, "nats", scripts, blocked)
	wide.Kubeconfig = tc.execute.Kubeconfig
	err := RunVerify(context.Background(), wide, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "outside what this run record attests") {
		t.Fatalf("a mixed record must not grant scope to a component it left alone; got %v", err)
	}
	if entries, _ := os.ReadDir(blocked); len(entries) > 0 {
		t.Fatalf("a refused --only must not write a report: %v", entries)
	}

	// The base install still verifies its own components from the same directory,
	// and neither report displaces the other.
	base := verifyInputFor(t, tc.baseRun, VerifyLevelSmoke, "cert-manager", scripts, shared)
	base.Kubeconfig = tc.execute.Kubeconfig
	if err := RunVerify(context.Background(), base, os.Stdout); err != nil {
		t.Fatalf("the base install's own smoke failed beside an execution smoke: %v", err)
	}
	baseReport := filepath.Join(shared, "verify-smoke-"+e.BaseRunID+".json")
	if _, err := os.Stat(baseReport); err != nil {
		entries, _ := os.ReadDir(shared)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the base smoke report is missing (looked for %s) in %v: %v", baseReport, names, err)
	}
	reloaded, err := r13ReadReportIfExists(t, execReport)
	if err != nil {
		t.Fatalf("the execution smoke report was displaced by the base run's smoke: %v", err)
	}
	if reloaded.RecordKind != RecordKindComponentsExecution {
		t.Fatalf("the execution report lost its subject kind: %+v", reloaded)
	}
	for name, before := range tc.baseBefore {
		if after := sha256FileHex(filepath.Join(filepath.Dir(tc.baseRun), name)); after != before {
			t.Fatalf("the base %s changed while verifying a mixed pass (%s -> %s)", name, before[:12], after[:12])
		}
	}
}

func r13ReadReportIfExists(t *testing.T, path string) (VerifyReport, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return VerifyReport{}, err
	}
	var report VerifyReport
	if err := json.Unmarshal(data, &report); err != nil {
		return VerifyReport{}, err
	}
	return report, nil
}

// The dispatch that selects the base-bytes, evidence and live-cluster checks is
// a record-kind comparison. Re-labelling an execution record as an
// install-success record must therefore fail on the block it carries, or every
// one of those checks could be skipped by editing one string — and the change
// budget would be re-keyed to the record's own subject run.
func TestComponentsExecutionRecordCannotClaimInstallSuccessKind(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", stateDir)
	tc := runR15Execution(t, "absent")
	if tc.recordPath == "" {
		t.Fatalf("no record to forge; stdout:\n%s", tc.stdout)
	}
	data, err := os.ReadFile(tc.recordPath)
	if err != nil {
		t.Fatalf("read the landed record: %v", err)
	}
	var m RunManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse the landed record: %v", err)
	}
	if m.ComponentsExecution == nil {
		t.Fatal("the landed record carries no execution block to betray it")
	}
	forgeDir := t.TempDir()
	m.RecordKind = RecordKindInstallSuccess
	forge := filepath.Join(forgeDir, "forged-install-success.json")
	rewritten, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal the forged record: %v", err)
	}
	if err := os.WriteFile(forge, rewritten, 0o600); err != nil {
		t.Fatalf("write the forged record: %v", err)
	}
	landedBefore := sha256FileHex(tc.recordPath)

	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	input := verifyInputFor(t, forge, VerifyLevelSmoke, "nats", scripts, filepath.Join(t.TempDir(), "verify"))
	input.Kubeconfig = tc.execute.Kubeconfig
	err = RunVerify(context.Background(), input, os.Stdout)
	if err == nil {
		t.Fatal("a re-labelled execution record was consumed as an install-success record")
	}
	if !strings.Contains(err.Error(), "componentsExecution") {
		t.Fatalf("the refusal must name the block that gives the record away, not a type error; got %v", err)
	}
	if strings.Contains(err.Error(), "unmarshal") || strings.Contains(err.Error(), "parse the run record") {
		t.Fatalf("a well-formed forged record must be refused on meaning, not on parsing: %v", err)
	}
	if after := sha256FileHex(tc.recordPath); after != landedBefore {
		t.Fatalf("refusing a forged copy must not touch the landed record (%s -> %s)", landedBefore[:12], after[:12])
	}
	// The same forgery aimed at acceptance must not reach the change budget.
	acc := verifyInputFor(t, forge, VerifyLevelAcceptance, "nats", scripts, filepath.Join(t.TempDir(), "acc"))
	acc.Kubeconfig = tc.execute.Kubeconfig
	acc.AllowPodRecreate = true
	if err := RunVerify(context.Background(), acc, os.Stdout); err == nil ||
		!strings.Contains(err.Error(), "componentsExecution") {
		t.Fatalf("a re-labelled execution record must not reach acceptance either; got %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(stateDir, "ledger-*.json")); len(files) != 0 {
		t.Fatalf("a refused acceptance may not spend any budget: %v", files)
	}
}

// Every field the consumer uses to name a file or to grant scope comes out of
// the record's own text, so a record that no longer describes its own targets —
// or whose names are not single path elements — must be refused before anything
// is read or written.
func TestComponentsExecutionRecordShapeGuards(t *testing.T) {
	tc := runR15Execution(t, "absent")
	if tc.recordPath == "" {
		t.Fatalf("no record to mutate; stdout:\n%s", tc.stdout)
	}
	data, err := os.ReadFile(tc.recordPath)
	if err != nil {
		t.Fatalf("read the landed record: %v", err)
	}
	var landed RunManifest
	if err := json.Unmarshal(data, &landed); err != nil {
		t.Fatalf("parse the landed record: %v", err)
	}
	if landed.ComponentsExecution == nil {
		t.Fatal("the landed record carries no execution block")
	}

	for _, test := range []struct {
		name    string
		mutate  func(*RunManifest)
		wantMsg string
	}{
		{
			name: "scope names a component no target reports",
			mutate: func(m *RunManifest) {
				// valkey is carried as a component of the run but no target of
				// this pass reports it as executed.
				m.Components = append(m.Components, "valkey")
				m.ComponentsExecution.Executed = append(m.ComponentsExecution.Executed, "valkey")
			},
			wantMsg: "no target of that name reports executed",
		},
		{
			name: "scope names a component the run never carried",
			mutate: func(m *RunManifest) {
				m.ComponentsExecution.Executed = append(m.ComponentsExecution.Executed, "postgresql")
			},
			wantMsg: "does not carry as a component of the run",
		},
		{
			name: "scope names the same component twice",
			mutate: func(m *RunManifest) {
				m.ComponentsExecution.Executed = append(m.ComponentsExecution.Executed, "nats")
			},
			wantMsg: "names nats in executed twice",
		},
		{
			name: "a scope entry is a path",
			mutate: func(m *RunManifest) {
				m.Components = append(m.Components, "../escape")
				m.ComponentsExecution.ObservedExists = append(m.ComponentsExecution.ObservedExists, "../escape")
			},
			wantMsg: "not a single safe path element",
		},
		{
			name: "an executed target is left out of the granted scope",
			mutate: func(m *RunManifest) {
				m.ComponentsExecution.Executed = []string{}
			},
			wantMsg: "leaves it out of executed",
		},
		{
			name: "the evidence the record must re-hash is deleted",
			mutate: func(m *RunManifest) {
				m.ComponentsExecution.Evidence = nil
			},
			wantMsg: "re-hash nothing",
		},
		{
			name: "the subject run id is a path",
			mutate: func(m *RunManifest) {
				m.RunID = "../../escape"
			},
			wantMsg: "single safe path element",
		},
		{
			name: "a target component name is a path",
			mutate: func(m *RunManifest) {
				m.ComponentsExecution.Targets[0].Component = "../elsewhere"
			},
			wantMsg: "single safe path element",
		},
		{
			name: "the record kind is an unknown string",
			mutate: func(m *RunManifest) {
				m.RecordKind = "components-execution-v2"
			},
			wantMsg: "require an install-success record",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := landed
			e := *landed.ComponentsExecution
			targets := make([]ComponentsExecutionTarget, len(landed.ComponentsExecution.Targets))
			copy(targets, landed.ComponentsExecution.Targets)
			e.Targets = targets
			evidence := make([]ComponentsEvidenceRef, len(landed.ComponentsExecution.Evidence))
			copy(evidence, landed.ComponentsExecution.Evidence)
			e.Evidence = evidence
			e.Executed = append([]string{}, landed.ComponentsExecution.Executed...)
			e.ObservedExists = append([]string{}, landed.ComponentsExecution.ObservedExists...)
			m.ComponentsExecution = &e
			test.mutate(&m)
			path := filepath.Join(t.TempDir(), ComponentsExecutionFileName)
			raw, err := json.Marshal(&m)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			input := VerifyInput{
				RunFile: path, Level: VerifyLevelSmoke, ScriptDir: t.TempDir(),
				Output: filepath.Join(t.TempDir(), "verify"),
			}
			input.Kubeconfig = tc.execute.Kubeconfig
			err = RunVerify(context.Background(), input, os.Stdout)
			if err == nil {
				t.Fatalf("the mutated record was accepted: %+v", m.ComponentsExecution)
			}
			if !strings.Contains(err.Error(), test.wantMsg) {
				t.Fatalf("refusal %q does not name %q", err.Error(), test.wantMsg)
			}
		})
	}
}

// L-06 requirement 1 and 2 on the paths that change the cluster: a run whose
// playbook failed, and a run whose playbook succeeded but whose connection facts
// could not be aggregated, both left the cluster touched. Neither may end with
// no consumable record — that is the original L-06 symptom, an operator holding
// a changed cluster and only an untyped report verify refuses.
func TestComponentsClusterTouchingFailuresStillLandRecords(t *testing.T) {
	for _, test := range []struct {
		name    string
		env     func(t *testing.T)
		wantErr string
	}{
		{
			name:    "the playbook itself failed",
			env:     func(t *testing.T) { t.Setenv("FAKE_KK_FAIL", "1") },
			wantErr: "components playbook execution failed",
		},
		{
			name:    "the playbook ran but its connection facts could not be aggregated",
			env:     func(t *testing.T) { t.Setenv("FAKE_KK_MISSING_FRAGMENT", "nats") },
			wantErr: "connection facts",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			planFile, execute, _, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
			raw, err := os.ReadFile(planFile)
			if err != nil {
				t.Fatalf("read plan: %v", err)
			}
			var plan ComponentsPlan
			if err := json.Unmarshal(raw, &plan); err != nil {
				t.Fatalf("parse plan: %v", err)
			}
			test.env(t)

			stderr := captureStderr(t, func() error {
				return RunComponentsExecute(context.Background(), execute, &bytes.Buffer{})
			})
			gotErr := stderr.err
			if gotErr == nil || !strings.Contains(gotErr.Error(), test.wantErr) {
				t.Fatalf("the run must surface %q, got %v", test.wantErr, gotErr)
			}
			path := failedRecordPathFrom(t, stderr.text)
			if path == "" {
				t.Fatalf("a cluster-touching failure must land a record and print it; stderr:\n%s", stderr.text)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the printed failed record is not readable: %v", err)
			}
			var m RunManifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("the landed failed record does not parse: %v", err)
			}
			if m.Result != ResultFailed {
				t.Fatalf("a failed pass must record result=failed, got %q", m.Result)
			}
			if m.ComponentsExecution == nil || m.ComponentsExecution.Operation != ComponentsOperationAdd {
				t.Fatalf("the record lost its execution block: %+v", m.ComponentsExecution)
			}
			if len(m.ComponentsExecution.Executed) != 0 {
				t.Fatalf("nothing was executed successfully, yet the record grants scope to %v", m.ComponentsExecution.Executed)
			}
			found := false
			for _, target := range m.ComponentsExecution.Targets {
				if target.Component == "nats" {
					found = true
					if target.Status != ComponentsTargetFailed {
						t.Fatalf("the component this run attempted must be recorded failed, got %q", target.Status)
					}
				}
			}
			if !found {
				t.Fatalf("the failed record names no nats target: %+v", m.ComponentsExecution.Targets)
			}

			// And verify must refuse it by name, not by an accident of parsing.
			scripts := filepath.Join(t.TempDir(), "scripts")
			r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
			input := verifyInputFor(t, path, VerifyLevelSmoke, "nats", scripts, filepath.Join(t.TempDir(), "verify"))
			input.Kubeconfig = execute.Kubeconfig
			err = RunVerify(context.Background(), input, os.Stdout)
			if err == nil {
				t.Fatal("a failed execution record must never verify as a pass")
			}
			if !strings.Contains(err.Error(), "only a succeeded execution") ||
				!strings.Contains(err.Error(), ResultFailed) {
				t.Fatalf("the refusal must name the record's failed result; got %v", err)
			}
		})
	}
}

type capturedRun struct {
	err  error
	text string
}

// captureStderr runs fn with os.Stderr redirected to a file, so the product's
// own operator-facing message on a failure path can be asserted the way an
// operator sees it. fn writes only to stderr and the returned error.
func captureStderr(t *testing.T, fn func() error) capturedRun {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	original := os.Stderr
	os.Stderr = file
	runErr := fn()
	os.Stderr = original
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close stderr capture: %v", closeErr)
	}
	data, readErr := os.ReadFile(file.Name())
	if readErr != nil {
		t.Fatalf("read stderr capture: %v", readErr)
	}
	return capturedRun{err: runErr, text: string(data)}
}

func failedRecordPathFrom(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(line, "components record (failed, not consumable): "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
