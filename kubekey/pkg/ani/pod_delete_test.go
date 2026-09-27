/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ---------------------------------------------------------------------------
// C03 — the authorised Pod uid has to be inside the delete request.
//
// These tests point real client-go at an isolated local endpoint and read the
// request back off the wire: method, path, and the decoded DeleteOptions body. An
// assertion that some argv string merely contained the uid would prove nothing
// about what the server was asked to enforce, so there is no such assertion here.
//
// Scope of the fake, stated plainly: it emulates the documented HTTP contract of
// Preconditions ("If not possible, a 409 Conflict status will be returned") so the
// CLIENT side can be proved. It is a request-integration test, not a server-side
// test — proving etcd/API-server enforcement needs a real apiserver, which this
// round neither has a facility for nor permission to reach.
// ---------------------------------------------------------------------------

// c03PodServer is a minimal pod endpoint with one object per name.
type c03PodServer struct {
	t      *testing.T
	server *httptest.Server

	mu          sync.Mutex
	standing    map[string]string // namespace/name -> uid
	deleted     []string
	requests    []c03Request
	parseErrors []string

	// behaviour overrides, set per test
	failWith    *apierrors.StatusError
	delay       time.Duration
	skipConfirm bool
}

type c03Request struct {
	method string
	path   string
	body   string
	uid    string
	// contentType is captured because the DeleteOptions body is only decoded by the
	// API server if the request declares a media type. A client that sent the right
	// JSON with no Content-Type would pass a body-shape assertion and still fail on
	// a real server, so this records what actually arrived.
	contentType string
}

func c03StartServer(t *testing.T) *c03PodServer {
	t.Helper()
	fake := &c03PodServer{standing: map[string]string{}, deleted: []string{}, requests: []c03Request{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fake.handle(w, r)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *c03PodServer) put(namespace, name, uid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.standing[namespace+"/"+name] = uid
}

// kubeconfig writes a file that names this endpoint and nothing else, and returns
// its path. The current-context is what newPodDeleter resolves, exactly as a real
// operator's admin.conf would be.
func (f *c03PodServer) kubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c03.kubeconfig")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: isolated
  cluster:
    server: %s
contexts:
- name: isolated
  context:
    cluster: isolated
    user: isolated
current-context: isolated
users:
- name: isolated
  user:
    token: c03-test-token
`, f.server.URL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *c03PodServer) handle(w http.ResponseWriter, r *http.Request) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	// /api/v1/namespaces/<ns>/<resource>/<name>
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	name := segments[len(segments)-1]
	namespace := ""
	for i, segment := range segments {
		if segment == "namespaces" && i+1 < len(segments) {
			namespace = segments[i+1]
		}
	}
	body := make(map[string]any)
	if r.Body != nil {
		raw := make([]byte, 0, 512)
		buf := make([]byte, 512)
		for {
			n, err := r.Body.Read(buf)
			raw = append(raw, buf[:n]...)
			if err != nil {
				break
			}
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			f.mu.Lock()
			f.parseErrors = append(f.parseErrors, fmt.Sprintf("ct=%q raw=%q", r.Header.Get("Content-Type"), string(raw)))
			f.mu.Unlock()
		}
	}
	preconditions, _ := body["preconditions"].(map[string]any)
	uid, _ := preconditions["uid"].(string)
	key := namespace + "/" + name
	f.mu.Lock()
	f.requests = append(f.requests, c03Request{method: r.Method, path: r.URL.Path, body: fmt.Sprint(body), uid: uid, contentType: r.Header.Get("Content-Type")})
	f.mu.Unlock()
	current, exists := f.standing[key]

	switch r.Method {
	case http.MethodDelete:
		f.handleDelete(w, key, name, uid, current, exists)
	case http.MethodGet:
		f.handleGet(w, name, current, exists)
	default:
		f.writeStatus(w, http.StatusMethodNotAllowed, "unsupported", name)
	}
}

func (f *c03PodServer) handleDelete(w http.ResponseWriter, key, name, uid, current string, exists bool) {
	f.mu.Lock()
	f.mu.Unlock()
	if !exists {
		f.writeStatus(w, http.StatusNotFound, "pods", name)
		return
	}
	// The documented precondition rule: a uid that does not match the stored object
	// is a 409 and nothing is deleted. This is the behaviour the client must rely
	// on, so the fake enforces it rather than trusting the client to have checked.
	if !f.skipConfirm && uid == "" {
		f.mu.Lock()
		f.deleted = append(f.deleted, name+":NO-PRECONDITION")
		f.mu.Unlock()
		f.writeStatus(w, http.StatusBadRequest, "DeleteOptions", "no precondition was sent")
		return
	}
	if uid != "" && uid != current {
		f.markDeleted(name + ":REFUSED(" + uid + "!=" + current + ")")
		f.writeStatus(w, http.StatusConflict, "pods", name)
		return
	}
	if f.failWith != nil {
		f.writeStatusError(w, name)
		return
	}
	f.markDeleted(name)
	f.mu.Lock()
	delete(f.standing, key)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success","code":200}`))
}

