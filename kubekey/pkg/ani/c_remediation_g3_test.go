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
# c08_create mints the object the way an API server would: a name that is already
# taken is refused, and the uid it assigns is the only uid the creator may claim.
c08_create() { # c08_create <name> <attempt-label-value>
  local key; key="$(podfile "$1")"
  if [ -e "$state/pod-$key.uid" ]; then echo "Error from server (AlreadyExists): pods \"$1\" already exists" >&2; return 1; fi
  printf 'uid-created-%s-%s\n' "$1" "$RANDOM$RANDOM" > "$state/pod-$key.uid"
  [ -n "$2" ] && printf '%s' "$2" > "$state/attempt-$key"
  return 0
}
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
  cat "$state/pod-$(podfile "$nm").uid" 2>/dev/null
  exit 0
fi
if [[ "$args" == *" run "* ]]; then
  nm="$(printf '%s' "$args" | awk '{for(i=1;i<=NF;i++) if($i=="run"){print $(i+1); exit}}')"
  attempt="$(printf '%s' "$args" | sed -n 's/.*--labels ani-attempt=\([^ ]*\).*/\1/p')"
  c08_create "$nm" "$attempt" || exit 1
  case "$args" in *"jsonpath={.metadata.uid}"*) printf '%s\n' "$(cat "$state/pod-$(podfile "$nm").uid")" ;; *) printf 'pod/%s created\n' "$nm" ;; esac
  exit 0
fi
# A create is how the script makes its marker, client and inspector pods: the
# object's name and its attempt label come from the manifest, and the uid the
# server assigns is printed back, because that is the only uid the attempt may
# legitimately claim as proof it created the object. A name already taken is
# refused, and NOTHING is adopted from a failed create.
if [[ "$args" == *" create -f "* ]]; then
  file="$(printf '%s' "$args" | awk '{for(i=1;i<=NF;i++) if($i=="create"){print $(i+2); exit}}')"
  manifest="$(cat "$file" 2>/dev/null)"
  nm="$(printf '%s\n' "$manifest" | sed -n 's/^  name: *//p' | head -1)"
  attempt="$(printf '%s\n' "$manifest" | sed -n 's/.*ani-attempt: *//p' | head -1 | tr -d '"')"
  [ -n "$nm" ] || { echo "fake kubectl: create with no metadata.name (file=$file)" >&2; exit 1; }
  c08_create "$nm" "$attempt" || exit 1
  case "$args" in *"jsonpath={.metadata.uid}"*) printf '%s\n' "$(cat "$state/pod-$(podfile "$nm").uid")" ;; *) printf 'pod/%s created\n' "$nm" ;; esac
  exit 0
fi
# apply is not a create: it would update whatever already owns the name and let
# this run adopt a pod it never created. Refused so a regression fails here.
if [[ "$args" == *"apply -f "* ]]; then
  echo "fake kubectl: refusing apply -f; a probe must be created, not adopted" >&2
  printf 'APPLY %s\n' "$args" >> "$state/creates"
  exit 1
fi
# A label delete selects a SET rather than the object a run created. It is kept
# out of the probe path entirely, so reaching for it is itself the finding.
if [[ "$args" == *"delete pod -l "* ]]; then
  printf 'LABEL-DELETE %s\n' "$args" >> "$state/name_deletes"
  echo "fake kubectl: no cleanup path deletes by label; the attempt's own ledger is the only set it owns" >&2
  exit 1
fi
if [[ "$args" == *"delete pod"* ]]; then
  # The whole point of C08: a probe is never removed by name. Any request that
  # arrives here is recorded and refused, so a regression cannot pass quietly.
  printf 'NAME-DELETE %s\n' "$args" >> "$state/name_deletes"
  echo "fake kubectl: refusing delete-by-name; release a probe through kk ani pod-release with the uid its create returned" >&2
  exit 1
