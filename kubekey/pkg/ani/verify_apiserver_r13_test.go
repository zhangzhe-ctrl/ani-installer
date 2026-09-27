/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// The isolated API endpoint the acceptance tests delete through.
//
// This replaces the fake kubectl's habit of interpreting `metadata.uid` itself with
// sed and then deciding whether to honour it — which asserted the shape of a shell
// argument and called that API semantics. Here the real client-go, built from the
// kubeconfig the run was given, sends a real DELETE to a real socket, and this
// server answers the way the API documents Preconditions: a uid that does not match
// the stored object is a 409 and the object stays.
//
// It is still a fake: it emulates a documented HTTP contract so the client side can
// be proved. Whether a real API server enforces that contract inside etcd is a
// separate question that needs a real apiserver, and is not claimed here.
//
// State is shared with the fake kubectl through the same state directory, because
// the acceptance target reads identities through one client and deletes through the
// other — and the test must not be able to pass by letting them disagree.
// ---------------------------------------------------------------------------

// r13AttachAPIServer starts the endpoint, points a fresh kubeconfig at it and
// returns that path. Both the pod uid the reads report and the uid the delete is
// authorised against come from the same files the fake kubectl already uses.
func r13AttachAPIServer(t *testing.T, baseDir, stateDir string) string {
	t.Helper()
	fake := &r13APIServer{stateDir: stateDir, server: nil}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)

	t.Setenv("ANI_R13_API_SERVER", fake.server.URL)
	return r13WriteKubeconfig(t, fake.server.URL, filepath.Join(baseDir, "kubeconfig"))
}

// r13WriteKubeconfig writes a kubeconfig naming the isolated endpoint, so any test
// that must build an API client can do it without inventing a second target.
func r13WriteKubeconfig(t *testing.T, serverURL, path string) string {
	t.Helper()
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: r13-isolated
  cluster:
    server: %s
contexts:
- name: r13-isolated
  context:
    cluster: r13-isolated
    user: r13-isolated
current-context: r13-isolated
users:
- name: r13-isolated
  user:
    token: r13-test-token
`, serverURL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the isolated kubeconfig: %v", err)
	}
	return path
}

type r13APIServer struct {
	stateDir string
	server   *httptest.Server
	mu       sync.Mutex
}

func (a *r13APIServer) podUID(name string) string {
	data, err := os.ReadFile(filepath.Join(a.stateDir, "pod-uid"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (a *r13APIServer) setPodUID(uid string) {
	if err := os.WriteFile(filepath.Join(a.stateDir, "pod-uid"), []byte(uid+"\n"), 0o600); err != nil {
		panic(err)
	}
}

func (a *r13APIServer) appendTo(file, line string) {
	f, err := os.OpenFile(filepath.Join(a.stateDir, file), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, line)
}

func (a *r13APIServer) serve(w http.ResponseWriter, r *http.Request) {
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	name := segments[len(segments)-1]
	a.mu.Lock()
	defer a.mu.Unlock()

	if r.Method != http.MethodDelete {
		a.status(w, http.StatusMethodNotAllowed, "only deletes are served", name)
		return
	}

	// Model the race the reviewer named, at the moment the request is served rather
	// than at the moment the client last looked. FAKE_REPLACE_AFTER_LAST_GET still
	// means "the object at this name is now somebody else's".
	if os.Getenv("FAKE_REPLACE_AFTER_LAST_GET") != "" {
		a.setPodUID("uid-INTRUDER-replaced")
	}
	current := a.podUID(name)

	var options struct {
		Preconditions *struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}
	raw := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for {
		n, err := r.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}
	_ = json.Unmarshal(raw, &options)

	// What the request carried, recorded from the decoded body. The old fake read a
	// uid out of an argv string; this reads it out of the DeleteOptions the server
	// would act on.
	uid := ""
	if options.Preconditions != nil {
		uid = options.Preconditions.UID
	}
	a.appendTo("delete-uid-preconditions", uid)
	a.appendTo("delete-content-types", r.Header.Get("Content-Type"))

	if current == "" {
		a.status(w, http.StatusNotFound, "no such pod", name)
		return
	}
	if uid == "" {
		// A delete with no precondition must never reach this point: the client
		// refuses to build one. Recorded so a regression here is visible rather than
		// silently honoured.
		a.appendTo("deleted", name+":NO-PRECONDITION")
		a.status(w, http.StatusBadRequest, "the delete carried no uid precondition", name)
		return
	}
	if uid != current {
		a.appendTo("deleted", name+":REFUSED")
		a.status(w, http.StatusConflict, "the pod at this name is not the authorised object", name)
		return
	}
	if os.Getenv("FAKE_DELETE_FORBIDDEN") != "" {
		a.appendTo("deleted", name+":FORBIDDEN")
		a.status(w, http.StatusForbidden, "not allowed", name)
		return
	}
	if os.Getenv("FAKE_STICKY_POD_UID") == "" {
		// The controller recreates it: the name keeps standing with a new uid, which
		// is what the acceptance waiter then looks for.
		a.setPodUID(fmt.Sprintf("uid-new-%d", len(a.deletionCount())+1))
	}
	// The state the fake kubectl used to mutate on a delete moves with the delete.
	// A PVC replaced underneath the recreation must still fail the check, and only
	// the object that actually served the delete can say that happened.
	if replacement := os.Getenv("FAKE_NEW_PVC_UID"); replacement != "" {
		if err := os.WriteFile(filepath.Join(a.stateDir, "pvc-uid"), []byte(replacement+"\n"), 0o600); err != nil {
			panic(err)
		}
	}
	a.appendTo("deleted", name)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success","code":200}`))
}

