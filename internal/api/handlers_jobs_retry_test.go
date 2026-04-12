// internal/api/handlers_jobs_retry_test.go
//
// Tests for retry policy parsing in POST /jobs.

package api_test

import (
	"net/http"
	"testing"
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