fi
exit 1
`

// c08FakeKK is the stand-in for `kk ani pod-release`. It is NOT a second
// implementation of the delete: the real precondition logic is proved on the wire
// in c_remediation_g5_test.go. What this models is the server's ANSWER, so the
// packaged checker can be shown to branch on the token — and to keep the object
// and its ownership record whenever the answer is anything but a deletion it
// authorised.
const c08FakeKK = `#!/usr/bin/env bash
state="__STATE__"
podfile() { printf '%s' "$1" | tr -c 'a-zA-Z0-9._-' '_' ; }
[ "$1" = "ani" ] && [ "$2" = "pod-release" ] || { echo "fake kk: unexpected argv: $*" >&2; exit 2; }
ns=""; pod=""; uid=""; kc=""
while [ $# -gt 0 ]; do
  case "$1" in
    --namespace) ns="$2"; shift 2 ;;
    --pod) pod="$2"; shift 2 ;;
    --uid) uid="$2"; shift 2 ;;
    --kubeconfig) kc="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf 'release ns=%s pod=%s uid=%s kc=%s\n' "$ns" "$pod" "$uid" "$kc" >> "$state/releases"
[ -n "$uid" ] || { echo "refused: no uid to condition on" >&2; exit 2; }
key="pod-$(podfile "$pod").uid"
if [ ! -f "$state/$key" ]; then printf 'not_found %s %s %s\n' "$ns" "$pod" "$uid"; exit 0; fi
have="$(cat "$state/$key")"
if [ "$have" != "$uid" ]; then printf 'conflict %s %s %s\n' "$ns" "$pod" "$uid"; exit 1; fi
case "${FAKE_RELEASE_RESULT:-}" in
  forbidden) printf 'forbidden %s %s %s\n' "$ns" "$pod" "$uid"; exit 1 ;;
  timeout)   printf 'timeout %s %s %s\n' "$ns" "$pod" "$uid"; exit 1 ;;
  garbage)   echo "the quick brown fox"; exit 0 ;;
esac
printf 'deleted %s %s %s\n' "$ns" "$pod" "$uid"
# FAKE_DELETE_KEEPS_OBJECT answers success while leaving the object standing: the
# case a swallowed cleanup failure used to hide.
[ -n "${FAKE_DELETE_KEEPS_OBJECT:-}" ] || rm -f "$state/$key" "$state/attempt-$(podfile "$pod")"
printf 'released %s %s\n' "$pod" "$uid" >> "$state/deletes"
exit 0
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
	fake := strings.ReplaceAll(c08FakeKubectl, "__STATE__", state)
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	kk := strings.ReplaceAll(c08FakeKK, "__STATE__", state)
	if err := os.WriteFile(filepath.Join(bin, "kk"), []byte(kk), 0o700); err != nil {
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
		"ANI_KK_BIN="+filepath.Join(home, "bin", "kk"),
		"ANI_VERIFY_LEVEL=smoke",
	)
	return out
}

// c08EscapedName mirrors the fake's podfile(): a pod name is turned into a file
// name by replacing everything outside [A-Za-z0-9._-] with an underscore.
func c08EscapedName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
}

