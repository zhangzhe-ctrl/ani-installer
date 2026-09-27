/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

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

// ---------------------------------------------------------------------------
// C04 — an acceptance plan with more than one business target, and the one rule
// that makes that safe: a declared object can be changed at most once per base
// run, whoever asks, from whichever component, and whatever names they use.
//
// These run the production entry (RunVerify at --level acceptance) against the
// R13 fixture: a state-driven fake kubectl for reads and an isolated HTTP
// endpoint that answers DELETEs with real DeleteOptions semantics. Only the
// cluster itself is substituted; the plan, the preflight, the ledger, the
// precondition and the report are the shipped code.
// ---------------------------------------------------------------------------

// c04AddTargets registers targets for one test and takes them back afterwards,
// so a plan under test cannot leak into the next one.
func c04AddTargets(t *testing.T, entries map[string]acceptanceTarget) {
	t.Helper()
	for key := range entries {
		if _, taken := acceptanceTargets[key]; taken {
			t.Fatalf("target %q is already declared; a test must not overwrite production state", key)
		}
	}
	for key, value := range entries {
		acceptanceTargets[key] = value
	}
	t.Cleanup(func() {
		for key := range entries {
			delete(acceptanceTargets, key)
		}
	})
}

// c04SetPlans installs plans for one test and puts back exactly what was there
// before, including "there was nothing". A test that widens a component's check
// set has to be able to say so without leaving the widened version behind for
// every later test.
func c04SetPlans(t *testing.T, entries map[string]acceptancePlan) {
	t.Helper()
	previous := map[string]acceptancePlan{}
	for key, value := range entries {
		if old, existed := acceptancePlans[key]; existed {
			previous[key] = old
		}
		acceptancePlans[key] = value
	}
	t.Cleanup(func() {
		for key := range entries {
			if old, existed := previous[key]; existed {
				acceptancePlans[key] = old
				continue
			}
			delete(acceptancePlans, key)
		}
	})
}

// c04Pod seeds one pod's live uid in the fixture, so two business pods can hold
// two identities at the same time.
func c04Pod(t *testing.T, stateDir, name, uid string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "pod-uid-"+name), []byte(uid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func c04SecondInstance() acceptanceTarget {
	return acceptanceTarget{
		LedgerToken:    "postgresql-instance-1",
		Namespace:      "ani-platform",
		ControllerKind: "StatefulSet",
		ControllerName: "postgresql",
		PodName:        "postgresql-1",
		PVCName:        "data-postgresql-1",
		Container:      "postgresql",
		Protocol:       acceptancePostgresSQL,
	}
}

func c04Run(t *testing.T, runFile, stateFile, only, output string) error {
	t.Helper()
	input := VerifyInput{
		RunFile: runFile, StateFile: stateFile, Level: VerifyLevelAcceptance,
		AllowPodRecreate: true, Output: output,
		Kubeconfig: filepath.Join(filepath.Dir(output), "kubeconfig"),
	}
	if only != "" {
		input.Only = strings.Split(only, ",")
	}
	stdout := &bytes.Buffer{}
	err := RunVerify(context.Background(), input, stdout)
	if err != nil {
		t.Logf("verify stdout:\n%s", stdout.String())
	}
	return err
}

// c04DeleteCount counts the deletes the isolated endpoint was actually asked to
// honour, which is the only count that means anything for a quota.
func c04DeleteCount(t *testing.T, stateDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "delete-uid-preconditions"))
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(data)))
}

