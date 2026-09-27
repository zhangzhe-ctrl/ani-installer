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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// F-remediation G2 regressions (F02 execute re-verification, F03 live base
// identity, F08 ownership/current revision). Fixtures reuse the r15 fake
// kubectl/knobs; all writes go to temp dirs. No live cluster is contacted.
// ---------------------------------------------------------------------------

// T12 / F03: same config digests, DIFFERENT live cluster (kube-system UID) →
// the plan refuses and writes nothing.
func TestFRemediationG2_WrongClusterRejected(t *testing.T) {
	input, outDir := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "absent")
	t.Setenv("FAKE_CLUSTER_UID", "uid-kube-SOME-OTHER-CLUSTER")
	err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "different cluster") {
		t.Fatalf("a different live cluster must be refused, got %v", err)
	}
	if entries, readErr := os.ReadDir(outDir); readErr == nil && len(entries) != 0 {
		t.Fatalf("a refused plan writes nothing, found %v", entries)
	}
}

// T13 / F02: a NotReady node in the live cluster fails the plan's health gate.
func TestFRemediationG2_NotReadyNodeRejected(t *testing.T) {
	input, _ := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "absent")
	t.Setenv("FAKE_NODE_NOT_READY", "1")
	err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "NotReady") {
		t.Fatalf("a NotReady node set must be refused, got %v", err)
	}
}

// T13 / F03: a healthy node set is not enough — the base stack's own CNI
// workloads must be fully ready, and their readiness must be a recorded live
// fact rather than an assumption.
func TestFRemediationG2_UnhealthyCNIRejected(t *testing.T) {
	input, outDir := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "absent")
	t.Setenv("FAKE_CNI_UNHEALTHY", "1")
	err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "kcn-cni-ds has 1/3 pods ready") {
		t.Fatalf("a half-deployed CNI must be refused by name and count, got %v", err)
	}
	if entries, readErr := os.ReadDir(outDir); readErr == nil && len(entries) != 0 {
		t.Fatalf("a refused plan writes nothing, found %v", entries)
	}

	// Positive control: the healthy case records the probe as a live fact.
	t.Setenv("FAKE_CNI_UNHEALTHY", "")
	input2, outDir2 := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "absent")
	if err := RunComponentsInstallPlan(context.Background(), input2, os.Stdout); err != nil {
		t.Fatalf("a healthy CNI must plan: %v", err)
	}
	plan := r15ReadPlan(t, outDir2)
	if plan.LiveChecks["cni/kcn-system/kcn-cni-ds"] != "3/3 ready" ||
		plan.LiveChecks["cni/kcn-system/kcn-ovs-ds"] != "3/3 ready" {
		t.Fatalf("the plan must record the CNI health it verified: %+v", plan.LiveChecks)
	}

	// An unknown stack has no probe list, and that is a refusal too.
	cluster, err := ParseClusterConfig([]byte(r15SiteWithComponents(5000, "  nats: {enabled: true}")))
	if err != nil {
		t.Fatal(err)
	}
	cluster.Network.Stack = "calico"
	if _, err := cniHealthProbes(cluster.Network.Stack); err == nil ||
		!strings.Contains(err.Error(), "no known CNI workloads") {
		t.Fatalf("an unsupported stack must be refused, got %v", err)
	}
	// Both supported stacks have explicit probe lists; the empty default is kcn.
	kubeovnProbes, err := cniHealthProbes("kubeovn")
	if err != nil || len(kubeovnProbes) != 2 || kubeovnProbes[0].name != "kube-ovn-cni" {
		t.Fatalf("kubeovn must probe its own CNI workloads, got %v (%v)", kubeovnProbes, err)
	}
	defaultProbes, err := cniHealthProbes("")
	if err != nil || len(defaultProbes) != 2 || defaultProbes[0].name != "kcn-cni-ds" {
		t.Fatalf("the default stack must probe the kcn workloads, got %v (%v)", defaultProbes, err)
	}
}

// T21 / F03: a Forbidden namespace probe is NOT treated as absent.
func TestFRemediationG2_ForbiddenIsNotNotFound(t *testing.T) {
	input, _ := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "absent")
	t.Setenv("FAKE_NS_FORBIDDEN", "1")
	err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "not NotFound") {
		t.Fatalf("Forbidden must refuse the plan, got %v", err)
	}
}

