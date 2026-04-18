// internal/api/handlers_jobs_retry_test.go
//
// Tests for retry policy parsing in POST /jobs.

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
)

func TestSubmitJob_WithRetryPolicy_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "retry-job",
		"command": "echo",
		"retry_policy": {
			"max_attempts": 3,
			"backoff": "exponential",
			"initial_delay_ms": 2000,
			"max_delay_ms": 30000,
			"jitter": true
		}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_RetryPolicy_BadBackoff_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "bad-backoff",
		"command": "echo",
		"retry_policy": {
			"max_attempts": 3,
			"backoff": "invalid"
		}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSubmitJob_RetryPolicy_TooManyAttempts_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "too-many",
		"command": "echo",
		"retry_policy": {"max_attempts": 999}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSubmitJob_RetryPolicy_SingleAttempt_NoRetryStored(t *testing.T) {
	store := newMockJobStore()
	srv := newServer(store, nil, nil)
	body := `{
		"id": "single-attempt",
		"command": "echo",
		"retry_policy": {"max_attempts": 1}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
	j := store.jobs["single-attempt"]
	if j.RetryPolicy != nil {
		t.Errorf("expected nil RetryPolicy for max_attempts=1, got %+v", j.RetryPolicy)
	}
}

func TestSubmitJob_RetryPolicy_LinearBackoff_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "linear-retry",
		"command": "echo",
		"retry_policy": {
			"max_attempts": 5,
			"backoff": "linear"
		}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

func TestSubmitJob_RetryPolicy_NoneBackoff_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "none-retry",
		"command": "echo",
		"retry_policy": {
			"max_attempts": 2,
			"backoff": "none",
			"jitter": false
		}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

// ── POST /jobs/{id}/cancel ──────────────────────────────────────────────────

func TestCancelJobAPI_Pending_Returns200(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	do(srv, "POST", "/jobs", `{"id":"can-1","command":"echo"}`)

	rr := do(srv, "POST", "/jobs/can-1/cancel", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", resp["status"])
	}
}

func TestCancelJobAPI_NotFound_Returns404(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs/nonexistent/cancel", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ── Priority ─────────────────────────────────────────────────────────────────

func TestSubmitJob_WithPriority_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"pri-1","command":"echo","priority":90}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp api.JobResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Priority != 90 {
		t.Errorf("priority = %d, want 90", resp.Priority)
	}
}

func TestSubmitJob_PriorityTooHigh_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"pri-bad","command":"echo","priority":101}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for priority > 100", rr.Code)
	}
}

func TestSubmitJob_NoPriority_Accepted(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"pri-default","command":"echo"}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

// ── Resources ────────────────────────────────────────────────────────────────

func TestSubmitJob_WithResources_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "resource-job",
		"command": "echo",
		"resources": {
			"cpu_millicores": 500,
			"memory_bytes": 134217728,
			"slots": 2
		}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
}

// TestSubmitJob_GPUsAtMax_Returns201 pins the maxGPUs = 16 boundary
// as accepted (feature-15 ResourceRequest.GPUs cap). Mirrors the
// priority / workflow-job-count pattern: one test for the inclusive
// upper bound, one for the first rejected value. Without these,
// an off-by-one flip from > to >= in validateSubmitRequest would
// silently reject every 16-GPU request while every existing
// resources test (which uses 0–2 GPUs) stayed green.
func TestSubmitJob_GPUsAtMax_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "gpu-16",
		"command": "echo",
		"resources": {"gpus": 16}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 at maxGPUs boundary; body: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_GPUsExceedsMax_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "gpu-17",
		"command": "echo",
		"resources": {"gpus": 17}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for gpus > maxGPUs; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCancelJobAPI_AlreadyTerminal_Returns409(t *testing.T) {
	store := newMockJobStore()
	srv := newServer(store, nil, nil)
	do(srv, "POST", "/jobs", `{"id":"can-done","command":"echo"}`)

	// Cancel once.
	do(srv, "POST", "/jobs/can-done/cancel", "")

	// Cancel again — already terminal.
	rr := do(srv, "POST", "/jobs/can-done/cancel", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}