func TestC04_TwoBusinessPodsEachSpendOnlyTheirOwnBudget(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	c04AddTargets(t, map[string]acceptanceTarget{"postgresql-instance-1": c04SecondInstance()})
	c04SetPlans(t, map[string]acceptancePlan{
		"postgresql": {Steps: []acceptanceStep{
			recreateStep("PG-01", "instance 0", "postgresql"),
			recreateStep("PG-02", "instance 1", "postgresql-instance-1"),
		}},
	})
	c04Pod(t, stateDir, "postgresql-0", "uid-old-postgresql-0")
	c04Pod(t, stateDir, "postgresql-1", "uid-old-postgresql-1")
	t.Setenv("FAKE_POD_CLAIMS", "data-postgresql-0 data-postgresql-1")

	out := filepath.Join(baseDir, "verify-out")
	if err := c04Run(t, runFile, stateFile, "postgresql", out); err != nil {
		t.Fatalf("a plan whose two targets are both healthy must pass: %v", err)
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
	if len(report.Results) != 1 || len(report.Results[0].Steps) != 2 {
		t.Fatalf("the report must show both steps: %+v", report.Results)
	}
	for _, step := range report.Results[0].Steps {
		if step.Status != VerifyStatusPass || step.QuotaState != ledgerStateDone {
			t.Fatalf("step %s did not pass with its quota closed out: %+v", step.ID, step)
		}
	}
	if got := c04DeleteCount(t, stateDir); got != 2 {
		t.Fatalf("two declared targets must produce exactly two deletes, got %d", got)
	}
	uids := strings.Fields(string(readOrEmpty(t, filepath.Join(stateDir, "delete-uid-preconditions"))))
	if !strings.Contains(strings.Join(uids, " "), "uid-old-postgresql-0") ||
		!strings.Contains(strings.Join(uids, " "), "uid-old-postgresql-1") {
		t.Fatalf("each delete must carry its own pod's uid, got %v", uids)
	}
	if got := len(c04LedgerFiles(t, baseDir)); got != 2 {
		t.Fatalf("one ledger file per target, got %d", got)
	}

	// Re-running the whole plan changes nothing: both budgets are spent, and the
	// refusal happens before either protocol write.
	before := c04DeleteCount(t, stateDir)
	if err := c04Run(t, runFile, stateFile, "postgresql", filepath.Join(baseDir, "verify-out-2")); err == nil {
		t.Fatal("a replayed acceptance passed on spent budgets")
	}
	if after := c04DeleteCount(t, stateDir); after != before {
		t.Fatalf("the replay issued %d further deletes", after-before)
	}
	replay := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out-2")))
	if !strings.Contains(replay.Results[0].Detail, "already recorded") {
		t.Fatalf("the replay must say the budget was already spent: %+v", replay.Results[0])
	}
}

func TestC04_ASharedTargetCannotBeReopenedFromAnotherComponent(t *testing.T) {
	// The logging backend is one object that two components legitimately want to
	// certify. Keying the ledger by the object rather than the component is what
	// stops the second entry from getting a fresh delete.
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql", "component-a", "component-b")
	shared := acceptanceTarget{
		LedgerToken: "postgresql", Namespace: "ani-platform", ControllerKind: "StatefulSet",
		ControllerName: "postgresql", PodName: "postgresql-0", PVCName: "data-postgresql-0",
		Container: "postgresql", Protocol: acceptancePostgresSQL,
	}
	c04AddTargets(t, map[string]acceptanceTarget{"shared-backend": shared})
	c04SetPlans(t, map[string]acceptancePlan{
		"component-a": {Steps: []acceptanceStep{recreateStep("A-01", "a changes the shared object", "shared-backend")}},
		"component-b": {Steps: []acceptanceStep{recreateStep("B-01", "b changes the same object", "shared-backend")},
			Dependencies: []string{"shared-backend"}},
	})
	if err := c04Run(t, runFile, stateFile, "component-a", filepath.Join(baseDir, "out-a")); err != nil {
		t.Fatalf("the first component's acceptance must pass: %v", err)
	}
	if got := c04DeleteCount(t, stateDir); got != 1 {
		t.Fatalf("one shared target must be deleted once, got %d", got)
	}
	if err := c04Run(t, runFile, stateFile, "component-b", filepath.Join(baseDir, "out-b")); err == nil {
		t.Fatal("a second component reached the same object for a second delete")
	}
	if got := c04DeleteCount(t, stateDir); got != 1 {
		t.Fatalf("the second component issued %d extra deletes against a spent target", got-1)
	}
	second := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "out-b")))
	if !strings.Contains(second.Results[0].Detail, "already recorded") {
		t.Fatalf("the refusal must name the spent budget: %+v", second.Results[0])
	}
	// And a component that changes something belonging to another one must say
	// so in its own report rather than let --only hide it.
	if len(second.Results[0].Dependencies) != 1 || second.Results[0].Dependencies[0] != "shared-backend" {
		t.Fatalf("the dependency must be named in the report: %+v", second.Results[0])
	}
}