// T19 / F08: a newer FAILED revision decides the outcome even while an old
// revision is still deployed.
func TestFRemediationG2_CurrentFailedRevisionRejected(t *testing.T) {
	input, _ := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "v2-failed")
	err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "failed/pending") {
		t.Fatalf("a current failed revision must be refused, got %v", err)
	}
}

// T20 / F08: chart+version match WITHOUT the ANI ownership marker is foreign:
// refused, never adopted, never upgraded.
func TestFRemediationG2_SameChartNoMarkerRejected(t *testing.T) {
	input, _ := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "no-marker")
	err := RunComponentsInstallPlan(context.Background(), input, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "matches the chart but carries no ani.io/managed-by") {
		t.Fatalf("an unmarked same-chart release must be refused, got %v", err)
	}
	// The marked own release still plans as already_installed (positive control).
	input2, outDir := r15PlanFixture(t, "  nats: {enabled: true}", "nats", false, "ours")
	if err := RunComponentsInstallPlan(context.Background(), input2, os.Stdout); err != nil {
		t.Fatalf("our marked release must plan cleanly: %v", err)
	}
	if plan := r15ReadPlan(t, outDir); plan.Components[0].Status != "already_installed" {
		t.Fatalf("expected already_installed, got %+v", plan.Components[0])
	}
}

// T18 / F02: a tampered plan (illegal status / duplicate row) is rejected by
// execute before anything is inspected or written.
func TestFRemediationG2_TamperedPlanRejected(t *testing.T) {
	_, execute, kkLog, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	os.Remove(kkLog)
	data, err := os.ReadFile(execute.PlanFile)
	if err != nil {
		t.Fatal(err)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	plan.Components[0].Status = "install-me-please"
	tampered, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execute.PlanFile, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "illegal status") {
		t.Fatalf("an illegal plan status must be refused, got %v", err)
	}
	if _, statErr := os.Stat(kkLog); !os.IsNotExist(statErr) {
		t.Fatal("the playbook must never run for a tampered plan")
	}

	// digests truncated to a short string must be refused, not panicked.
	plan.Components[0].Status = "planned"
	plan.KKBinaryDigest = "abc"
	tampered, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execute.PlanFile, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err == nil ||
		!strings.Contains(err.Error(), "identity binding") {
		t.Fatalf("a plan without a well-formed runner digest must be refused, got %v", err)
	}
}

// T15 / F02: the live scene drifts AFTER planning (the release appears);
// execute must refuse before writing, and the playbook must never run.
func TestFRemediationG2_SceneDriftAfterPlanRefusedBeforeWrite(t *testing.T) {
	_, execute, kkLog, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	// Flip the fixture: now a foreign release exists at execute time.
	t.Setenv("FAKE_NATS_RELEASE", "foreign")
	foreign := r15HelmSecret(t, filepath.Dir(execute.ConfigFile), "foreign")
	t.Setenv("FAKE_FOREIGN_SECRET_FILE", foreign)
	os.Remove(kkLog)
	err := RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "ownership changed after planning") {
		t.Fatalf("post-plan drift must force a re-plan, got %v", err)
	}
	if _, statErr := os.Stat(kkLog); !os.IsNotExist(statErr) {
		t.Fatal("no playbook run may start after a refused re-verification")
	}
}

// T16 / F02: acceptance, the first install, and execute share ONE product
// lock: while a holder exists, execute never reaches the write path.
func TestFRemediationG2_SharedProductLock(t *testing.T) {
	_, execute, kkLog, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	os.Remove(kkLog)
	release, err := AcquireInstallFlock(productLockPath())
	if err != nil {
		t.Fatalf("take the product lock as the competing writer: %v", err)
	}
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	release()
	if err == nil || !strings.Contains(err.Error(), "another installer process holds") {
		t.Fatalf("execute must refuse while the product lock is held, got %v", err)
	}
	if _, statErr := os.Stat(kkLog); !os.IsNotExist(statErr) {
		t.Fatal("the playbook must not run behind a held lock")
	}
}

