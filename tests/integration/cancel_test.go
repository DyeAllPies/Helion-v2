// tests/integration/cancel_test.go
//
// End-to-end tests for POST /jobs/{id}/cancel.

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

func startCancelAPIServer(t *testing.T, jobs *cluster.JobStore) string {
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

func TestCancel_PendingJob(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startCancelAPIServer(t, jobs)

	// Submit a job.
	body, _ := json.Marshal(map[string]interface{}{
		"id": "cancel-e2e-1", "command": "sleep", "args": []string{"3600"},
	})
	resp, err := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /jobs: %v", err)
	}
	resp.Body.Close()

	// Cancel it.
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/jobs/cancel-e2e-1/cancel", addr), nil)
	cancelResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jobs/cancel-e2e-1/cancel: %v", err)
	}
	defer cancelResp.Body.Close()

	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel returned %d, want 200", cancelResp.StatusCode)
	}

	// Verify job is cancelled.
	getResp, err := http.Get(fmt.Sprintf("http://%s/jobs/cancel-e2e-1", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()

	var result api.JobResponse
	if err := json.NewDecoder(getResp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", result.Status)
	}
	t.Logf("job %s cancelled: status=%s", result.ID, result.Status)
}

func TestCancel_NotFound(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startCancelAPIServer(t, jobs)

	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/jobs/nonexistent/cancel", addr), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCancel_AlreadyTerminal(t *testing.T) {
	persister, err := cluster.NewBadgerJSONPersister(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	addr := startCancelAPIServer(t, jobs)

	body, _ := json.Marshal(map[string]interface{}{
		"id": "cancel-term", "command": "echo",
	})
	resp, _ := http.Post("http://"+addr+"/jobs", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	// Cancel once.
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/jobs/cancel-term/cancel", addr), nil)
	r1, _ := http.DefaultClient.Do(req)
	r1.Body.Close()

	// Cancel again — should conflict.
	req2, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/jobs/cancel-term/cancel", addr), nil)
	r2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer r2.Body.Close()

	if r2.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", r2.StatusCode)
	}
}
