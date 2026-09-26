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
	"strings"
	"sync"
	"testing"
	"text/template"

	"github.com/cockroachdb/errors"
)

// ---------------------------------------------------------------------------
// C08 — the Fluent Bit smoke probe has to own what it deletes.
//
// CLIENT_POD and the per-node marker pods used to carry fixed names
// (ani-fluent-bit-verify-client, ani-log-marker-1/2/3), and each helper began by
// deleting any pod of that name. Two overlapping smoke passes therefore deleted
// each other's live probes, and a run also cleared away the pod a previous
// attempt had left behind as evidence of its own failure.
//
// This test runs the REAL packaged checker twice at once against a fake that
// models pods with uids, then reads back which objects each attempt created and
// which it destroyed. It is deliberately not a check of generated strings: a name
// only matters through the API calls a script that actually ran issued.
// ---------------------------------------------------------------------------

const c08FakeKubectl = `#!/usr/bin/env bash
state="__STATE__"
log="$state/calls"
lock="$state/.lock"
podfile() { printf '%s' "$1" | tr -c 'a-zA-Z0-9._-' '_' ; }
call() {
  for i in $(seq 1 400); do
    mkdir "$lock" 2>/dev/null && { printf '%s\n' "$*" >> "$log"; rmdir "$lock"; return 0; }
    sleep 0.002
  done
  printf '%s\n' "$*" >> "$log"
}
call "$$" "$*"
args="$*"

# ---- the facts section [1] reads, so a run reaches the probe stage ----------
CONF='[SERVICE]\n    Flush      5\n[INPUT]\n    Name              tail\n    Path              /var/log/containers/*.log\n    DB_file           /var/lib/fluent-bit/tail.db\n    storage.type      filesystem\n    storage.sync      normal\n    Mem_Buf_Limit     5MB\n[OUTPUT]\n    Name          loki\n    Match         *\n    storage.path /var/lib/fluent-bit/buffers\n    storage.total_limit_size  1G\n    buffer\n    storage.type  filesystem\n'
case "$args" in
  *"get nodes"*"metadata.name"*)       printf 'node1\nnode2\n'; exit 0 ;;
  *"get nodes --no-headers"*)         printf 'node1   Ready\nnode2   Ready\n'; exit 0 ;;
  *"get daemonset"*"numberReady"*)    printf '2'; exit 0 ;;
  *"get daemonset"*"metadata.uid"*)   printf 'uid-ds-fluent-bit\n'; exit 0 ;;
  *"get daemonset"*"hostPath.path"*)  printf 'fluent-bit-state /var/lib/ani-installer/fluent-bit\n'; exit 0 ;;
  *"get daemonset"*) printf 'NAME   DESIRED   CURRENT   READY\nfluent-bit   2   2   2\n'; exit 0 ;;
  *"get pod"*"configMap.name"*) printf 'ani-fluent-bit-config\n'; exit 0 ;;
  *"get configmap"*"{.data}"*) printf '{"fluent-bit.conf":"%s"}\n' "$CONF"; exit 0 ;;
  *"get configmap"*) printf "$CONF"; exit 0 ;;
  *"wait --for=condition=Ready"*) exit 0 ;;
  *"get pod"*"-o wide"*) printf 'NAME   READY   STATUS   NODE\nfluent-bit-abc   1/1   Running   node1\n'; exit 0 ;;
  *"get pod"*"items[0]"*) printf 'fluent-bit-abc\n'; exit 0 ;;
esac

# ---- pod registry: one object per name, created by whoever got there first --
if [[ "$args" == *"get pod"*"metadata.uid"* ]]; then
  nm="$(printf '%s' "$args" | awk '{for(i=1;i<=NF;i++) if($i=="pod"){print $(i+1); exit}}')"
  cat "$state/pod-$(podfile "$nm")" 2>/dev/null
  exit 0
fi
if [[ "$args" == *" run "* ]]; then
  nm="$(printf '%s' "$args" | awk '{for(i=1;i<=NF;i++) if($i=="run"){print $(i+1); exit}}')"
  f="$state/pod-$(podfile "$nm")"
  if [ -e "$f" ]; then echo "Error from server (AlreadyExists)" >&2; exit 1; fi
  printf 'uid-%s\n' "$nm" > "$f"
  printf 'pod/%s created\n' "$nm"; exit 0
fi
if [[ "$args" == *"delete pod"* ]]; then
  nm="$(printf '%s' "$args" | awk '{for(i=1;i<=NF;i++) if($i=="pod"){print $(i+1); exit}}')"
  printf 'DELETE %s %s\n' "$$" "$nm" >> "$state/deletes"
  rm -f "$state/pod-$(podfile "$nm")"
  exit 0
fi
exit 1
`

