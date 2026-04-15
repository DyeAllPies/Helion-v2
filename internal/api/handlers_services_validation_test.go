package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// Service-submit validation: exercises the feature-17 rules in
// validateServiceSpec. Uses the disable-auth test server since we're
// not testing auth here.

func TestSubmit_Service_Accepted(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "svc-1",
		"command": "python",
		"args": ["-m", "myserver"],
		"service": {"port": 8080, "health_path": "/healthz", "health_initial_ms": 2000}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusAccepted && rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("service submit: %d %s", rr.Code, rr.Body.String())
	}
}

func TestSubmit_Service_RejectsPrivilegedPort(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{
		"id":"s","command":"python",
		"service":{"port":80,"health_path":"/"}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "privileged") {
		t.Fatalf("want privileged-port error: %s", rr.Body.String())
	}
}

func TestSubmit_Service_RejectsOutOfRangePort(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{
		"id":"s","command":"python",
		"service":{"port":70000,"health_path":"/h"}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmit_Service_RejectsEmptyHealthPath(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{
		"id":"s","command":"python",
		"service":{"port":8080,"health_path":""}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestSubmit_Service_RejectsRelativeHealthPath(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{
		"id":"s","command":"python",
		"service":{"port":8080,"health_path":"healthz"}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}