func TestC04_AliasAndOutputCannotReopenATarget(t *testing.T) {
	// The same object reached under a different step id, a different component
	// alias, or a different --output is still the same object.
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql", "alias-one", "alias-two")
	c04SetPlans(t, map[string]acceptancePlan{
		"alias-one": {Steps: []acceptanceStep{recreateStep("ONE-01", "first name", "postgresql")}},
		"alias-two": {Steps: []acceptanceStep{recreateStep("TOTALLY-DIFFERENT", "second name", "postgresql")}},
	})
	if err := c04Run(t, runFile, stateFile, "alias-one", filepath.Join(baseDir, "out-1")); err != nil {
		t.Fatalf("the first alias must pass: %v", err)
	}
	for index, output := range []string{"out-2", "out-3"} {
		if err := c04Run(t, runFile, stateFile, "alias-two", filepath.Join(baseDir, output)); err == nil {
			t.Fatalf("attempt %d reopened the target through an alias and a new --output", index)
		}
	}
	if got := c04DeleteCount(t, stateDir); got != 1 {
		t.Fatalf("the target was deleted %d times, want exactly 1", got)
	}
	// The ledger's own name is the proof of what the budget is keyed to: the
	// base run and the target, and nothing else.
	matches, _ := filepath.Glob(filepath.Join(baseDir, "acceptance-state", "ledger-*.json"))
	if len(matches) != 1 || !strings.Contains(filepath.Base(matches[0]), "-postgresql.json") {
		t.Fatalf("the quota file must be keyed to the base run and the target name, got %v", matches)
	}
}