func c08FakeCluster(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	state := filepath.Join(dir, "state")
	for _, d := range []string{bin, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"),
		[]byte(strings.ReplaceAll(c08FakeKubectl, "__STATE__", state)), 0o700); err != nil {
		t.Fatal(err)
	}
	return state
}

// c08RunChecker renders and executes the packaged checker exactly as the
// installer ships it. An unrendered file would exit at its backend selection
// before it created anything, and the test would then measure nothing.
func c08RunChecker(t *testing.T, home, pin string) string {
	t.Helper()
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "fluent-bit", "templates", "verify.sh")
	source, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read the packaged checker: %v", err)
	}
	tmpl, err := template.New("verify.sh").Parse(string(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, c07TemplateContext()); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(rendered.String(), "{{") {
		t.Fatal("the checker under test still holds template markers")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "verify.sh")
	if err := os.WriteFile(script, []byte(rendered.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _ := execBash(t, script,
		"PATH="+home+"/bin:"+os.Getenv("PATH"),
		"HOME="+filepath.Join(home, "home"),
		"ANI_VERIFY_KUBECONFIG="+pin,
		"KUBECONFIG="+pin,
		"ANI_VERIFY_OUTPUT_DIR="+filepath.Join(home, "out"),
		"ANI_VERIFY_LEVEL=smoke",
	)
	return out
}

func TestC08_OverlappingRunsNeverDeleteEachOthersProbes(t *testing.T) {
	a, _, home := c07DummyKubeconfigs(t)
	state := c08FakeCluster(t, home)

	// Objects an earlier attempt left behind, under the names the old script
	// used. A run that "cleans up first" deletes these; a run that only owns what
	// it created must not touch them.
	legacy := []string{"ani-fluent-bit-verify-client", "ani-log-marker-1"}
	for _, name := range legacy {
		if err := os.WriteFile(filepath.Join(state, "pod-"+name), []byte("uid-legacy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var transcripts []string
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := c08RunChecker(t, home, a)
			mu.Lock()
			transcripts = append(transcripts, out)
			mu.Unlock()
		}()
	}
	wg.Wait()
	transcript := strings.Join(transcripts, "\n--- second run ---\n")

	calls, err := os.ReadFile(filepath.Join(state, "calls"))
	if err != nil {
		t.Fatalf("no checker reached the API at all; nothing was tested.\n%s", transcript)
	}
	var creates []string
	for _, line := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(line, "DELETE") {
			continue
		}
		if strings.Contains(line, " run ") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "run" && i+1 < len(fields) {
					creates = append(creates, fields[i+1])
				}
			}
		}
	}
	var deletes []string
	if data, err := os.ReadFile(filepath.Join(state, "deletes")); err == nil {
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			fields := strings.Fields(l)
			if len(fields) >= 3 {
				deletes = append(deletes, fields[2])
			}
		}
	}
	if len(creates) < 2 {
		t.Fatalf("both runs must create their own probe to test anything; created %v.\ntranscript:\n%s", creates, transcript)
	}
	seen := map[string]int{}
	for _, name := range creates {
		for _, old := range legacy {
			if name == old {
				t.Fatalf("a run created the shared fixed name %q (C08)", name)
			}
		}
		seen[name]++
	}
	if len(seen) < 2 {
		t.Fatalf("both runs created the same probe name %v; names are not attempt-scoped", creates)
	}
	for _, name := range deletes {
		for _, old := range legacy {
			if name == old {
				t.Fatalf("a run deleted a leftover probe it did not create: %v", deletes)
			}
		}
	}
	for _, name := range legacy {
		if _, err := os.Stat(filepath.Join(state, "pod-"+name)); err != nil {
			t.Fatalf("the pre-existing %s is gone; a smoke pass destroyed another attempt's evidence", name)
		}
	}
}

// ---------------------------------------------------------------------------
// C05 — a satisfied dependency is not a plan error, and a no-op is not exempt
// from re-checking what it claims to have observed.
// ---------------------------------------------------------------------------

