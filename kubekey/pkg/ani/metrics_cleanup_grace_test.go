package ani

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMetricsCleanupWaitsForNormalPodGrace(t *testing.T) {
	var mu sync.Mutex
	var deletedAt time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodDelete:
			var options metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "original-client" || options.GracePeriodSeconds != nil {
				t.Errorf("cleanup must preserve UID and normal grace: %+v", options)
				w.WriteHeader(400)
				return
			}
			deletedAt = time.Now()
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Success","code":200}`))
		case http.MethodGet:
			// Kubernetes accepts DELETE before the Pod's normal 30-second grace
			// and asynchronous removal have completed. A 30-second wait races it.
			if !deletedAt.IsZero() && time.Since(deletedAt) >= 31*time.Second {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`))
				return
			}
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"client","uid":"original-client"},"spec":{"terminationGracePeriodSeconds":30}}`))
		default:
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	config := r13WriteKubeconfig(t, server.URL, filepath.Join(dir, "kubeconfig"))
	a := &acceptanceAttempt{ctx: context.Background(), runner: kubectlRunner{kubeconfig: config},
		metrics: &metricsAttemptState{outputDir: dir, owned: []metricsOwned{{Kind: "pod", Name: "client", UID: "original-client"}}}}
	if err := a.metricsDeleteOwned(); err != nil {
		t.Fatalf("normal Pod termination must finish without force: %v", err)
	}
	if len(a.metrics.owned) != 0 {
		t.Fatal("cleanup did not close its ownership record")
	}
}

func TestMetricsCleanupResumesAfterOwnedPodIsAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	config := r13WriteKubeconfig(t, server.URL, filepath.Join(dir, "kubeconfig"))
	a := &acceptanceAttempt{ctx: context.Background(), runner: kubectlRunner{kubeconfig: config},
		metrics: &metricsAttemptState{outputDir: dir, owned: []metricsOwned{{Kind: "pod", Name: "client", UID: "original-client"}}}}
	if err := a.metricsDeleteOwned(); err != nil {
		t.Fatalf("an already absent recorded object must close cleanup: %v", err)
	}
	if len(a.metrics.owned) != 0 {
		t.Fatal("ownership record stayed open for an absent object")
	}
}