func (a *r13APIServer) deletionCount() []string {
	data, err := os.ReadFile(filepath.Join(a.stateDir, "deleted"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}

func (a *r13APIServer) status(w http.ResponseWriter, code int, message, name string) {
	status := map[string]any{
		"kind":       "Status",
		"apiVersion": "v1",
		"metadata":   map[string]any{},
		"status":     "Failure",
		"message":    fmt.Sprintf("%s (%s)", message, name),
		"reason":     reasonForCode(code),
		"code":       code,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

func reasonForCode(code int) string {
	switch code {
	case http.StatusNotFound:
		return "NotFound"
	case http.StatusConflict:
		// Must be "Conflict" and not "AlreadyExists": apimachinery's IsConflict
		// accepts the Conflict reason outright, and falls back to the 409 code only
		// for a reason it does not already know — so an AlreadyExists 409 would be
		// classified as a generic failure by the very helper this code branches on.
		return "Conflict"
	case http.StatusForbidden:
		return "Forbidden"
	case http.StatusBadRequest:
		return "BadRequest"
	default:
		return "Failure"
	}
}

// r15Kubeconfig writes the fixture kubeconfig the components planner/executor
// carry, and returns its path. C03 made this necessary: that value is also what
// `ani verify` builds its API client from, so it must be a file that resolves to a
// cluster rather than a placeholder name. It points at the isolated endpoint when
// one is running; the components fixtures never delete anything, and if one ever
// did the fake would answer instead of some real cluster.
func r15Kubeconfig(t *testing.T, baseDir string) string {
	t.Helper()
	path := filepath.Join(baseDir, "kubeconfig")
	server := os.Getenv("ANI_R13_API_SERVER")
	if server == "" {
		// Nothing listens here on purpose: a test that tried to reach a cluster
		// would fail loudly rather than silently find one through $HOME.
		server = "https://127.0.0.1:1"
	}
	return r13WriteKubeconfig(t, server, path)
}

// r13SuccessfulDeletions counts the deletes the endpoint actually honoured. A
// line naming just the pod is a removal; a line with a suffix (REFUSED,
// FORBIDDEN, NO-PRECONDITION) is an attempt that was answered and changed nothing,
// so counting the file's lines would confuse "was asked" with "did it".
func r13SuccessfulDeletions(log string) int {
	n := 0
	for _, line := range strings.Fields(log) {
		if !strings.Contains(line, ":") {
			n++
		}
	}
	return n
}

// r13DeleteRequests returns the uids the isolated endpoint was asked to honour.
func r13DeleteRequests(t *testing.T, stateDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "delete-uid-preconditions"))
	if err != nil {
		return nil
	}
	out := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// r13DeleteContentTypes proves the delete body arrived in a media type an API
// server can negotiate, not merely that a uid appeared somewhere.
func r13DeleteContentTypes(t *testing.T, stateDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "delete-content-types"))
	if err != nil {
		return nil
	}
	out := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}