// c05Site is the reviewer's own scenario: OpenSearch enabled next to a Fluent Bit
// it depends on, so a plan that writes only OpenSearch necessarily has Fluent Bit
// inside its closure.
func c05OpenSearchSite(t *testing.T) ClusterConfig {
	t.Helper()
	site := r15SiteWithComponents(5000, strings.Join([]string{
		"  certManager: {enabled: true}",
		"  postgresql: {enabled: true, storageClass: ani-block}",
		"  metrics: {enabled: true}",
		"  logging: {backend: opensearch, storageClass: ani-block, storageSize: 10Gi, retentionDays: 3}",
	}, "\n"))
	cluster, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse the site: %v", err)
	}
	if err := Validate(cluster); err != nil {
		t.Fatalf("the site must be valid: %v", err)
	}
	return cluster
}

func TestC05_AlreadyInstalledDependencyIsStillAValidPlan(t *testing.T) {
	cluster := c05OpenSearchSite(t)
	// The whole closure of the request, exactly as the planner resolves it.
	closure, err := componentsScope(cluster, []string{"opensearch"})
	if err != nil {
		t.Fatalf("resolve the closure: %v", err)
	}
	if strings.Join(closure, ",") == "opensearch" {
		t.Fatal("the fixture no longer carries a dependency; OpenSearch must pull Fluent Bit into its closure")
	}
	// The state the old comparison refused: Fluent Bit is already installed, so
	// only OpenSearch is written.
	plan := ComponentsPlan{Components: []ComponentsPlanComponent{
		{Component: "opensearch", Status: "planned"},
		{Component: "fluent-bit", Status: "already_installed"},
	}}
	got, err := validatePlanClosure(plan, cluster, []string{"opensearch"})
	if err != nil {
		t.Fatalf("adding a component whose dependency is already satisfied must be executable, got %v", err)
	}
	if strings.Join(got, ",") != strings.Join(closure, ",") {
		t.Fatalf("the re-verified closure must be the full dependency set %v, got %v", closure, got)
	}
}

