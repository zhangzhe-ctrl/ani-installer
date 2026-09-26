/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/yaml"
)

// ---------------------------------------------------------------------------
// C02 — cancelling a started smoke script has to stop the script's DESCENDANTS,
// not just the bash that CommandContext kills.
//
// exec.CommandContext installs its own Cancel (Process.Kill), so the previous
// `if cmd.Cancel == nil` guard never assigned the process-group kill: Setpgid
// built a group that nothing ever signalled. The old test only asserted that the
// parent script's LAST line (`touch finished`) never appeared — which the
// default Process.Kill satisfies while a backgrounded child carries on writing.
// This test makes a descendant, not the parent, the thing that must not happen.
// ---------------------------------------------------------------------------

func TestC02_CancelledSmokeStopsDescendantsNotJustTheParent(t *testing.T) {
	baseDir, _ := r13Prepare(t)
	scriptDir := filepath.Join(baseDir, "ani-scripts")
	if err := os.MkdirAll(filepath.Join(scriptDir, "nats"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(baseDir, "started")
	childPID := filepath.Join(baseDir, "child.pid")
	childWrote := filepath.Join(baseDir, "child-wrote")
	parentLastLine := filepath.Join(baseDir, "finished")

	// The subshell is a descendant in the SAME process group (no setsid — that
	// would be an escape, not a test of the fix). It records its own pid so the
	// deferred cleanup can reap exactly what this test created, and then performs
	// the write that must never happen once the task is cancelled.
	script := "#!/usr/bin/env bash\n" +
		"touch " + started + "\n" +
		"( echo $$ > " + childPID + "; sleep 2; touch " + childWrote + " ) &\n" +
		"sleep 30\n" +
		"touch " + parentLastLine + "\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "nats", "verify.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	// Reap whatever descendant survived, so a failing run leaves no orphan behind.
	t.Cleanup(func() {
		data, err := os.ReadFile(childPID)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(bytes.TrimSpace(data)))
		if err != nil || pid <= 0 {
			return
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(pid, 0); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("cleanup could not reap descendant pid %d", pid)
	})

	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "nats")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(started); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Let the descendant surely be spawned and inside its sleep before signal.
		time.Sleep(200 * time.Millisecond)
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

	if _, err := os.Stat(started); err != nil {
		t.Fatal("the smoke script should have started before cancellation")
	}
	if _, err := os.Stat(childWrote); err == nil {
		t.Fatal("cancellation let the smoke script's DESCENDANT keep running and write after the parent died; " +
			"the process-group Cancel was never registered (C02)")
	}
	if _, err := os.Stat(parentLastLine); err == nil {
		t.Fatal("cancellation ran the parent script to completion")
	}
	// The group kill must be observable while the descendant is still mid-sleep,
	// otherwise "it had not got there yet" could masquerade as "it was stopped".
	if data, err := os.ReadFile(childPID); err == nil {
		if pid, err := strconv.Atoi(string(bytes.TrimSpace(data))); err == nil && pid > 0 {
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatalf("descendant pid %d is still alive after the smoke cancellation returned", pid)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// C04 — the emitted acceptance Job has to be a Job the cluster can run.
//
// runCheckJob used to hand-build YAML whose `template:` block sat at the top
// level, so the object had no pod template at all. Every offline test that faked
// `apply` as "exit 0" was blind to it. These tests parse the manifest that
// actually leaves the process, and the fake apply now refuses the shape.
// ---------------------------------------------------------------------------

func TestC04_EmittedAcceptanceJobIsATypedJobWithAPodTemplate(t *testing.T) {
	job, err := buildCheckJob("ani-platform", "ani-acc-natspub-test", "reg.local/natsio/nats-box:0.19.7",
		map[string]string{"PLAIN": "value"},
		[]secretEnv{{name: "NATS_TOKEN", secret: "ani-nats-auth", key: "token"}},
		"set -e\necho ANI-NATS-PUBACK-OK\n")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := validateCheckJob(job); err != nil {
		t.Fatalf("the typed acceptance job must validate locally: %v", err)
	}
	manifest, err := acceptanceJobManifest(job)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	// Parse the bytes back, independently of the Go value, because what the
	// cluster sees is the document, not the struct.
	var round batchv1.Job
	if err := yaml.Unmarshal(manifest, &round); err != nil {
		t.Fatalf("the emitted manifest does not parse as a batch/v1 Job: %v\n%s", err, manifest)
	}
	if round.APIVersion != "batch/v1" || round.Kind != "Job" {
		t.Fatalf("emitted object is %s/%s: %s", round.APIVersion, round.Kind, manifest)
	}
	if len(round.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("spec.template.spec.containers must hold the client, got %d\n%s",
			len(round.Spec.Template.Spec.Containers), manifest)
	}
	if round.Spec.Template.Spec.RestartPolicy != "Never" {
		t.Fatalf("the pod template lost its restartPolicy: %s", round.Spec.Template.Spec.RestartPolicy)
	}
	c := round.Spec.Template.Spec.Containers[0]
	if c.Image == "" || len(c.Command) != 3 || c.Command[2] == "" {
		t.Fatalf("the client container is not runnable: %+v", c)
	}
	// The credential must travel as a Secret reference, never as a literal.
	var viaSecret bool
	for _, e := range c.Env {
		if e.Name == "PLAIN" && e.Value != "value" {
			t.Fatalf("plain env value was mangled: %+v", e)
		}
		if e.Name == "NATS_TOKEN" {
			if e.Value != "" {
				t.Fatalf("the token was written as a literal into the manifest: %+v", e)
			}
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
				e.ValueFrom.SecretKeyRef.Name != "ani-nats-auth" || e.ValueFrom.SecretKeyRef.Key != "token" {
				t.Fatalf("NATS_TOKEN is not sourced from the Secret key: %+v", e)
			}
			viaSecret = true
		}
	}
	if !viaSecret {
		t.Fatal("the acceptance job declares no NATS_TOKEN secret reference")
	}
	// The structural proof C04 asked for: the pod template lives under spec.
	if !regexp.MustCompile(`(?m)^spec:\n(.*\n)*?  template:\n(.*\n)*?      containers:\n`).Match(manifest) {
		t.Fatalf("the emitted YAML does not nest the pod template under spec:\n%s", manifest)
	}
}

func TestC04_AcceptanceQuotaIsNeverSpentOnAnUnimplementedSpecialist(t *testing.T) {
	cases := []struct {
		name string
		only []string
		want string
	}{
		{"metrics", []string{"metrics"}, "not implemented for metrics"},
		{"fluent-bit", []string{"fluent-bit"}, "not implemented for fluent-bit"},
		{"one good one bad", []string{"postgresql", "loki"}, "not implemented for loki"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseDir, stateDir := r13Prepare(t)
			runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql", "metrics", "fluent-bit", "loki")
			statePath := filepath.Join(baseDir, "acceptance-state")
			err := RunVerify(context.Background(), VerifyInput{
				RunFile:          runFile,
				StateFile:        stateFile,
				Level:            VerifyLevelAcceptance,
				Only:             tc.only,
				AllowPodRecreate: true,
				Output:           filepath.Join(baseDir, "verify-out"),
				Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected refusal naming %q, got %v", tc.want, err)
			}
			// Refused BEFORE the change: no ledger entry, no delete, no report.
			if entries, _ := os.ReadDir(statePath); len(entries) != 0 {
				t.Fatalf("a refused acceptance must not consume quota, found %v", entries)
			}
			if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
				t.Fatal("a refused acceptance deleted something")
			}
			if _, err := os.Stat(filepath.Join(baseDir, "verify-out")); err == nil {
				t.Fatal("a refused acceptance must not write a report")
			}
		})
	}
}

func TestC04_AcceptanceCannotPassOnNothingChecked(t *testing.T) {
	// A record whose whole scope lacks a declared check used to produce zero
	// results and `overall: pass` with exit 0.
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "metrics", "fluent-bit")
	err := RunVerify(context.Background(), VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           filepath.Join(baseDir, "verify-out"),
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no declared check") {
		t.Fatalf("an acceptance with nothing to check must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
		t.Fatal("the empty acceptance deleted something")
	}
}

func TestC04_DefaultAcceptanceRunsDeclaredTargetsAndNamesWhatItSkipped(t *testing.T) {
	baseDir, _ := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql", "metrics", "fluent-bit")
	out := filepath.Join(baseDir, "verify-out")
	var stdout bytes.Buffer
	if err := RunVerify(context.Background(), VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           out,
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}, &stdout); err != nil {
		t.Fatalf("acceptance over the declared target should pass: %v", err)
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, out))
	if len(report.Results) != 1 || report.Results[0].Component != "postgresql" {
		t.Fatalf("only the declared check may run: %+v", report.Results)
	}
	if strings.Join(report.NotDeclared, ",") != "metrics,fluent-bit" {
		t.Fatalf("the report must name what it did not check, got %v", report.NotDeclared)
	}
	if !strings.Contains(stdout.String(), "did NOT check") {
		t.Fatalf("the operator must see the gap on stdout:\n%s", stdout.String())
	}
}

// TestC04_TheOldMisNestedJobShapeIsRejected pins the defect itself. These are the
// bytes runCheckJob used to emit (`template:` at the document's top level, so the
// Job had no pod template); the checks added for C04 must refuse them. Without
// this, a future edit could reintroduce the shape and every green test would
// still pass, because the old fake `apply` accepted anything.
func TestC04_TheOldMisNestedJobShapeIsRejected(t *testing.T) {
	const oldShape = `apiVersion: batch/v1
kind: Job
metadata:
  name: ani-acc-natspub-deadbeef
  namespace: ani-platform
  labels:
    app.kubernetes.io/name: ani-acceptance
spec:
  backoffLimit: 0
template:
  metadata:
    labels:
      app.kubernetes.io/name: ani-acceptance
  spec:
    restartPolicy: Never
    containers:
      - name: client
        image: reg.local/natsio/nats-box:0.19.7
        imagePullPolicy: IfNotPresent
        command:
          - /bin/sh
          - -c
          - |
            echo ANI-NATS-PUBACK-OK
`
	var job batchv1.Job
	if err := yaml.Unmarshal([]byte(oldShape), &job); err != nil {
		t.Fatalf("the old document parses as a Job for the reviewer, so it must parse here too: %v", err)
	}
	if len(job.Spec.Template.Spec.Containers) != 0 {
		t.Fatal("the old mis-nested document supposedly carried a pod template under spec; re-check the fixture")
	}
	if err := validateCheckJob(&job); err == nil {
		t.Fatal("validateCheckJob accepted a Job with no spec.template containers")
	} else if !strings.Contains(err.Error(), "pod template") {
		t.Fatalf("the refusal must name the pod template, got %v", err)
	}
	if regexp.MustCompile(`(?m)^spec:\n(.*\n)*?  template:\n(.*\n)*?      containers:\n`).MatchString(oldShape) {
		t.Fatal("the nesting assertion would have accepted the old shape")
	}
}

// ---------------------------------------------------------------------------
// C01 — a verification is only worth what its live target is.
// ---------------------------------------------------------------------------

func TestC01_ARecordOfClusterBCannotBeVerifiedAgainstClusterA(t *testing.T) {
	// Two real, distinct cluster fingerprints. The record is genuine for one of
	// them; the kubeconfig in use reaches the other. The fake answers with
	// FAKE_CLUSTER_UID, exactly as the API server answers with the kube-system
	// namespace uid.
	for _, level := range []string{VerifyLevelSmoke, VerifyLevelAcceptance} {
		t.Run(level, func(t *testing.T) {
			baseDir, stateDir := r13Prepare(t)
			runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
			if level == VerifyLevelSmoke {
				r13StubScript(t, filepath.Join(baseDir, "ani-scripts"), "postgresql", "PG-OK")
			}
			t.Setenv("FAKE_CLUSTER_UID", "uid-a-completely-different-cluster")
			out := filepath.Join(baseDir, "verify-out")
			err := RunVerify(context.Background(), VerifyInput{
				RunFile:          runFile,
				StateFile:        stateFile,
				Level:            level,
				AllowPodRecreate: true,
				ScriptDir:        filepath.Join(baseDir, "ani-scripts"),
				Output:           out,
				Kubeconfig:       filepath.Join(baseDir, "kubeconfig-of-the-other-cluster"),
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "points at uid-a-completely-different-cluster") {
				t.Fatalf("%s must refuse a record from another cluster naming the live uid, got %v", level, err)
			}
			if !strings.Contains(err.Error(), "belongs to cluster uid uid-kube-system-audit") {
				t.Fatalf("the refusal must name the uid the record belongs to: %v", err)
			}
			// Zero business writes: no protocol SQL, no pod delete, no report.
			if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
				t.Fatalf("%s mutated the wrong cluster", level)
			}
			if data, err := os.ReadFile(filepath.Join(stateDir, "kubectl-calls.log")); err == nil {
				for _, verb := range []string{" delete ", " exec ", " apply "} {
					if strings.Contains(string(data), verb) {
						t.Fatalf("%s issued %q against the wrong cluster:\n%s", level, verb, data)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(baseDir, "acceptance-state")); !levelIsSmoke(level) && err == nil {
				if entries, _ := os.ReadDir(filepath.Join(baseDir, "acceptance-state")); len(entries) > 1 ||
					(len(entries) == 1 && !strings.HasSuffix(entries[0].Name(), ".lock")) {
					t.Fatalf("a refused acceptance consumed quota: %v", entries)
				}
			}
		})
	}
}

func levelIsSmoke(level string) bool { return level == VerifyLevelSmoke }

func TestC01_AcceptanceIsRefusedOnAHostThatDoesNotOwnTheInstallerNode(t *testing.T) {
	// The record is for the right cluster; only the machine differs. A copied
	// record plus a copied kubeconfig brings a different local flock, so the
	// single-installer-node topology refuses it instead of pretending the lock
	// still serialises anything (C01). 192.0.2.1 is TEST-NET-1 and is not an
	// address of this host.
	baseDir, stateDir := r13Prepare(t)
	runFile, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	data, err := os.ReadFile(runFile)
	if err != nil {
		t.Fatal(err)
	}
	var manifest RunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Installer.Address = "192.0.2.1"
	rewritten, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runFile, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(baseDir, "verify-out")
	err = RunVerify(context.Background(), VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           out,
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("acceptance from a non-installer host must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
		t.Fatal("the wrong host deleted a pod")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a refused acceptance wrote a report")
	}
}

// ---------------------------------------------------------------------------
// C03 — the authorized old Pod UID has to survive the race, so it must travel
// inside the delete request rather than be re-read next to it.
// ---------------------------------------------------------------------------

func runC03Acceptance(t *testing.T, baseDir, stateFile string) (error, string) {
	t.Helper()
	runFile, _ := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	out := filepath.Join(baseDir, "verify-out")
	err := RunVerify(context.Background(), VerifyInput{
		RunFile:          runFile,
		StateFile:        stateFile,
		Level:            VerifyLevelAcceptance,
		AllowPodRecreate: true,
		Output:           out,
		Kubeconfig:       filepath.Join(baseDir, "kubeconfig"),
	}, io.Discard)
	return err, r13FindAcceptanceReport(t, out)
}

func TestC03_TheDeleteCarriesTheAuthorizedPodUID(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	_, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	err, _ := runC03Acceptance(t, baseDir, stateFile)
	if err != nil {
		t.Fatalf("the clean recreation should pass: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "delete-uid-preconditions"))
	if err != nil {
		t.Fatalf("no delete ever named a uid precondition; the request was UID-unconditional (C03): %v", err)
	}
	got := strings.Fields(string(data))
	if len(got) != 1 {
		t.Fatalf("exactly one delete is allowed, got %v", got)
	}
	if got[0] != "uid-old-postgresql-0" {
		t.Fatalf("the delete was restricted to uid %q, not the authorized one", got[0])
	}
}

func TestC03_ReplacementAfterTheLastClientReadIsNotDeleted(t *testing.T) {
	// The window the reviewer named: the client's final re-read still shows the
	// authorized object, and the swap happens as the request is served. Only a
	// precondition carried BY the request can survive that, so the intruder must
	// still be standing and this run must report a refusal, not a recreation.
	baseDir, stateDir := r13Prepare(t)
	_, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	t.Setenv("FAKE_REPLACE_AFTER_LAST_GET", "1")
	err, _ := runC03Acceptance(t, baseDir, stateFile)
	if err == nil {
		t.Fatal("a same-name replacement that appeared after the last read was accepted")
	}
	if !strings.Contains(err.Error(), "uid") && !strings.Contains(err.Error(), "precondition") {
		// The error is the level failure; the per-component detail is in the report.
		report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
		if !strings.Contains(report.Results[0].Detail, "precondition") {
			t.Fatalf("the refusal must name the uid precondition: %+v", report.Results[0])
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
		t.Fatal("the intruder pod was deleted")
	}
	standing, _ := os.ReadFile(filepath.Join(stateDir, "pod-uid"))
	if strings.TrimSpace(string(standing)) != "uid-INTRUDER-replaced" {
		t.Fatalf("the object that stood here was not left alone: %q", standing)
	}
	// The delete must have asked for the AUTHORIZED uid, otherwise the refusal
	// came from something else and the precondition is still untested.
	preconditions, err := os.ReadFile(filepath.Join(stateDir, "delete-uid-preconditions"))
	if err != nil || !strings.Contains(string(preconditions), "uid-old-postgresql-0") {
		t.Fatalf("the request did not carry the authorized uid: %v %q", err, preconditions)
	}
	// And the failed attempt must not be replayable: its quota is spent/unknown.
	if entries, _ := os.ReadDir(filepath.Join(baseDir, "acceptance-state")); len(entries) == 0 {
		t.Fatal("the attempt left no durable ledger entry")
	}
}

func TestC03_OwnerAndStorageRelationshipsAreAuthorizingFacts(t *testing.T) {
	cases := []struct {
		name    string
		knob    string
		value   string
		message string
	}{
		{"controller flag missing", "FAKE_OWNER_NOT_CONTROLLER", "1", "not marked controller=true"},
		{"owner uid differs from the live controller", "FAKE_OWNER_UID", "uid-of-an-old-controller-generation", "different controller generation"},
		{"pod does not mount the declared PVC", "FAKE_POD_CLAIMS", "some-other-claim", "does not mount the declared PVC"},
		{"pv is bound to a different claim uid", "FAKE_PV_CLAIM_UID", "uid-of-another-pvc", "the binding is not this pair"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseDir, stateDir := r13Prepare(t)
			_, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
			t.Setenv(tc.knob, tc.value)
			err, _ := runC03Acceptance(t, baseDir, stateFile)
			if err == nil {
				t.Fatalf("acceptance passed without %s", tc.name)
			}
			report := r13ReadReport(t, r13FindAcceptanceReport(t, filepath.Join(baseDir, "verify-out")))
			if !strings.Contains(report.Results[0].Detail, tc.message) {
				t.Fatalf("expected %q in the refusal, got %+v", tc.message, report.Results[0])
			}
			// Refused before the mutation: the quota was never consumed, so no
			// ledger entry exists and nothing was deleted.
			if _, err := os.Stat(filepath.Join(stateDir, "deleted")); !os.IsNotExist(err) {
				t.Fatal("the pod was deleted despite the failed authorization")
			}
			entries, _ := os.ReadDir(filepath.Join(baseDir, "acceptance-state"))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "ledger-") {
					t.Fatalf("a rejected authorization spent the one-shot quota: %v", e.Name())
				}
			}
		})
	}
}

func TestC03_TheClosedLedgerKeepsEveryIdentityTheIntentCarried(t *testing.T) {
	baseDir, stateDir := r13Prepare(t)
	_, stateFile := r13RunRecord(t, baseDir, true, PhaseSucceeded, "postgresql")
	err, _ := runC03Acceptance(t, baseDir, stateFile)
	if err != nil {
		t.Fatalf("clean acceptance should pass: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(baseDir, "acceptance-state"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger *os.File
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ledger-") {
			ledger, err = os.Open(filepath.Join(baseDir, "acceptance-state", e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			var rec acceptanceLedger
			if err := json.NewDecoder(ledger).Decode(&rec); err != nil {
				t.Fatalf("decode ledger: %v", err)
			}
			if rec.State != ledgerStateDone || rec.Outcome != VerifyStatusPass {
				t.Fatalf("unexpected terminal ledger: %+v", rec)
			}
			if rec.OldPodUID != "uid-old-postgresql-0" || rec.OldPVCUID == "" {
				t.Fatalf("the terminal ledger dropped the authorized identities: %+v", rec)
			}
			if rec.Controller != "StatefulSet/postgresql" {
				t.Fatalf("the terminal ledger dropped the controller: %+v", rec)
			}
			if rec.NewPodUID == "" || rec.NewPodUID == rec.OldPodUID {
				t.Fatalf("the terminal ledger does not name the recreated pod: %+v", rec)
			}
			if rec.StartedAt == "" || rec.FinishedAt == "" {
				t.Fatalf("the terminal ledger lost its time bounds: %+v", rec)
			}
			return
		}
	}
	t.Fatalf("no ledger entry found among %v (and no writes were expected from %v)", entries, stateDir)
}
