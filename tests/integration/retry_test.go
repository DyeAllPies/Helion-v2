// tests/integration/retry_test.go
//
// End-to-end tests for retry policy API endpoints.
// Verifies that POST /jobs accepts retry_policy and that
// GET /jobs/{id} returns attempt and retry fields.

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
)

func startRetryAPIServer(t *testing.T, jobs *cluster.JobStore) string {
	t.Helper()
	addr := freePort(t)
	jobsAdapter := api.NewJobStoreAdapter(jobs)
	rateLimiter := ratelimit.NewNodeLimiter()
	srv := api.NewServer(jobsAdapter, nil, nil, nil, nil, rateLimiter, nil, nil)
	srv.DisableAuth()

	go func() {
		if err := srv.Serve(addr); err != nil && err != http.ErrServerClosed {
			t.Logf("API server: %v", err)
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return addr
}

// ── TestRetry_SubmitWithPolicy ───────────────────────────────────────────────

func TestRetry_SubmitWithPolicy(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startRetryAPIServer(t, jobs)

	body, _ := json.Marshal(map[string]interface{}{
		"id":      "retry-e2e-1",
		"command": "echo",
		"args":    []string{"hello"},
		"retry_policy": map[string]interface{}{
			"max_attempts":     3,
			"backoff":          "exponential",
			"initial_delay_ms": 1000,
			"max_delay_ms":     30000,
			"jitter":           true,
		},
	})

	resp, err := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /jobs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("POST /jobs returned %d: %s", resp.StatusCode, errResp.Error)
	}

	var result api.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.ID != "retry-e2e-1" {
		t.Errorf("id = %q, want retry-e2e-1", result.ID)
	}
	if result.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", result.Attempt)
	}

	t.Logf("job %s submitted with retry policy: attempt=%d", result.ID, result.Attempt)
}

// ── TestRetry_InvalidBackoff ─────────────────────────────────────────────────

func TestRetry_InvalidBackoff(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startRetryAPIServer(t, jobs)

	body, _ := json.Marshal(map[string]interface{}{
		"id":      "retry-bad-backoff",
		"command": "echo",
		"retry_policy": map[string]interface{}{
			"max_attempts": 3,
			"backoff":      "random",
		},
	})

	resp, err := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ── TestRetry_GetJobShowsAttempt ─────────────────────────────────────────────

func TestRetry_GetJobShowsAttempt(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startRetryAPIServer(t, jobs)

	body, _ := json.Marshal(map[string]interface{}{
		"id":      "retry-get-1",
		"command": "echo",
		"retry_policy": map[string]interface{}{
			"max_attempts": 5,
			"backoff":      "linear",
		},
	})
	resp, _ := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	getResp, err := http.Get(fmt.Sprintf("http://%s/jobs/retry-get-1", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()

	var result api.JobResponse
	if err := json.NewDecoder(getResp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", result.Attempt)
	}

	t.Logf("GET /jobs/%s: attempt=%d status=%s", result.ID, result.Attempt, result.Status)
}

// ── TestRetry_NoPolicy_DefaultBehavior ───────────────────────────────────────

func TestRetry_NoPolicy_DefaultBehavior(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startRetryAPIServer(t, jobs)

	// Submit without retry_policy — should still work (no retry).
	body, _ := json.Marshal(map[string]interface{}{
		"id":      "no-retry",
		"command": "echo",
	})
	resp, err := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var result api.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", result.Attempt)
	}
}