func TestC04_AFailedStepStopsTheNextTargetsChange(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	c04AddTargets(t, map[string]acceptanceTarget{"postgresql-instance-1": c04SecondInstance()})
	c04SetPlans(t, map[string]acceptancePlan{
		"postgresql": {Steps: []acceptanceStep{
			recreateStep("PG-01", "first", "postgresql"),
			recreateStep("PG-02", "second", "postgresql-instance-1"),
		}},
	})
	c04Pod(t, stateDir, "postgresql-0", "uid-old-postgresql-0")
	c04Pod(t, stateDir, "postgresql-1", "uid-old-postgresql-1")
	t.Setenv("FAKE_POD_CLAIMS", "data-postgresql-0 data-postgresql-1")
	// The committed row does not come back, so the first step fails. The second
	// must then be recorded not_run and its pod must not be touched at all.
	t.Setenv("FAKE_DATA_LOST", "1")
	out := filepath.Join(baseDir, "verify-out")
	if err := c04Run(t, runFile, stateFile, "postgresql", out); err == nil {
		t.Fatal("a plan whose first durability check lost data must fail")
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
	steps := report.Results[0].Steps
	if len(steps) != 2 {
		t.Fatalf("both steps must be reported: %+v", steps)
	}
	if steps[0].Status != VerifyStatusFailed || steps[1].Status != VerifyStatusNotRun {
		t.Fatalf("the first step must fail and the second must be not_run, got %s / %s", steps[0].Status, steps[1].Status)
	}
	uids := strings.Join(strings.Fields(readOrEmpty(t, filepath.Join(stateDir, "delete-uid-preconditions"))), " ")
	if strings.Contains(uids, "uid-old-postgresql-1") {
		t.Fatalf("the stopped plan still changed the second target: %s", uids)
	}
}

func TestC04_IncompleteDeclarationsAreRefusedBeforeAnythingChanges(t *testing.T) {
	cases := map[string]struct {
		target acceptanceTarget
		want   string
	}{
		"no storage identity": {acceptanceTarget{
			LedgerToken: "loose", Namespace: "ani-platform", ControllerKind: "StatefulSet",
			ControllerName: "postgresql", PodName: "postgresql-0", Container: "postgresql",
			Protocol: acceptancePostgresSQL,
		}, "neither a PVC nor a hostPath"},
		"invented pvc plus hostpath": {acceptanceTarget{
			LedgerToken: "double", Namespace: "ani-platform", ControllerKind: "DaemonSet",
			ControllerName: "postgresql", PodName: "postgresql-0", PVCName: "x", HostPath: "/y",
			Container: "postgresql", Protocol: acceptancePostgresSQL,
		}, "one durable storage identity"},
		"hostpath on a statefulset": {acceptanceTarget{
			LedgerToken: "wrongkind", Namespace: "ani-platform", ControllerKind: "StatefulSet",
			ControllerName: "postgresql", PodName: "postgresql-0", HostPath: "/y",
			Container: "postgresql", Protocol: acceptancePostgresSQL,
		}, "per-node directory"},
		"no protocol": {acceptanceTarget{
			LedgerToken: "noprotocol", Namespace: "ani-platform", ControllerKind: "StatefulSet",
			ControllerName: "postgresql", PodName: "postgresql-0", PVCName: "data-postgresql-0",
			Container: "postgresql",
		}, "no data protocol"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			baseDir, stateDir := r13Prepare(t)
			runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
			c04AddTargets(t, map[string]acceptanceTarget{"incomplete": tc.target})
			c04SetPlans(t, map[string]acceptancePlan{
				"postgresql": {Steps: []acceptanceStep{recreateStep("X-01", "incomplete target", "incomplete")}},
			})
			out := filepath.Join(baseDir, "verify-out")
			if err := c04Run(t, runFile, stateFile, "postgresql", out); err == nil {
				t.Fatal("an incomplete declaration was accepted")
			}
			report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
			if !strings.Contains(report.Results[0].Detail, "refused before any change") {
				t.Fatalf("the refusal must say it happened before any change: %+v", report.Results[0])
			}
			if !strings.Contains(report.Results[0].Detail, tc.want) {
				t.Fatalf("the refusal must say %q: %+v", tc.want, report.Results[0])
			}
			if got := c04DeleteCount(t, stateDir); got != 0 {
				t.Fatalf("a refused plan changed something: %d deletes", got)
			}
			if got := len(c04LedgerFiles(t, baseDir)); got != 0 {
				t.Fatalf("a refused plan consumed quota: %d ledger file(s)", got)
			}
		})
	}
}

