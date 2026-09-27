/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// C08 (round 3) — the checker's release path, proved at the wire.
//
// `kk ani pod-release` is the only way a packaged checker may remove a probe it
// created, so what matters about it is not what it prints but what it sends: one
// DELETE for one named pod, carrying the uid this run holds from the create
// response as DeleteOptions.Preconditions — and nothing else. These tests drive
// RunPodRelease against the isolated endpoint from the R13 harness and read the
// requests that actually arrived.
//
// What is NOT claimed here: the endpoint emulates a documented contract. That a
// real API server enforces the precondition inside etcd, and that the recreated
// Fluent Bit/Loki workloads behave, are live facts this environment cannot
// produce and are recorded as untested, not as passing.
// ---------------------------------------------------------------------------

// c08Server is the isolated endpoint plus the state files the fake reads, so the
// object standing at a name is a fact on disk rather than an assumption in a test.
func c08Server(t *testing.T) (kubeconfig, stateDir string) {
	t.Helper()
	base := t.TempDir()
	stateDir = filepath.Join(base, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kubeconfig = r13AttachAPIServer(t, base, stateDir)
	return kubeconfig, stateDir
}

func c08SetPodUID(t *testing.T, stateDir, uid string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "pod-uid"), []byte(uid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func c08Release(t *testing.T, kubeconfig, namespace, pod, uid string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	err := RunPodRelease(context.Background(), PodReleaseInput{
		Kubeconfig: kubeconfig, Namespace: namespace, Pod: pod, UID: uid,
	}, out)
	return out.String(), err
}

// c08ReadState reads one of the isolated endpoint's record files.
func c08ReadState(t *testing.T, stateDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

func TestC08_PodReleaseSendsTheUIDAsAServerSidePrecondition(t *testing.T) {
	kubeconfig, stateDir := c08Server(t)
	const authorized = "uid-created-by-this-run"
	c08SetPodUID(t, stateDir, authorized)

	printed, err := c08Release(t, kubeconfig, "ani-observability", "ani-log-marker-run1", authorized)
	if err != nil {
		t.Fatalf("a delete the server accepted was reported as a failure: %v (%s)", err, printed)
	}
	if !strings.HasPrefix(printed, PodReleaseReleased+" ") {
		t.Fatalf("the release printed %q, want the %s token", printed, PodReleaseReleased)
	}
	requests := r13DeleteRequests(t, stateDir)
	if len(requests) != 1 {
		t.Fatalf("exactly one delete request must reach the server, got %v", requests)
	}
	if requests[0] != authorized {
		t.Fatalf("the delete carried precondition uid %q, not the uid the create returned %q", requests[0], authorized)
	}
	types := r13DeleteContentTypes(t, stateDir)
	if len(types) != 1 || types[0] != "application/json" {
		t.Fatalf("the precondition body must arrive as JSON the server can decode, got %v", types)
	}
}

func TestC08_AReplacementUnderTheSameNameIsRefusedAndNotRetried(t *testing.T) {
	kubeconfig, stateDir := c08Server(t)
	// The object standing at the name when the request is served is somebody
	// else's: this is the race the precondition exists for.
	c08SetPodUID(t, stateDir, "uid-SOMEONE-ELSE")

	printed, err := c08Release(t, kubeconfig, "ani-observability", "ani-fb-insp-node1", "uid-created-by-this-run")
	if err == nil {
		t.Fatalf("a conflicting object was released anyway: %s", printed)
	}
	if !strings.HasPrefix(printed, string(deleteConflict)+" ") {
		t.Fatalf("the answer must name the reason it refused, got %q", printed)
	}
	if !strings.Contains(err.Error(), "no longer the authorised object") {
		t.Fatalf("the error must say what a conflict means for the operator: %v", err)
	}
	// One request only. Reading a fresh uid and deleting again would be deleting
	// the replacement under the original object's authority.
	if requests := r13DeleteRequests(t, stateDir); len(requests) != 1 {
		t.Fatalf("the refused delete was retried: %v", requests)
	}
	// The endpoint records an answered-but-refused delete as "<name>:REFUSED" and
	// a real removal as the bare name.
	deleted := c08ReadState(t, stateDir, "deleted")
	if !strings.Contains(deleted, "ani-fb-insp-node1:REFUSED") {
		t.Fatalf("the conflict was not recorded as a refusal: %q", deleted)
	}
	if len(c08DeletedNames(deleted)) != 0 {
		t.Fatalf("the replacement object was deleted: %q", deleted)
	}
	// And the object is still there for the next reader to find.
	if uid := c08ReadState(t, stateDir, "pod-uid"); !strings.Contains(uid, "SOMEONE-ELSE") {
		t.Fatalf("a refused delete changed the object: %q", uid)
	}
}

// c08DeletedNames lists the pods the isolated endpoint actually removed, ignoring
// the refusals it also records.
func c08DeletedNames(log string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, ":") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func TestC08_AnAbsentPodIsReportedAsAbsentAndNeverAsDeleted(t *testing.T) {
	kubeconfig, stateDir := c08Server(t)
	// No pod-uid file at all: the endpoint answers 404.
	printed, err := c08Release(t, kubeconfig, "ani-observability", "ani-log-marker-gone", "uid-created-by-this-run")
	if err != nil {
		t.Fatalf("an object that is simply not there must end the obligation, not fail it: %v (%s)", err, printed)
	}
	if !strings.HasPrefix(printed, PodReleaseAbsent+" ") {
		t.Fatalf("the release printed %q; \"absent\" and \"released\" are different facts", printed)
	}
	if strings.HasPrefix(printed, PodReleaseReleased) {
		t.Fatal("this run claimed a deletion it did not perform")
	}
	if deleted := c08ReadState(t, stateDir, "deleted"); strings.Contains(deleted, "ani-log-marker-gone") {
		t.Fatalf("the endpoint recorded a deletion for a pod that was absent: %q", deleted)
	}
}

func TestC08_ForbiddenAndUnreachableAnswersKeepTheObligation(t *testing.T) {
	kubeconfig, stateDir := c08Server(t)
	c08SetPodUID(t, stateDir, "uid-created-by-this-run")
	t.Setenv("FAKE_DELETE_FORBIDDEN", "1")

	printed, err := c08Release(t, kubeconfig, "ani-observability", "ani-fb-client-run1", "uid-created-by-this-run")
	if err == nil {
		t.Fatalf("a forbidden delete was reported as success: %s", printed)
	}
	if !strings.HasPrefix(printed, string(deleteForbidden)+" ") {
		t.Fatalf("the answer must say the identity was refused, got %q", printed)
	}
	if !strings.Contains(err.Error(), "was not released") {
		t.Fatalf("the caller must be told the pod is still there: %v", err)
	}

	// An endpoint that is simply not there is not "absent" either: the object may
	// still exist and the outcome is unknown.
	broken := r13WriteKubeconfig(t, "http://127.0.0.1:1", filepath.Join(t.TempDir(), "kc"))
	printed, err = c08Release(t, broken, "ani-observability", "ani-fb-client-run1", "uid-created-by-this-run")
	if err == nil {
		t.Fatalf("an unreachable endpoint must not read as a released probe: %s", printed)
	}
	if strings.HasPrefix(printed, PodReleaseReleased) || strings.HasPrefix(printed, PodReleaseAbsent) {
		t.Fatalf("an unknown outcome was reported as a known one: %q", printed)
	}
}

func TestC08_PodReleaseRefusesToInventATargetOrACondition(t *testing.T) {
	kubeconfig, stateDir := c08Server(t)
	c08SetPodUID(t, stateDir, "uid-created-by-this-run")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"),
		[]byte("apiVersion: v1\nkind: Config\nclusters:\n- name: home\n  cluster:\n    server: http://127.0.0.1:1\ncontexts:\n- name: home\n  context:\n    cluster: home\n    user: home\ncurrent-context: home\nusers:\n- name: home\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", filepath.Join(home, ".kube", "config"))

	for _, tc := range []struct {
		name  string
		input PodReleaseInput
	}{
		{"no kubeconfig", PodReleaseInput{Namespace: "ns", Pod: "p", UID: "u"}},
		{"no namespace", PodReleaseInput{Kubeconfig: kubeconfig, Pod: "p", UID: "u"}},
		{"no pod", PodReleaseInput{Kubeconfig: kubeconfig, Namespace: "ns", UID: "u"}},
		// The case that matters: a delete the caller cannot condition on is a
		// delete of whatever stands at the name.
		{"no uid", PodReleaseInput{Kubeconfig: kubeconfig, Namespace: "ns", Pod: "p"}},
		{"missing kubeconfig file", PodReleaseInput{Kubeconfig: filepath.Join(home, "nope.conf"), Namespace: "ns", Pod: "p", UID: "u"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			if err := RunPodRelease(context.Background(), tc.input, out); err == nil {
				t.Fatalf("the request was accepted with no %s: %s", tc.name, out.String())
			}
			if out.Len() != 0 {
				t.Fatalf("a refused request still printed an outcome: %q", out.String())
			}
			if requests := r13DeleteRequests(t, stateDir); len(requests) != 0 {
				t.Fatalf("a request reached the endpoint before its inputs were complete: %v", requests)
			}
		})
	}
}

func TestC08_PodReleaseHonoursContextCancellationBeforeTouchingTheServer(t *testing.T) {
	kubeconfig, stateDir := c08Server(t)
	c08SetPodUID(t, stateDir, "uid-created-by-this-run")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := &bytes.Buffer{}
	err := RunPodRelease(ctx, PodReleaseInput{
		Kubeconfig: kubeconfig, Namespace: "ani-observability",
		Pod: "ani-log-marker-run1", UID: "uid-created-by-this-run",
	}, out)
	if err == nil {
		t.Fatalf("a finished context still produced a delete: %s", out.String())
	}
	if requests := r13DeleteRequests(t, stateDir); len(requests) != 0 {
		t.Fatalf("the cancelled run reached the server %v times", requests)
	}
}

// T-C08-2: the entry point is real CLI wiring, not only a Go function: every flag
// the packaged checker passes must be registered on the command it calls.
func TestC08_PodReleaseCommandIsWiredAndRejectsAnUnconditionedCall(t *testing.T) {
	script := c08CheckerReleaseInvocation(t)
	for _, flag := range []string{"--kubeconfig", "--namespace", "--pod", "--uid"} {
		if !strings.Contains(script, flag) {
			t.Fatalf("the packaged checker calls pod-release without %s, so the uid it holds is not the condition of the delete:\n%s", flag, script)
		}
	}
	if strings.Contains(script, "delete pod \"$name\"") || strings.Contains(script, "delete pod -l") {
		t.Fatalf("the checker still deletes by name or by label:\n%s", script)
	}
	// The command side of the same contract, read from the file that builds it:
	// a flag the script passes that the command never registered would fail at
	// run time, long after the probe was created.
	commandSource, err := os.ReadFile(filepath.Join("..", "..", "cmd", "kk", "app", "builtin", "ani.go"))
	if err != nil {
		t.Fatal(err)
	}
	block := c08Between(string(commandSource), "func newANIPodReleaseCommand()", "\n}\n")
	for _, flag := range []string{"kubeconfig", "namespace", "pod", "uid"} {
		if !strings.Contains(block, "\""+flag+"\"") {
			t.Fatalf("kk ani pod-release never registers --%s that the checker passes:\n%s", flag, block)
		}
	}
	if !strings.Contains(block, "MarkFlagRequired(\"uid\")") {
		t.Fatal("the uid is the precondition; the command must require it rather than accept an unconditioned delete")
	}
	if !strings.Contains(block, "ani.RunPodRelease") {
		t.Fatalf("the command does not reach the conditional delete it claims to expose:\n%s", block)
	}
}

// c08Between returns the text between a start marker and the next end marker.
func c08Between(text, start, end string) string {
	from := strings.Index(text, start)
	if from < 0 {
		return ""
	}
	rest := text[from:]
	to := strings.Index(rest[len(start):], end)
	if to < 0 {
		return rest
	}
	return rest[:len(start)+to+len(end)]
}

// c08CheckerReleaseInvocation returns the checker's own release helper, read from
// the packaged template.
func c08CheckerReleaseInvocation(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "builtin", "core", "roles", "ani", "fluent-bit", "templates", "verify.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "conditional_release() {")
	if start < 0 {
		t.Fatal("the packaged checker defines no conditional_release helper; the probe cleanup path is not the one under test")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("conditional_release is not terminated")
	}
	return text[start : start+end+2]
}

// T-C08-3: the run must be able to name its own executable, because that is what
// the checker calls. An empty value is a refusal, not a fallback.
func TestC08_SelfExecutableIsAbsoluteOrEmpty(t *testing.T) {
	path := selfExecutable()
	if path == "" {
		t.Fatal("a test binary that cannot name itself would silently disable every probe release")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("the checker resolves ANI_KK_BIN without a shell PATH search, so it must be absolute: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the reported executable is not there: %v", err)
	}
	// The scope carries it, and a run that lost it must still be able to say so.
	scope := InstallRunScope("ani-lab")
	if scope.KKBinary != "" {
		t.Fatal("the generator must not invent a binary path it never resolved")
	}
	scope.KKBinary = path
	spec := map[string]any{"ani": map[string]any{"run": InstallRunScope("ani-lab").SpecMap()}}
	if err := scope.Replace(spec); err != nil {
		t.Fatal(err)
	}
	run := spec["ani"].(map[string]any)[specKey].(map[string]any)
	if got, _ := run["kk_bin"].(string); got != path {
		t.Fatalf("the run's own binary did not survive into the spec the roles render: %v", got)
	}
}