func TestC08_OverlappingRunsNeverDeleteEachOthersProbes(t *testing.T) {
	a, _, home := c07DummyKubeconfigs(t)
	state := c08FakeCluster(t, home)

	// Objects an earlier attempt left behind, under the names the old script
	// used. A run that "cleans up first" deletes these; a run that only owns what
	// it created must not touch them.
	legacy := []string{"ani-fluent-bit-verify-client", "ani-log-marker-1"}
	for _, name := range legacy {
		// Escaped the way the fake escapes names, and with no attempt label:
		// these are the previous run's probes, so a label-scoped delete must not
		// be able to reach them.
		if err := os.WriteFile(filepath.Join(state, "pod-"+c08EscapedName(name)+".uid"), []byte("uid-legacy\n"), 0o600); err != nil {
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
	// What this test can and cannot reach, stated rather than glossed: the full
	// checker stops at the backend query, which needs a live Loki, so the probes it
	// creates here are the client pods only. The marker, inspector and post-rebuild
	// paths — and the label-scoped cleanup over all of them — are covered by
	// TestC08_OwnershipIsProofFromTheCreateNotFromALaterRead, which drives the real
	// helpers. Claiming more from this run than it did would be the same kind of
	// overstatement C08 is about.
	if !strings.Contains(string(calls), " run ") {
		t.Fatalf("neither run reached the client probe:\n%s", transcript)
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
	// No cleanup is expected here: the runs fail before reaching it, and a run that
	// failed must keep its probes. Anything deleted at all would therefore be a bug.
	if len(deletes) != 0 {
		t.Fatalf("a run that never reached its cleanup still deleted something: %v", deletes)
	}
	createdByName := map[string]bool{}
	for _, name := range creates {
		createdByName[name] = true
	}
	for _, name := range deletes {
		for _, old := range legacy {
			if name == old {
				t.Fatalf("a run deleted a leftover probe it did not create: %v", deletes)
			}
		}
		if !createdByName[name] {
			t.Fatalf("a run deleted %q, which it never created; creates were %v", name, creates)
		}
	}
	for _, name := range legacy {
		if _, err := os.Stat(filepath.Join(state, "pod-"+c08EscapedName(name)+".uid")); err != nil {
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

// c08SourceChecker loads the REAL packaged checker through its own lib-only seam
// and returns a runner that executes a snippet in that loaded context. The
// ownership helpers under test are therefore the shipped ones, not a copy.
func c08SourceChecker(t *testing.T, home, pin string) func(t *testing.T, script string) string {
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
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib.sh")
	if err := os.WriteFile(lib, []byte(rendered.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return func(t *testing.T, script string) string {
		t.Helper()
		runner := filepath.Join(dir, "runner-"+strings.ReplaceAll(t.Name(), "/", "_")+".sh")
		// The trailer reports the ledger and the failure list rather than deciding
		// the case: a snippet that legitimately ends in a failed cleanup must still
		// be able to say what it recorded.
		body := "set -uo pipefail\nsource \"" + lib + "\"\n" +
			"set +e\n" + script + "\nset -e\n" +
			// The keys of an associative array are printed via a count plus the
			// on-disk ledger: the ledger is the artefact that survives the function
			// boundary, and that is the property this test cares about.
			"printf 'OWNED_COUNT=%s\\n' \"${#OWNED[@]}\"\n" +
			"printf 'LEDGER=%s\\n' \"$(tr '\\n' ' ' < \"$EVIDENCE/owned-pods.txt\" 2>/dev/null)\"\n" +
			"printf 'RECORD=%s\\n' \"${OWNED[*]:-none}\"\n" +
			"printf 'FAILURES=%s\\n' \"${CLEANUP_FAILURES[*]:-none}\"\n" +
			"printf 'EVIDENCE=%s\\n' \"$EVIDENCE\"\n"
		if err := os.WriteFile(runner, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		out, _ := execBash(t, runner,
			"PATH="+filepath.Join(home, "bin")+":"+os.Getenv("PATH"),
			"HOME="+filepath.Join(home, "home"),
			"ANI_VERIFY_KUBECONFIG="+pin,
			"KUBECONFIG="+pin,
			"ANI_VERIFY_OUTPUT_DIR="+filepath.Join(home, "out"),
			"ANI_KK_BIN="+filepath.Join(home, "bin", "kk"),
			// The packaged checker defines its helpers and returns under this
			// variable, so the functions below are the ones that ship.
			"ANI_VERIFY_LIB_ONLY=1",
			"FAKE_STATE_DIR="+stateDirOf(t, home),
		)
		return out
	}
}

// stateDirOf is where the fake cluster keeps its pod registry.
func stateDirOf(t *testing.T, home string) string {
	t.Helper()
	return filepath.Join(home, "state")
}

// c08Escape mirrors the fake's podfile(): pod name -> the file it is stored under.
func c08Escape(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
}

// c08ProbeManifest is a probe pod carrying this attempt's own label, which is what
// a label-scoped cleanup is allowed to find.
func c08ProbeManifest(name string) string {
	return "apiVersion: v1\nkind: Pod\nmetadata:\n  name: " + name +
		"\n  labels:\n    ani-attempt: \"" + "%RUN_ID%" + "\"\n" +
		"spec:\n  restartPolicy: Never\n  containers:\n    - name: marker\n      image: busybox\n"
}

func TestC08_OwnershipIsProofFromTheCreateNotFromALaterRead(t *testing.T) {
	a, _, home := c07DummyKubeconfigs(t)
	state := c08FakeCluster(t, home)
	run := c08SourceChecker(t, home, a)

	// A previous attempt's probe in the same registry, carrying no attempt label:
	// nothing below is allowed to reach it.
	legacy := "ani-log-marker-legacy"
	if err := os.WriteFile(filepath.Join(state, "pod-"+c08Escape(legacy)+".uid"), []byte("uid-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a create whose response has no uid claims nothing", func(t *testing.T) {
		out := run(t, `own_pod ani-fb-client-fake "" || true`)
		if strings.Contains(out, "OWNED=ani-fb-client-fake") {
			t.Fatalf("an object with no uid from its create was recorded as owned: %s", out)
		}
		if !strings.Contains(out, "FAILURES=ani-fb-client-fake") || !strings.Contains(out, "no uid") {
			t.Fatalf("the missing uid must be reported, not tolerated: %s", out)
		}
	})

	t.Run("cleanup removes exactly what this attempt created", func(t *testing.T) {
		out := run(t, `
mkdir -p "$EVIDENCE"
cat > "$EVIDENCE/ani-log-marker-owned.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: ani-log-marker-owned
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
create_pod ani-log-marker-owned "$EVIDENCE/ani-log-marker-owned.yaml"
release_all_owned`)
		// The uid must be the one the create returned, and it must be on disk:
		// a ledger only held in a shell variable would be lost the moment the
		// function ran in a subshell, which is precisely how the first version of
		// this helper recorded nothing at all.
		if !strings.Contains(out, "LEDGER=ani-log-marker-owned uid-created-ani-log-marker-owned") {
			t.Fatalf("the ownership ledger did not record the uid from the create response: %s", out)
		}
		if !strings.Contains(out, "OWNED_COUNT=0") {
			t.Fatalf("the owned probe was still listed after cleanup: %s", out)
		}
		if !strings.Contains(out, "FAILURES=none") {
			t.Fatalf("a clean cleanup reported a failure: %s", out)
		}
		deletes := c08ReadFile(t, filepath.Join(state, "deletes"))
		if !strings.Contains(deletes, "ani-log-marker-owned") {
			t.Fatalf("the owned probe was not removed: %q", deletes)
		}
		if strings.Contains(deletes, legacy) {
			t.Fatalf("a label-scoped delete reached a pod this attempt did not create: %q", deletes)
		}
		if _, err := os.Stat(filepath.Join(state, "pod-"+c08Escape(legacy)+".uid")); err != nil {
			t.Fatal("the leftover probe from an earlier attempt was destroyed")
		}
	})

	t.Run("a replacement under the same name is kept and its ownership retained", func(t *testing.T) {
		// C08: there is no client-side read that could notice the swap, and none is
		// wanted — the server refuses the delete because the uid in the request is
		// not the object's. What the run must do with that answer is keep the
		// object, keep its own claim on it, and fail.
		out := run(t, `
mkdir -p "$EVIDENCE"
cat > "$EVIDENCE/ani-fb-insp-swapped.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: ani-fb-insp-swapped
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
create_pod ani-fb-insp-swapped "$EVIDENCE/ani-fb-insp-swapped.yaml"
printf 'uid-SOMEONE-ELSE\n' > "$FAKE_STATE_DIR/pod-ani-fb-insp-swapped.uid"
release_pod ani-fb-insp-swapped || true`)
		if !strings.Contains(out, "kept") || !strings.Contains(out, "conflict") {
			t.Fatalf("the refusal must say the server refused on the precondition: %s", out)
		}
		if !strings.Contains(out, "OWNED_COUNT=1") {
			t.Fatalf("a refused release must not drop the ownership record: %s", out)
		}
		if strings.Contains(c08ReadFile(t, filepath.Join(state, "deletes")), "ani-fb-insp-swapped uid-SOMEONE-ELSE") {
			t.Fatal("the replacement pod was deleted")
		}
		if _, err := os.Stat(filepath.Join(state, "pod-ani-fb-insp-swapped.uid")); err != nil {
			t.Fatal("the object the run could not release was removed anyway")
		}
		// And the uid that travelled is the one this run created with, never the
		// one standing there now.
		releases := c08ReadFile(t, filepath.Join(state, "releases"))
		if !strings.Contains(releases, "uid=uid-created-ani-fb-insp-swapped") {
			t.Fatalf("the release did not carry the uid from the create response: %s", releases)
		}
		if strings.Contains(releases, "uid-SOMEONE-ELSE") {
			t.Fatalf("the run re-read the replacement's uid and deleted under it: %s", releases)
		}
	})

	t.Run("a cleanup failure fails the run instead of vanishing", func(t *testing.T) {
		out := run(t, `
own_pod ani-fb-client-nothing "" || true
report_cleanup || echo REPORT_CLEANUP_RETURNED_NONZERO`)
		if !strings.Contains(out, "REPORT_CLEANUP_RETURNED_NONZERO") {
			t.Fatalf("report_cleanup did not fail a run holding a cleanup failure: %s", out)
		}
		if !strings.Contains(out, "did not complete cleanly") {
			t.Fatalf("the failure must be stated, not only exited on: %s", out)
		}
	})

	t.Run("a release the server refused keeps everything and is reported", func(t *testing.T) {
		for _, answer := range []struct{ knob, expect string }{
			{"forbidden", "forbidden"},
			{"timeout", "timeout"},
			// An answer that is not one of the tokens at all must not be read as a
			// success because the exit code happened to be zero.
			{"garbage", "the quick brown fox"},
		} {
			// One name per answer: the previous answer's refusal deliberately left
			// its pod standing, and reusing the name would test the create guard
			// again instead of this one.
			pod := "ani-fb-insp-" + answer.knob
			out := run(t, `
mkdir -p "$EVIDENCE"
cat > "$EVIDENCE/`+pod+`.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: `+pod+`
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
export FAKE_RELEASE_RESULT=`+answer.knob+`
create_pod `+pod+` "$EVIDENCE/`+pod+`.yaml"
release_pod `+pod+` || true`)
			if !strings.Contains(out, answer.expect) {
				t.Fatalf("the %s answer was not passed through: %s", answer.knob, out)
			}
			if strings.Contains(out, "FAILURES=none") {
				t.Fatalf("a %s release was recorded as a clean cleanup: %s", answer.knob, out)
			}
			if !strings.Contains(out, "OWNED_COUNT=1") {
				t.Fatalf("a %s release dropped the ownership record: %s", answer.knob, out)
			}
			if _, err := os.Stat(filepath.Join(state, "pod-"+pod+".uid")); err != nil {
				t.Fatalf("the %s case removed the object anyway", answer.knob)
			}
		}
	})

	t.Run("an absent probe is reported as absent and not as deleted", func(t *testing.T) {
		out := run(t, `
mkdir -p "$EVIDENCE"
cat > "$EVIDENCE/ani-fb-insp-gone.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: ani-fb-insp-gone
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
create_pod ani-fb-insp-gone "$EVIDENCE/ani-fb-insp-gone.yaml"
rm -f "$FAKE_STATE_DIR/pod-ani-fb-insp-gone.uid"
release_pod ani-fb-insp-gone || true`)
		if !strings.Contains(out, "already gone") {
			t.Fatalf("an absent object must be named as absent: %s", out)
		}
		if strings.Contains(out, "released ani-fb-insp-gone") {
			t.Fatalf("the run claimed a deletion it did not perform: %s", out)
		}
		if !strings.Contains(out, "OWNED_COUNT=0") || !strings.Contains(out, "FAILURES=none") {
			t.Fatalf("an absent object still held up the cleanup: %s", out)
		}
	})

	t.Run("no conditional entry point means no delete at all", func(t *testing.T) {
		// The rule the whole of C08 rests on: without a precondition the run has no
		// authority over the object at a name, so it keeps the probe and says so
		// instead of deleting by name.
		out := run(t, `
mkdir -p "$EVIDENCE"
cat > "$EVIDENCE/ani-fb-insp-blind.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: ani-fb-insp-blind
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
create_pod ani-fb-insp-blind "$EVIDENCE/ani-fb-insp-blind.yaml"
ANI_KK_BIN= release_pod ani-fb-insp-blind || true
report_cleanup || echo REPORT_CLEANUP_RETURNED_NONZERO`)
		if !strings.Contains(out, "ANI_KK_BIN is empty") {
			t.Fatalf("a run with no conditional delete entry point must say so: %s", out)
		}
		if !strings.Contains(out, "OWNED_COUNT=1") {
			t.Fatalf("the probe was dropped from the ledger even though nothing released it: %s", out)
		}
		if !strings.Contains(out, "REPORT_CLEANUP_RETURNED_NONZERO") {
			t.Fatalf("an unreleased probe must fail the run: %s", out)
		}
		if c08ReadFile(t, filepath.Join(state, "name_deletes")) != "" {
			t.Fatalf("a run with no entry point still issued a delete-by-name: %s", c08ReadFile(t, filepath.Join(state, "name_deletes")))
		}
	})

	t.Run("a probe is created only, never adopted", func(t *testing.T) {
		// A name this run did not create is somebody else's object. create_pod must
		// fail on it and record nothing, so no later release can call it ours.
		out := run(t, `
mkdir -p "$EVIDENCE"
printf 'uid-PREEXISTING\n' > "$FAKE_STATE_DIR/pod-ani-fb-insp-taken.uid"
cat > "$EVIDENCE/ani-fb-insp-taken.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: ani-fb-insp-taken
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
create_pod ani-fb-insp-taken "$EVIDENCE/ani-fb-insp-taken.yaml" || true
release_all_owned || true`)
		if !strings.Contains(out, "create-only request failed") {
			t.Fatalf("an existing object was accepted as this run's create: %s", out)
		}
		if strings.Contains(out, "LEDGER=ani-fb-insp-taken") {
			t.Fatalf("the run recorded ownership of a pod it did not create: %s", out)
		}
		if uid := c08ReadFile(t, filepath.Join(state, "pod-ani-fb-insp-taken.uid")); !strings.Contains(uid, "PREEXISTING") {
			t.Fatalf("the pre-existing object was modified or replaced: %q", uid)
		}
		if strings.Contains(c08ReadFile(t, filepath.Join(state, "deletes")), "ani-fb-insp-taken") {
			t.Fatal("the run deleted an object it never created")
		}
	})

	t.Run("an accepted release claims only that the delete was accepted", func(t *testing.T) {
		// FAKE_DELETE_KEEPS_OBJECT answers `deleted` while the object is still
		// terminating. The checker must not then read the pod again and re-delete
		// under a fresh uid — that is the exact behaviour C08 removes — so what it
		// may say is what it was told: this run's uid was released.
		out := run(t, `
mkdir -p "$EVIDENCE"
cat > "$EVIDENCE/ani-fb-insp-stubborn.yaml" <<MANIFEST
apiVersion: v1
kind: Pod
metadata:
  name: ani-fb-insp-stubborn
  labels:
    ani-attempt: "$RUN_ID"
spec:
  restartPolicy: Never
  containers:
    - name: marker
      image: busybox
MANIFEST
create_pod ani-fb-insp-stubborn "$EVIDENCE/ani-fb-insp-stubborn.yaml"
export FAKE_DELETE_KEEPS_OBJECT=1
release_pod ani-fb-insp-stubborn || true`)
		if !strings.Contains(out, "released ani-fb-insp-stubborn") {
			t.Fatalf("an accepted release was not reported as accepted: %s", out)
		}
		if !strings.Contains(out, "FAILURES=none") {
			t.Fatalf("an accepted release must not become a cleanup failure: %s", out)
		}
		if !strings.Contains(out, "OWNED_COUNT=0") {
			t.Fatalf("the run kept a claim on an object its own delete was accepted for: %s", out)
		}
		// And it must not have gone back for a second look: one release request
		// per probe, with the uid this run created the object with.
		calls := c08ReadFile(t, filepath.Join(state, "releases"))
		if got := c08CountLines(calls, "pod=ani-fb-insp-stubborn "); got != 1 {
			t.Fatalf("the same probe was released %d times, want exactly 1: %s", got, calls)
		}
		if deletes := c08ReadFile(t, filepath.Join(state, "name_deletes")); deletes != "" {
			t.Fatalf("the checker fell back to a delete-by-name: %s", deletes)
		}
	})
}

// c08CountLines counts the recorded requests that contain a marker.
func c08CountLines(log, marker string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, marker) {
			n++
		}
	}
	return n
}

func c08ReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
