package ani

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryStartupWaitsForLateRealReadiness(t *testing.T) {
	// The first B13 clean install imported all 86 images seven seconds after
	// the old 60-second deadline. Guard that the production budget covers
	// that measured startup without making an unresponsive service pass.
	if registryStartupTimeout < 2*time.Minute {
		t.Fatalf("registry startup budget %s cannot cover the observed 68-second import", registryStartupTimeout)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		if requests.Add(1) < 5 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	if err := waitRegistryHTTP(context.Background(), address, time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("late readiness should pass: %v", err)
	}
	if got := requests.Load(); got < 5 {
		t.Fatalf("registry passed before HTTP 200, requests=%d", got)
	}
}

func TestRegistryStartupStopsAndReportsRealFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	started := time.Now()
	err := waitRegistryHTTP(context.Background(), address, 250*time.Millisecond, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out after 250ms") {
		t.Fatalf("unready registry must report bounded failure: %v", err)
	}
	// A request still in flight at the deadline legitimately becomes the last
	// transport error, even if earlier requests received HTTP 503. Requiring
	// that the final request finish before the deadline made this test flaky
	// under the formal gate's load; neither outcome makes readiness pass.
	if !strings.Contains(err.Error(), "HTTP 503") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("failure lost the actual HTTP or transport result: %v", err)
	}
	if requests.Load() == 0 {
		t.Fatal("the readiness check never reached the real HTTP server")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("registry wait exceeded its bounded deadline: %s", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRegistryHTTP(ctx, address, time.Second, 10*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation must stay cancellation, got %v", err)
	}
}