// T17 / F02: running from a host that is not the installer node's address is
// refused — the local lock protects nothing across machines.
func TestFRemediationG2_WrongExecutionHost(t *testing.T) {
	cluster, err := ParseClusterConfig([]byte(r15SiteWithComponents(5000, "  nats: {enabled: true}")))
	if err != nil {
		t.Fatal(err)
	}
	cluster.Nodes[0].Address = "198.51.100.77" // TEST-NET-2, never this host
	if err := verifyInstallerExecutionHost(cluster); err == nil ||
		!strings.Contains(err.Error(), "does not own the installer node address") {
		t.Fatalf("a non-installer host must be refused, got %v", err)
	}
	cluster.Nodes[0].Address = "127.0.0.1"
	if err := verifyInstallerExecutionHost(cluster); err != nil {
		t.Fatalf("the installer address must be accepted: %v", err)
	}
}

// T14 / F02: the kubeconfig used for the plan checks is exported to the real
// playbook child (no HOME/KUBECONFIG drift), and a wrong --base-run than the
// plan bound is refused before any write.
func TestFRemediationG2_KubeconfigPropagatesAndBaseRunBound(t *testing.T) {
	planFile, execute, kkLog, outDir := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	_ = planFile
	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err != nil {
		t.Fatalf("happy path execute: %v", err)
	}
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(execute.ConfigFile), "kk-env.log"))
	if err != nil {
		t.Fatalf("read the child environment capture: %v", err)
	}
	if !strings.Contains(string(calls), "KUBECONFIG="+execute.Kubeconfig) {
		t.Fatalf("the child playbook runner must carry the pinned kubeconfig:\n%s", calls)
	}
	// Wrong base-run for the same plan: refused.
	badDir := t.TempDir()
	badRecord := filepath.Join(badDir, "run.json")
	other, err := ParseClusterConfig([]byte(r15SiteWithComponents(5000, "  certManager: {enabled: true}")))
	if err != nil {
		t.Fatal(err)
	}
	r15SuccessBaseRun(t, other, badRecord)
	execute.BaseRunFile = badRecord
	os.Remove(kkLog)
	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err == nil ||
		!strings.Contains(err.Error(), "the plan was built against") {
		t.Fatalf("a different base-run than the plan bound must be refused, got %v", err)
	}
	_ = outDir
}