func TestC05_ClosureChangesAfterPlanningStillRefuse(t *testing.T) {
	cluster := c05OpenSearchSite(t)
	cases := []struct {
		name    string
		plan    ComponentsPlan
		planned []string
		want    string
	}{
		{
			// Fluent Bit dropped out of the plan while its dependency edge did
			// not: the plan no longer describes what this config needs.
			name:    "dependency vanished from the plan",
			plan:    ComponentsPlan{Components: []ComponentsPlanComponent{{Component: "opensearch", Status: "planned"}}},
			planned: []string{"opensearch"},
			want:    "brings in",
		},
		{
			// A component the config no longer enables is still listed as
			// planned, so executing it would write something unapproved.
			name: "component outside the enabled set",
			plan: ComponentsPlan{Components: []ComponentsPlanComponent{
				{Component: "opensearch", Status: "planned"},
				{Component: "fluent-bit", Status: "already_installed"},
				{Component: "valkey", Status: "planned"},
			}},
			planned: []string{"opensearch", "valkey"},
			want:    `is not enabled in the site config`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePlanClosure(tc.plan, cluster, tc.planned)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestC05_NoopReVerifiesWhatThePlanClaimedToHaveSeen(t *testing.T) {
	// Plan with everything already installed, then change the cluster before
	// execute. The old code ran every freshness check only `if len(plannedNames) >
	// 0`, so a pure no-op re-recorded a stale observation with whatever uid it
	// happened to read now.
	tc := runR15Execution(t, "ours") // the read-only pass
	if tc.recordPath == "" {
		t.Fatalf("the no-op pass produced no record; stdout:\n%s", tc.stdout)
	}
	// The same plan, but the release the plan saw is now gone from the cluster.
	execute := tc.execute
	t.Setenv("FAKE_NATS_RELEASE", "absent")
	var out bytes.Buffer
	err := RunComponentsExecute(context.Background(), execute, &out)
	if err == nil {
		t.Fatalf("a no-op whose observed release disappeared still succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "already_installed") || !strings.Contains(err.Error(), "re-plan") {
		t.Fatalf("the refusal must name the stale observation and ask for a re-plan, got %v", err)
	}
	// Refused before writing anything: the plan's own run must not gain a record.
	if path := recordPathFromStdout(t, out.String()); path != "" {
		t.Fatalf("a refused no-op still landed a record: %s", path)
	}
}

// ---------------------------------------------------------------------------
// C09 — the persisted outcome follows what the command actually returned.
// ---------------------------------------------------------------------------

func TestC09_TerminalResultFollowsTheReturnedError(t *testing.T) {
	// The exact bug: the body stamped `succeeded` before the success record was
	// written, the write failed, and the finalizer's guard skipped every
	// correction because Result already read "succeeded".
	stamped := InstallState{Result: ResultSucceeded, Phase: PhaseSucceeded}
	settleTerminalResult(&stamped, errors.New("write the install-success record: no space left on device"), nil, false)
	if stamped.Result == ResultSucceeded {
		t.Fatalf("a command that returned an error still persisted %q (C09)", stamped.Result)
	}
	if stamped.Result != ResultFailed {
		t.Fatalf("a failed run must persist %q, got %q", ResultFailed, stamped.Result)
	}

	ok := InstallState{Result: ResultRunning, Phase: PhaseClusterBuilt}
	settleTerminalResult(&ok, nil, nil, true)
	if ok.Result != ResultSucceeded {
		t.Fatalf("a run that really published success must persist succeeded, got %q", ok.Result)
	}

	abandoned := InstallState{Result: ResultRunning}
	settleTerminalResult(&abandoned, nil, nil, false)
	if abandoned.Result != ResultFailed {
		t.Fatalf("a run that stopped mid-flight cannot persist running, got %q", abandoned.Result)
	}

	cancelled := InstallState{Result: ResultRunning}
	settleTerminalResult(&cancelled, context.Canceled, context.Canceled, false)
	if cancelled.Result != ResultCancelled {
		t.Fatalf("a cancelled run must persist cancelled, got %q", cancelled.Result)
	}
}

func TestC09_SuccessIsOnlyAnnouncedAfterBothWritesLand(t *testing.T) {
	base := RunManifest{
		SchemaVersion: RunManifestSchemaVersion,
		ClusterName:   "ani-lab",
		ConfigDigest:  strings.Repeat("a", 64),
	}
	identity := ManifestIdentity{
		SiteConfigDigest:    strings.Repeat("a", 64),
		MaterialsLockDigest: strings.Repeat("b", 64),
		ClusterUID:          "uid-kube-system-audit",
		NodeCount:           3,
	}
	newState := func() InstallState {
		return InstallState{
			SchemaVersion: InstallStateSchemaVersion, RunID: "ani-ani-lab-20260927-000000",
			ClusterName: "ani-lab", Phase: PhaseClusterBuilt, Result: ResultRunning,
			ChangesStarted: true, ConfigDigest: strings.Repeat("a", 64),
		}
	}

	// Happy path: record and state both durable, and the live state moved to
	// succeeded only because both writes returned.
	dir := t.TempDir()
	statePath := filepath.Join(dir, "run-state.json")
	state := newState()
	record, err := publishInstallSuccess(&state, statePath, dir, base, identity)
	if err != nil {
		t.Fatalf("the durable success path must succeed: %v", err)
	}
	if state.Result != ResultSucceeded || state.Phase != PhaseSucceeded {
		t.Fatalf("the live state was not advanced by a published success: %+v", state)
	}
	if _, err := os.ReadFile(record); err != nil {
		t.Fatalf("the success record is missing: %v", err)
	}
	persisted, err := ReadRunState(statePath)
	if err != nil || persisted.Result != ResultSucceeded {
		t.Fatalf("the persisted state must say succeeded: %+v %v", persisted, err)
	}

	// Injection 1: the success record cannot be written (its directory is a
	// file). Nothing may read as success — in memory or on disk.
	blockedRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedStatePath := filepath.Join(t.TempDir(), "run-state.json")
	state = newState()
	if _, err := publishInstallSuccess(&state, blockedStatePath, blockedRoot, base, identity); err == nil {
		t.Fatal("a success record that cannot be written must fail the command")
	}
	if state.Result == ResultSucceeded || state.Phase == PhaseSucceeded {
		t.Fatalf("the live state claimed success although the record failed: %+v", state)
	}
	if _, err := os.Stat(blockedStatePath); !os.IsNotExist(err) {
		t.Fatal("a failed publish still wrote a terminal run-state")
	}

	// Injection 2: the record lands but the terminal state does not (the state
	// path is a directory). The command must fail, the live state must stay
	// un-succeeded, and the error must say what really happened so nobody
	// re-installs over a cluster that was built.
	recordDir := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state.json")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state = newState()
	landed, err := publishInstallSuccess(&state, stateDir, recordDir, base, identity)
	if err == nil {
		t.Fatal("a terminal run-state that cannot be written must fail the command")
	}
	if !strings.Contains(err.Error(), "install-success") && !strings.Contains(err.Error(), "run.json") {
		t.Fatalf("the error must name the record that did land, so the operator knows the install happened: %v", err)
	}
	if state.Result == ResultSucceeded {
		t.Fatalf("the live state claimed success although its persistence failed: %+v", state)
	}
	if landed != filepath.Join(recordDir, RunManifestFileName) {
		t.Fatalf("the published record path must still be reported even on failure, got %q", landed)
	}
	if _, err := os.ReadFile(landed); err != nil {
		t.Fatalf("the success record that really landed was lost: %v", err)
	}
}

// ---------------------------------------------------------------------------
// C06 — a no-op over a component the first install itself put in place carries
// the base's effective config digest, and that is legitimate.
// ---------------------------------------------------------------------------

func TestC06_SameConfigNoopGoesWriterToLoaderToSmoke(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", stateDir)
	// The base install itself selected nats, so asking for nats again cannot
	// change the effective config: this is the shape C06 is about.
	sameConfig := "  certManager: {enabled: true}\n  nats: {enabled: true}"
	tc := runR15ExecutionOverBase(t, sameConfig, sameConfig, "ours")
	if tc.recordPath == "" {
		t.Fatalf("C06: a no-op whose config did not move must still land a record; stdout:\n%s", tc.stdout)
	}
	e := tc.record.ComponentsExecution
	if e.Operation != ComponentsOperationNoop || e.DidInstall {
		t.Fatalf("the same-config pass must stay a read-only observation, got %q/%v", e.Operation, e.DidInstall)
	}
	// This is the whole point: the base's digest and this record's digest are the
	// same bytes, because nothing was added. The old guard refused exactly this.
	if e.BaseConfigDigest != tc.record.ConfigDigest {
		t.Fatalf("the fixture is no longer the same-config case this test exists for: base %s vs record %s",
			e.BaseConfigDigest, tc.record.ConfigDigest)
	}
	base, err := os.ReadFile(tc.baseRun)
	if err != nil {
		t.Fatalf("read the base record: %v", err)
	}
	var baseManifest RunManifest
	if err := json.Unmarshal(base, &baseManifest); err != nil {
		t.Fatalf("parse the base record: %v", err)
	}
	if baseManifest.ConfigDigest != tc.record.ConfigDigest {
		t.Fatalf("the record must carry the base's own digest, got %s vs %s", baseManifest.ConfigDigest, tc.record.ConfigDigest)
	}
	// The consumer accepts it, smoke runs, and nothing about the base moved.
	scripts := filepath.Join(t.TempDir(), "scripts")
	r13StubScript(t, scripts, "nats", "ANI-NATS-OK")
	t.Setenv("FAKE_SCRIPT_LOG", filepath.Join(t.TempDir(), "scripts.log"))
	out := filepath.Join(t.TempDir(), "verify")
	input := verifyInputFor(t, tc.recordPath, VerifyLevelSmoke, "nats", scripts, out)
	input.Kubeconfig = tc.execute.Kubeconfig
	if err := RunVerify(context.Background(), input, os.Stdout); err != nil {
		t.Fatalf("the loader must consume a same-config observation record: %v", err)
	}
	// It is still only an observation: no change authority, and no quota re-arm.
	acc := verifyInputFor(t, tc.recordPath, VerifyLevelAcceptance, "nats", scripts, filepath.Join(t.TempDir(), "acc"))
	acc.Kubeconfig = tc.execute.Kubeconfig
	acc.AllowPodRecreate = true
	if err := RunVerify(context.Background(), acc, io.Discard); err == nil {
		t.Fatal("a same-config no-op must not buy a recreation")
	}
	// The base record and its state file are byte-identical to before.
	for name, before := range tc.baseBefore {
		if after := sha256FileHex(filepath.Join(filepath.Dir(tc.baseRun), name)); after != before {
			t.Fatalf("the base %s changed while recording a no-op (%s -> %s)", name, before[:12], after[:12])
		}
	}
}