func (f *c03PodServer) handleGet(w http.ResponseWriter, name, current string, exists bool) {
	if !exists {
		f.writeStatus(w, http.StatusNotFound, "pods", name)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"kind":"Pod","apiVersion":"v1","metadata":{"name":%q,"namespace":"ani-platform","uid":%q,"resourceVersion":"100"}}`, name, current)
}

func (f *c03PodServer) writeStatus(w http.ResponseWriter, code int, kind, name string) {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   "Failure",
		Message:  fmt.Sprintf("%s %q rejected", kind, name),
		Reason:   metav1.StatusReason(reasonFor(code)),
		Code:     int32(code),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

func (f *c03PodServer) writeStatusError(w http.ResponseWriter, name string) {
	status := f.failWith.Status()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(status.Code))
	_ = json.NewEncoder(w).Encode(status)
}

func reasonFor(code int) metav1.StatusReason {
	switch code {
	case http.StatusNotFound:
		return metav1.StatusReasonNotFound
	case http.StatusConflict:
		return metav1.StatusReasonConflict
	case http.StatusForbidden:
		return metav1.StatusReasonForbidden
	default:
		return metav1.StatusReasonInternalError
	}
}

func (f *c03PodServer) markDeleted(what string) {
	f.mu.Lock()
	f.deleted = append(f.deleted, what)
	f.mu.Unlock()
}

// standingAt reads the object the fake still holds, so "the replacement survived"
// is answered by the server's state rather than by a client-side claim.
func (f *c03PodServer) standingAt(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.standing[key]
}

func (f *c03PodServer) parseFailures() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.parseErrors...)
}

func (f *c03PodServer) deletes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func (f *c03PodServer) deleteRequests() []c03Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []c03Request{}
	for _, r := range f.requests {
		if r.method == http.MethodDelete {
			out = append(out, r)
		}
	}
	return out
}

func TestC03_ClientGoDeleteCarriesTheAuthorizedUID(t *testing.T) {
	fake := c03StartServer(t)
	fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
	deleter, err := newPodDeleter(fake.kubeconfig(t))
	if err != nil {
		t.Fatalf("build the deleter from the isolated kubeconfig: %v", err)
	}
	outcome, err := deleter.deletePodWithUIDPrecondition(context.Background(), "ani-platform", "postgresql-0", "uid-old-postgresql-0")
	if err != nil {
		t.Fatalf("the matching delete should succeed: %v", err)
	}
	if outcome != deleteDeleted {
		t.Fatalf("outcome = %q, want %q", outcome, deleteDeleted)
	}
	requests := fake.deleteRequests()
	if len(requests) != 1 {
		t.Fatalf("exactly one delete request, got %v", requests)
	}
	got := requests[0]
	// Method and path: a delete of THE named pod, not a collection delete and not a
	// delete selected by labels.
	if got.method != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE", got.method)
	}
	if got.path != "/api/v1/namespaces/ani-platform/pods/postgresql-0" {
		t.Fatalf("path = %q; the delete must name the pod in its namespace", got.path)
	}
	// The body is where the authorisation lives.
	if got.uid != "uid-old-postgresql-0" {
		t.Fatalf("the request body carried preconditions.uid = %q, want uid-old-postgresql-0; body was %s", got.uid, got.body)
	}
	if !strings.Contains(got.body, "preconditions") {
		t.Fatalf("no preconditions in the request body: %s", got.body)
	}
	if !strings.Contains(got.contentType, "json") {
		t.Fatalf("the DeleteOptions body arrived as %q; the API server only decodes a delete body it can negotiate, so an empty or non-JSON media type would be rejected on a real cluster while passing every shape assertion here", got.contentType)
	}
	if fails := fake.parseFailures(); len(fails) != 0 {
		t.Fatalf("the delete body did not parse as JSON: %v", fails)
	}
	if deletes := fake.deletes(); len(deletes) != 1 || deletes[0] != "postgresql-0" {
		t.Fatalf("the server should have deleted exactly the authorised pod, got %v", deletes)
	}
}

func TestC03_SameNameReplacementIsRefusedAndNotDeleted(t *testing.T) {
	fake := c03StartServer(t)
	fake.put("ani-platform", "postgresql-0", "uid-INTRUDER-replaced")
	deleter, err := newPodDeleter(fake.kubeconfig(t))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := deleter.deletePodWithUIDPrecondition(context.Background(), "ani-platform", "postgresql-0", "uid-old-postgresql-0")
	if outcome != deleteConflict {
		t.Fatalf("a uid mismatch must report %q, got %q (%v)", deleteConflict, outcome, err)
	}
	if err == nil || !apierrors.IsConflict(err) {
		t.Fatalf("the 409 must remain recognisable as a conflict: %v", err)
	}
	// The whole point: the object standing there now is untouched.
	deletes := fake.deletes()
	if len(deletes) != 1 || !strings.HasPrefix(deletes[0], "postgresql-0:REFUSED") {
		t.Fatalf("the server must refuse without removing the replacement, got %v", deletes)
	}
	if standing := fake.standingAt("ani-platform/postgresql-0"); standing != "uid-INTRUDER-replaced" {
		t.Fatalf("the intruder pod was removed (uid now %q)", standing)
	}
	// And the client must not have quietly retried with the new uid.
	if n := len(fake.deleteRequests()); n != 1 {
		t.Fatalf("the refusal triggered %d delete requests; a precondition failure is never retried", n)
	}
}

func TestC03_ForbiddenTimeoutAndCancellationAreDistinct(t *testing.T) {
	t.Run("forbidden", func(t *testing.T) {
		fake := c03StartServer(t)
		fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
		fake.failWith = apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "postgresql-0", fmt.Errorf("no rbac"))
		deleter, err := newPodDeleter(fake.kubeconfig(t))
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := deleter.deletePodWithUIDPrecondition(context.Background(), "ani-platform", "postgresql-0", "uid-old-postgresql-0")
		if outcome != deleteForbidden {
			t.Fatalf("a refusal must report %q, got %q (%v)", deleteForbidden, outcome, err)
		}
		if fake.standingAt("ani-platform/postgresql-0") == "" {
			t.Fatal("a forbidden delete still removed the object")
		}
	})

	t.Run("not found is not deleted", func(t *testing.T) {
		fake := c03StartServer(t)
		deleter, err := newPodDeleter(fake.kubeconfig(t))
		if err != nil {
			t.Fatal(err)
		}
		outcome, _ := deleter.deletePodWithUIDPrecondition(context.Background(), "ani-platform", "postgresql-0", "uid-old-postgresql-0")
		if outcome != deleteNotFound {
			t.Fatalf("absent target must report %q, got %q", deleteNotFound, outcome)
		}
	})

	t.Run("timeout is reported once and never retried", func(t *testing.T) {
		fake := c03StartServer(t)
		fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
		fake.delay = 3 * time.Second
		deleter, err := newPodDeleter(fake.kubeconfig(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		start := time.Now()
		outcome, err := deleter.deletePodWithUIDPrecondition(ctx, "ani-platform", "postgresql-0", "uid-old-postgresql-0")
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("the delete blocked for %s; a bounded request must respect its context", elapsed)
		}
		if outcome == deleteDeleted {
			t.Fatalf("a timed-out delete must never be reported as %q", deleteDeleted)
		}
		if err == nil {
			t.Fatal("a timed-out delete returned no error")
		}
		if n := len(fake.deleteRequests()); n > 1 {
			t.Fatalf("the timeout produced %d delete requests; no retry is allowed", n)
		}
	})

	t.Run("an already finished context never reaches the server", func(t *testing.T) {
		fake := c03StartServer(t)
		fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
		deleter, err := newPodDeleter(fake.kubeconfig(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome, err := deleter.deletePodWithUIDPrecondition(ctx, "ani-platform", "postgresql-0", "uid-old-postgresql-0")
		if outcome == deleteDeleted || err == nil {
			t.Fatalf("a cancelled caller still performed a delete: %q %v", outcome, err)
		}
		if n := len(fake.deleteRequests()); n != 0 {
			t.Fatalf("a cancelled caller sent %d requests", n)
		}
	})
}

func TestC03_DeleteInputsAreRefusedLocally(t *testing.T) {
	fake := c03StartServer(t)
	fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
	deleter, err := newPodDeleter(fake.kubeconfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, ns, pod, uid string }{
		{"no uid", "ani-platform", "postgresql-0", ""},
		{"no namespace", "", "postgresql-0", "uid-old-postgresql-0"},
		{"no pod name", "ani-platform", "", "uid-old-postgresql-0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := deleter.deletePodWithUIDPrecondition(context.Background(), tc.ns, tc.pod, tc.uid)
			if err == nil || outcome == deleteDeleted {
				t.Fatalf("an empty %s must be refused before any request: %q %v", tc.name, outcome, err)
			}
			if n := len(fake.deleteRequests()); n != 0 {
				t.Fatalf("an incomplete delete reached the server anyway (%d requests)", n)
			}
		})
	}
}

func TestC03_KubeconfigSelectionNeverFallsBack(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := newPodDeleter(filepath.Join(t.TempDir(), "absent.kubeconfig")); err == nil {
			t.Fatal("a missing explicit kubeconfig was accepted; some other context would be used instead")
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if _, err := newPodDeleter("  "); err == nil {
			t.Fatal("an empty kubeconfig path must be refused, not defaulted")
		}
	})
	t.Run("environment is not consulted", func(t *testing.T) {
		fake := c03StartServer(t)
		fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
		belongsElsewhere := fake.kubeconfig(t)
		decoy := filepath.Join(t.TempDir(), "decoy.kubeconfig")
		if err := os.WriteFile(decoy, []byte(strings.Replace(
			mustRead(t, belongsElsewhere), fake.server.URL, "https://127.0.0.1:1", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		// $KUBECONFIG and $HOME point at the decoy; the explicit path must win.
		t.Setenv("KUBECONFIG", decoy)
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".kube", "config"), []byte(mustRead(t, decoy)), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		deleter, err := newPodDeleter(belongsElsewhere)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deleter.deletePodWithUIDPrecondition(context.Background(), "ani-platform", "postgresql-0", "uid-old-postgresql-0"); err != nil {
			t.Fatalf("the explicit kubeconfig was not the one used: %v", err)
		}
		if n := len(fake.deleteRequests()); n != 1 {
			t.Fatalf("the delete went somewhere else (%d requests reached the pinned endpoint)", n)
		}
	})
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A precondition-free delete must be impossible to reach: the fake server rejects
// one outright, so this pins that the production path never sends it.
func TestC03_NoPreconditionFreeDeleteReachesTheServer(t *testing.T) {
	fake := c03StartServer(t)
	fake.put("ani-platform", "postgresql-0", "uid-old-postgresql-0")
	deleter, err := newPodDeleter(fake.kubeconfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleter.deletePodWithUIDPrecondition(context.Background(), "ani-platform", "postgresql-0", "uid-old-postgresql-0"); err != nil {
		t.Fatalf("the happy path should delete: %v", err)
	}
	for _, r := range fake.deleteRequests() {
		if r.uid == "" {
			t.Fatalf("a delete without a uid precondition was sent to %s: %s", r.path, r.body)
		}
	}
}
