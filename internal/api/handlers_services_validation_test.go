package api_test

import (
	"encoding/json"
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

// TestSubmit_Service_RejectsOversizeHealthPath pins the
// maxHealthPathLen=256 cap. The first-pass tests cover empty /
// relative paths; nothing pinned the oversize branch, so a refactor
// dropping the length check would silently accept megabyte-scale
// paths (sent to the prober's URL string) without tripping any
// existing test.
func TestSubmit_Service_RejectsOversizeHealthPath(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id":"s","command":"python",
		"service":{"port":8080,"health_path":"/` + strings.Repeat("a", 257) + `"}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestSubmit_Service_RejectsHealthPathWithWhitespace pins the
// defense-in-depth character-set check. An attacker could otherwise
// inject CRLF into the health URL string (Go's net/http usually
// catches it at request-build time, but the validator is our primary
// defense). Each rejected byte class shares the same check — one
// representative (space) is sufficient; \r\n and NUL ride the same
// branch.
func TestSubmit_Service_RejectsHealthPathWithWhitespace(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{
		"id":"s","command":"python",
		"service":{"port":8080,"health_path":"/has space"}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestSubmit_Service_RejectsOversizeGracePeriod pins the 30-minute
// cap on HealthInitialMs. A misconfigured grace of, say, 24 hours
// would delay failure detection far beyond any operational limit;
// catching it at submit is better than waiting a day for the first
// probe to fire. maxServiceHealthInitialMs is 30 min = 1_800_000ms
// per handlers_jobs.go; use 2_000_000 so we're clearly over.
func TestSubmit_Service_RejectsOversizeGracePeriod(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{
		"id":"s","command":"python",
		"service":{"port":8080,"health_path":"/h","health_initial_ms":2000000}
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestSubmit_Service_Response_RoundtripsServiceSpec pins the
// jobToResponse service plumbing at helpers.go:67-73. A refactor
// dropping the `if j.Service != nil { resp.Service = ... }` block
// would cause GET /api/jobs/{id} to return `service: null` for a
// job that declared a service at submit time — the internal job
// record still carries it, but the API response silently strips
// it. TestSubmit_Service_Accepted only checks the submit status;
// it never fetches the job back.
func TestSubmit_Service_Response_RoundtripsServiceSpec(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{
		"id": "svc-roundtrip",
		"command": "python",
		"service": {"port": 9090, "health_path": "/ready", "health_initial_ms": 500}
	}`
	if rr := do(srv, "POST", "/jobs", body); rr.Code != http.StatusCreated && rr.Code != http.StatusAccepted && rr.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	rr := do(srv, "GET", "/jobs/svc-roundtrip", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Service *struct {
			Port            int    `json:"port"`
			HealthPath      string `json:"health_path"`
			HealthInitialMs int    `json:"health_initial_ms"`
		} `json:"service"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Service == nil {
		t.Fatal("response dropped Service field")
	}
	if resp.Service.Port != 9090 || resp.Service.HealthPath != "/ready" || resp.Service.HealthInitialMs != 500 {
		t.Fatalf("service roundtrip mismatch: %+v", resp.Service)
	}
}