func TestC04_AHostPathTargetIsAuthorisedByItsNodeAndMount(t *testing.T) {
	collector := acceptanceTarget{
		LedgerToken: "collector-node-1", Namespace: "ani-observability",
		ControllerKind: "DaemonSet", ControllerName: "ani-fluent-bit",
		NodeName: "node1", PodSelector: "app.kubernetes.io/name=fluent-bit",
		HostPath: "/var/lib/ani-installer/fluent-bit", Container: "fluent-bit",
		Protocol: acceptancePostgresSQL, // the fixture's readable protocol
	}
	t.Run("a pod that really mounts the declared directory passes", func(t *testing.T) {
		baseDir, stateDir := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		c04AddTargets(t, map[string]acceptanceTarget{"collector": collector})
		c04SetPlans(t, map[string]acceptancePlan{
			"postgresql": {Steps: []acceptanceStep{recreateStep("DS-01", "one collector on node1", "collector")}},
		})
		t.Setenv("FAKE_DAEMON_POD", "ani-fluent-bit-1")
		c04Pod(t, stateDir, "ani-fluent-bit-1", "uid-collector-1")
		if err := os.WriteFile(filepath.Join(stateDir, "hostpath-paths"),
			[]byte("/var/lib/fluent-bit /var/lib/ani-installer/fluent-bit /var/run/secrets\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(baseDir, "verify-out")
		if err := c04Run(t, runFile, stateFile, "postgresql", out); err != nil {
			report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
			t.Fatalf("a daemon target with a real hostPath mount must be authorisable: %v\n%+v", err, report.Results[0])
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
		evidence := report.Results[0].Steps[0].Evidence
		if evidence["node"] != "node1" || evidence["hostPath"] != collector.HostPath || evidence["nodeUid"] == "" {
			t.Fatalf("the node and directory identities must be recorded: %+v", evidence)
		}
		if _, invented := evidence["pvc"]; invented {
			t.Fatalf("a hostPath target was given a PVC it does not have: %+v", evidence)
		}
	})

	t.Run("a pod that does not mount it is refused", func(t *testing.T) {
		baseDir, stateDir := r13Prepare(t)
		runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
		c04AddTargets(t, map[string]acceptanceTarget{"collector": collector})
		c04SetPlans(t, map[string]acceptancePlan{
			"postgresql": {Steps: []acceptanceStep{recreateStep("DS-01", "one collector on node1", "collector")}},
		})
		t.Setenv("FAKE_DAEMON_POD", "ani-fluent-bit-1")
		c04Pod(t, stateDir, "ani-fluent-bit-1", "uid-collector-1")
		if err := os.WriteFile(filepath.Join(stateDir, "hostpath-paths"),
			[]byte("/var/lib/fluent-bit /var/run/secrets\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(baseDir, "verify-out")
		if err := c04Run(t, runFile, stateFile, "postgresql", out); err == nil {
			t.Fatal("a collector that does not mount the declared directory was accepted")
		}
		report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
		if !strings.Contains(report.Results[0].Detail, "does not mount the declared hostPath") {
			t.Fatalf("the refusal must name the mount mismatch: %+v", report.Results[0])
		}
		if got := c04DeleteCount(t, stateDir); got != 0 {
			t.Fatalf("a target that failed authorisation was still deleted %d times", got)
		}
	})
}

func TestC04_TheLegacySingleTargetLedgerFileStillBlocks(t *testing.T) {
	// Before plans existed the file was named after the controller. A target may
	// now carry an explicit token, but a spent record written by the older build
	// has to keep blocking, or renaming a constant would re-arm every cluster
	// this project has already verified.
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	state := filepath.Join(baseDir, "acceptance-state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := acceptanceLedger{
		State: ledgerStateDone, RunID: "ani-ani-lab-20260924-130000", Target: "postgresql",
		OldPodUID: "uid-old-postgresql-0", OldPVCUID: "uid-pvc-data-postgresql-0",
		Controller: "StatefulSet/postgresql", Token: "ani-accept-legacy",
		StartedAt: time.Now().UTC().Format(time.RFC3339), FinishedAt: time.Now().UTC().Format(time.RFC3339),
		NewPodUID: "uid-new-1", Outcome: VerifyStatusPass,
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ledger-ani-ani-lab-20260924-130000-postgresql.json"),
		append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(baseDir, "verify-out")
	if err := c04Run(t, runFile, stateFile, "postgresql", out); err == nil {
		t.Fatal("a record left by the single-target build did not block the new entry")
	}
	if got := c04DeleteCount(t, stateDir); got != 0 {
		t.Fatalf("the legacy record was overridden and a delete issued (%d requests)", got)
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
	if !strings.Contains(report.Results[0].Detail, "already recorded as done") {
		t.Fatalf("the refusal must name the legacy state: %+v", report.Results[0])
	}
}

// c04LedgerFiles names the durable quota records this run wrote.
func c04LedgerFiles(t *testing.T, baseDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(baseDir, "acceptance-state", "ledger-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func readOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