// T18 / F02: the plan is untrusted input. Path-shaped fields are validated
// before anything is read, so a rewritten plan cannot steer the executor at an
// arbitrary file or escape its output directory.
func TestFRemediationG2_PlanPathFieldsValidated(t *testing.T) {
	planFile, execute, _, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	data, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatal(err)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		mutate  func()
		wantErr string
	}{
		{
			name: "run id escaping its directory",
			mutate: func() {
				plan.RunID = "ani-components-../../etc"
			},
			wantErr: "not a single safe path element",
		},
		{
			name: "relative base run path",
			mutate: func() {
				plan.BaseRunFile = "run.json"
			},
			wantErr: "absolute clean path",
		},
		{
			name: "non-clean base run path",
			mutate: func() {
				// Built by concatenation on purpose: filepath.Clean() would
				// collapse the ".." away and the plan would look clean.
				plan.BaseRunFile = filepath.Dir(planFile) + "/../" + filepath.Base(planFile)
			},
			wantErr: "absolute clean path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh := ComponentsPlan{}
			if err := json.Unmarshal(data, &fresh); err != nil {
				t.Fatal(err)
			}
			plan = fresh
			tc.mutate()
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(execute.PlanFile, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			err = RunComponentsExecute(context.Background(), execute, os.Stdout)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// T43 / F02 small regression: two consecutive additions of DIFFERENT
// components into the same output root. Each run owns its plan file, new run
// record, runtime root, log and connections document, and neither run may
// overwrite the other's or the base install's record.
func TestFRemediationG2_ConsecutiveAdditionsStaySeparate(t *testing.T) {
	// The claim itself must be collision-free: two derivations inside one
	// second get different ids.
	claimDir := t.TempDir()
	idA, dirA, err := claimComponentsRunDir(claimDir, "ani-lab")
	if err != nil {
		t.Fatalf("claim the first run: %v", err)
	}
	idA2, dirA2, err := claimComponentsRunDir(claimDir, "ani-lab")
	if err != nil {
		t.Fatalf("claim the second run: %v", err)
	}
	if idA == idA2 || dirA == dirA2 {
		t.Fatalf("same-second run ids must not collide: %s vs %s", idA, idA2)
	}

	// One fixture, two plans of different scope, both executed.
	input, outDir := r15PlanFixture(t, "  certManager: {enabled: true}\n  nats: {enabled: true}\n  valkey: {enabled: true}", "nats", false, "absent")
	baseDir := filepath.Dir(input.ConfigFile)
	binDir := filepath.Join(baseDir, "kkbin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r15FakeKK(t, binDir)
	input.KKBin = filepath.Join(binDir, "kk")
	input.PlanKKDigestOverride = fileSHA256Hex(input.KKBin)

	// Addition #1: nats.
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("plan nats: %v", err)
	}
	planFile1 := r15OnlyPlanFile(t, outDir)
	plan1, err := os.ReadFile(planFile1)
	if err != nil {
		t.Fatal(err)
	}
	var componentsPlan1 ComponentsPlan
	if err := json.Unmarshal(plan1, &componentsPlan1); err != nil {
		t.Fatal(err)
	}
	if len(componentsPlan1.Components) != 1 || componentsPlan1.Components[0].Component != "nats" {
		t.Fatalf("run 1 must scope nats alone: %+v", componentsPlan1.Components)
	}
	record1 := filepath.Join(outDir, "runs", componentsPlan1.RunID, RunManifestFileName)
	recordBefore, err := os.ReadFile(record1)
	if err != nil {
		t.Fatalf("run 1 must write its own run record: %v", err)
	}
	baseRecordBefore, err := os.ReadFile(input.BaseRunFile)
	if err != nil {
		t.Fatal(err)
	}
	execute1, kkLog := r15ExecuteInput(t, input, outDir, planFile1)
	if err := RunComponentsExecute(context.Background(), execute1, os.Stdout); err != nil {
		t.Fatalf("execute nats: %v", err)
	}
	connections1 := filepath.Join(execute1.Output, "components-"+componentsPlan1.RunID, "connections.md")
	connectionsBefore, err := os.ReadFile(connections1)
	if err != nil {
		t.Fatalf("run 1 must write its own connections document: %v", err)
	}
	if !strings.Contains(string(connectionsBefore), "NATS connection facts") {
		t.Fatalf("run 1's connections document must hold its own facts:\n%s", connectionsBefore)
	}

	// Addition #2: valkey, same output root, same second is allowed.
	input.Only = []string{"valkey"}
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("plan valkey: %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var planFiles []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "components-plan-") {
			planFiles = append(planFiles, filepath.Join(outDir, entry.Name()))
		}
	}
	if len(planFiles) != 2 {
		t.Fatalf("two additions must leave two plan files: %v", planFiles)
	}
	var componentsPlan2 ComponentsPlan
	for _, candidate := range planFiles {
		if candidate == planFile1 {
			continue
		}
		data, readErr := os.ReadFile(candidate)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := json.Unmarshal(data, &componentsPlan2); err != nil {
			t.Fatal(err)
		}
	}
	if len(componentsPlan2.Components) != 1 || componentsPlan2.Components[0].Component != "valkey" {
		t.Fatalf("run 2 must scope valkey alone: %+v", componentsPlan2.Components)
	}
	if componentsPlan2.RunID == componentsPlan1.RunID {
		t.Fatalf("the second addition reused the first run id %s", componentsPlan2.RunID)
	}
	execute2, _ := r15ExecuteInput(t, input, outDir, filepath.Join(outDir, "components-plan-"+componentsPlan2.RunID+".json"))
	if err := RunComponentsExecute(context.Background(), execute2, os.Stdout); err != nil {
		t.Fatalf("execute valkey: %v", err)
	}

	// Run 1's evidence survived run 2 untouched, at its own paths.
	if now, err := os.ReadFile(planFile1); err != nil || string(now) != string(plan1) {
		t.Fatalf("run 2 must not rewrite run 1's plan (%v)", err)
	}
	if now, err := os.ReadFile(record1); err != nil || string(now) != string(recordBefore) {
		t.Fatalf("run 2 must not rewrite run 1's run record (%v)", err)
	}
	if now, err := os.ReadFile(connections1); err != nil || string(now) != string(connectionsBefore) {
		t.Fatalf("run 2 must not rewrite run 1's connections document (%v)", err)
	}
	connections2, err := os.ReadFile(filepath.Join(execute2.Output, "components-"+componentsPlan2.RunID, "connections.md"))
	if err != nil {
		t.Fatalf("run 2 must write its own connections document: %v", err)
	}
	if !strings.Contains(string(connections2), "valkey connection facts") || strings.Contains(string(connections2), "NATS") {
		t.Fatalf("run 2's connections must hold only its own component's facts:\n%s", connections2)
	}
	if now, err := os.ReadFile(input.BaseRunFile); err != nil || string(now) != string(baseRecordBefore) {
		t.Fatalf("a components addition must never rewrite the base install record (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "runs", componentsPlan2.RunID, RunManifestFileName)); err != nil {
		t.Fatalf("run 2 must write its own run record: %v", err)
	}

	// Each run's runtime root, log and connections stay separate.
	runtimeRoots := map[string]string{
		componentsPlan1.RunID: filepath.Join(execute1.Output, "components-"+componentsPlan1.RunID),
		componentsPlan2.RunID: filepath.Join(execute2.Output, "components-"+componentsPlan2.RunID),
	}
	if runtimeRoots[componentsPlan1.RunID] == runtimeRoots[componentsPlan2.RunID] {
		t.Fatal("two runs must not share a runtime root")
	}
	for runID, root := range runtimeRoots {
		for _, want := range []string{
			filepath.Join(root, "logs", "components-install.log"),
			filepath.Join(root, "connections.md"),
			filepath.Join(root, "work", "config.yaml"),
		} {
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("run %s is missing its own %s: %v", runID, filepath.Base(want), err)
			}
		}
		report := filepath.Join(execute1.Output, "components-execute-"+runID+".json")
		data, err := os.ReadFile(report)
		if err != nil {
			t.Fatalf("run %s must write its own report: %v", runID, err)
		}
		var executed ComponentsExecuteReport
		if err := json.Unmarshal(data, &executed); err != nil {
			t.Fatalf("parse %s: %v", report, err)
		}
		if executed.RunID != runID {
			t.Fatalf("report %s belongs to run %q", report, executed.RunID)
		}
		if len(executed.Results) != 1 {
			t.Fatalf("run %s must report exactly its own component: %+v", runID, executed.Results)
		}
	}

	// The fake kk saw both playbooks and nothing else: two invocations, each
	// with its own rendered config in its own work dir.
	calls, err := os.ReadFile(kkLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 {
		t.Fatalf("both additions must each run the playbook once:\n%s", calls)
	}
	seen := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, "run builtin/core/playbooks/ani_components.yaml") {
			t.Fatalf("unexpected playbook invocation:\n%s", line)
		}
		if strings.Contains(line, "create cluster") || strings.Contains(line, "create_cluster") {
			t.Fatalf("create_cluster must never run:\n%s", line)
		}
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "-c" && i+1 < len(fields) {
				seen[fields[i+1]] = true
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("each run must render its own config; saw %v", seen)
	}
}

// §4.5 / F02: a playbook failure must still record every component of the run
// (planned → fail, already_installed untouched), write the report, and never
// replay the playbook.
func TestFRemediationG2_FailedRunRecordsEveryComponent(t *testing.T) {
	input, outDir := r15PlanFixture(t, "  nats: {enabled: true}\n  valkey: {enabled: true}", "nats,valkey", false, "ours")
	baseDir := filepath.Dir(input.ConfigFile)
	binDir := filepath.Join(baseDir, "kkbin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r15FakeKK(t, binDir)
	input.KKBin = filepath.Join(binDir, "kk")
	input.PlanKKDigestOverride = fileSHA256Hex(input.KKBin)
	if err := RunComponentsInstallPlan(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("plan nats,valkey: %v", err)
	}
	planFile := r15OnlyPlanFile(t, outDir)
	planData, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatal(err)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, component := range plan.Components {
		statuses[component.Component] = component.Status
	}
	if statuses["nats"] != "already_installed" || statuses["valkey"] != "planned" {
		t.Fatalf("the plan must mix an owned release with a new component: %+v", statuses)
	}

	execute, kkLog := r15ExecuteInput(t, input, outDir, planFile)
	t.Setenv("FAKE_KK_FAIL", "1")
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "components playbook execution failed") {
		t.Fatalf("a failing playbook must surface as an error, got %v", err)
	}

	reportData, err := os.ReadFile(filepath.Join(execute.Output, "components-execute-"+plan.RunID+".json"))
	if err != nil {
		t.Fatalf("the failed run must still write its report: %v", err)
	}
	var report ComponentsExecuteReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	if report.Overall != VerifyStatusFailed {
		t.Fatalf("the failed run reports %q", report.Overall)
	}
	got := map[string]ComponentsPlanComponent{}
	for _, result := range report.Results {
		got[result.Component] = result
	}
	if len(report.Results) != 2 {
		t.Fatalf("both components must be recorded, got %+v", report.Results)
	}
	if got["valkey"].Status != VerifyStatusFailed || !strings.Contains(got["valkey"].Detail, "never replays") {
		t.Fatalf("the planned component must be recorded as failed with the no-replay note: %+v", got["valkey"])
	}
	if got["nats"].Status != "already_installed" {
		t.Fatalf("the read-only component must keep its status: %+v", got["nats"])
	}

	// No silent retry: exactly one playbook invocation, and the log says so.
	calls, err := os.ReadFile(kkLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 1 {
		t.Fatalf("the failed run must invoke the playbook exactly once:\n%s", calls)
	}
	runLog, err := os.ReadFile(filepath.Join(execute.Output, "components-"+plan.RunID, "logs", "components-install.log"))
	if err != nil {
		t.Fatalf("the failed run keeps its log: %v", err)
	}
	if !strings.Contains(string(runLog), "components run failed") {
		t.Fatalf("the log must record the failure:\n%s", runLog)
	}
}

// §4.5 / F02: when the playbook ran but a component's connection fragment is
// missing, the run is not reported as a clean success and its outcome is still
// written — the aggregation failure must not swallow the executed actions.
func TestFRemediationG2_MissingFragmentFailsHonestly(t *testing.T) {
	planFile, execute, _, _ := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	planData, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatal(err)
	}
	var plan ComponentsPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_KK_MISSING_FRAGMENT", "nats")
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "connection facts") {
		t.Fatalf("a missing fragment must fail the run, got %v", err)
	}
	reportData, err := os.ReadFile(filepath.Join(execute.Output, "components-execute-"+plan.RunID+".json"))
	if err != nil {
		t.Fatalf("the run must record what it did even though aggregation failed: %v", err)
	}
	var report ComponentsExecuteReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	if report.Overall != VerifyStatusFailed || len(report.Results) != 1 ||
		report.Results[0].Status != "executed" ||
		!strings.Contains(report.Results[0].Detail, "connection facts could not be aggregated") {
		t.Fatalf("the report must say the playbook ran but aggregation failed: %+v", report)
	}
}

// F02/F12: the plan binds the binary that will run its playbook. Executing it
// with a different kk — same path or different path — is refused BEFORE any run
// directory, log or report of that run is created.
func TestFRemediationG2_ChildBinaryMustMatchPlanBinding(t *testing.T) {
	_, execute, kkLog, outDir := r15PlanAndExecute(t, "  nats: {enabled: true}", "absent")
	plannedKk := execute.KKBin

	// A different binary at a different path (an older release on PATH, say).
	otherDir := t.TempDir()
	other := filepath.Join(otherDir, "kk")
	data, err := os.ReadFile(plannedKk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, append(append([]byte(nil), data...), '\n'), 0o700); err != nil {
		t.Fatal(err)
	}
	os.Remove(kkLog)
	execute.KKBin = other
	err = RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "the plan is bound to kk digest") {
		t.Fatalf("a child kk that is not the planned binary must be refused, got %v", err)
	}
	if _, statErr := os.Stat(kkLog); !os.IsNotExist(statErr) {
		t.Fatal("the playbook must never run with an unbound binary")
	}
	// Nothing of that refused run was written: only run 1's own artifacts exist.
	entries, err := os.ReadDir(execute.Output)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read runtime output: %v", err)
	}
	roots := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "components-") {
			roots++
		}
	}
	if roots != 0 {
		t.Fatalf("a refused execution must not create a runtime root of its own; found %d in %v (plan output %s)", roots, entries, outDir)
	}
}
