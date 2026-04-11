// internal/api/handlers_jobs_test.go
//
// Tests for POST /jobs, GET /jobs/{id}, GET /jobs, and JobStoreAdapter.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── POST /jobs ────────────────────────────────────────────────────────────────

func TestSubmitJob_ValidRequest_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"job-1","command":"echo","args":["hello"]}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "job-1" {
		t.Errorf("want id=job-1, got %q", resp.ID)
	}
	if resp.Command != "echo" {
		t.Errorf("want command=echo, got %q", resp.Command)
	}
}

func TestSubmitJob_MissingID_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"command":"echo"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_MissingCommand_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"job-1"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_InvalidJSON_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `not json`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_StoreError_Returns500(t *testing.T) {
	js := newMockJobStore()
	js.submitErr = errors.New("storage full")
	srv := newServer(js, nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"job-1","command":"ls"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

func TestSubmitJob_ResponseContainsStatus(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"job-status","command":"test"}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"status"`)) {
		t.Error("response should contain status field")
	}
}

func TestSubmitJob_WithAuditLog_LogsEvent(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	body := `{"id":"audit-job","command":"echo"}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_WithTokenAndAudit_ActorFromClaims(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(context.Background(), store)
	auditLog := audit.NewLogger(newAuditStore(), 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken(context.Background(), "submit-user", "admin", time.Minute)
	body := `{"id":"sa-job","command":"echo"}`
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_WithEnvAndTimeout_StoredAndReturned(t *testing.T) {
	js := newMockJobStore()
	srv := newServer(js, nil, nil)

	body := `{
		"id": "job-env",
		"command": "python3",
		"args": ["-c", "import os; print(os.getenv('FOO'))"],
		"env": {"FOO": "bar", "WORKERS": "4"},
		"timeout_seconds": 30
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Env["FOO"] != "bar" {
		t.Errorf("env FOO: want 'bar', got %q", resp.Env["FOO"])
	}
	if resp.Env["WORKERS"] != "4" {
		t.Errorf("env WORKERS: want '4', got %q", resp.Env["WORKERS"])
	}
	if resp.TimeoutSeconds != 30 {
		t.Errorf("timeout_seconds: want 30, got %d", resp.TimeoutSeconds)
	}

	stored := js.jobs["job-env"]
	if stored == nil {
		t.Fatal("job not found in store")
	}
	if stored.Env["FOO"] != "bar" {
		t.Errorf("stored env FOO: want 'bar', got %q", stored.Env["FOO"])
	}
	if stored.TimeoutSeconds != 30 {
		t.Errorf("stored timeout_seconds: want 30, got %d", stored.TimeoutSeconds)
	}
}

func TestSubmitJob_NoEnvNoTimeout_DefaultsToZero(t *testing.T) {
	js := newMockJobStore()
	srv := newServer(js, nil, nil)

	body := `{"id": "job-plain", "command": "echo"}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TimeoutSeconds != 0 {
		t.Errorf("timeout_seconds: want 0, got %d", resp.TimeoutSeconds)
	}
	if len(resp.Env) != 0 {
		t.Errorf("env: want empty, got %v", resp.Env)
	}
}

func TestSubmitJob_WithLimits_StoredAndReturned(t *testing.T) {
	js := newMockJobStore()
	srv := newServer(js, nil, nil)

	body := `{
		"id": "job-limits",
		"command": "stress",
		"limits": {
			"memory_bytes": 536870912,
			"cpu_quota_us": 50000,
			"cpu_period_us": 100000
		}
	}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Limits.MemoryBytes != 536870912 {
		t.Errorf("memory_bytes: want 536870912, got %d", resp.Limits.MemoryBytes)
	}
	if resp.Limits.CPUQuotaUS != 50000 {
		t.Errorf("cpu_quota_us: want 50000, got %d", resp.Limits.CPUQuotaUS)
	}
	if resp.Limits.CPUPeriodUS != 100000 {
		t.Errorf("cpu_period_us: want 100000, got %d", resp.Limits.CPUPeriodUS)
	}

	stored := js.jobs["job-limits"]
	if stored == nil {
		t.Fatal("job not found in store")
	}
	if stored.Limits.MemoryBytes != 536870912 {
		t.Errorf("stored memory_bytes: want 536870912, got %d", stored.Limits.MemoryBytes)
	}
	if stored.Limits.CPUQuotaUS != 50000 {
		t.Errorf("stored cpu_quota_us: want 50000, got %d", stored.Limits.CPUQuotaUS)
	}
}

func TestSubmitJob_NoLimits_DefaultsToZero(t *testing.T) {
	js := newMockJobStore()
	srv := newServer(js, nil, nil)

	rr := do(srv, "POST", "/jobs", `{"id":"job-nolimits","command":"echo"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Limits.MemoryBytes != 0 || resp.Limits.CPUQuotaUS != 0 || resp.Limits.CPUPeriodUS != 0 {
		t.Errorf("limits should be zero, got %+v", resp.Limits)
	}
}

// ── AUDIT C4 / C5: input validation ──────────────────────────────────────────

func TestSubmitJob_CommandWithSlash_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"j","command":"/bin/echo"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("command with '/': want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_CommandWithShellMeta_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"j","command":"echo; rm -rf /"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("command with ';': want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_CommandWithDotDot_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// ".." on its own is allowed as a command name (it contains no forbidden
	// chars), but a path like "..\evil" or "../evil" is blocked via `/` or `\`.
	rr := do(srv, "POST", "/jobs", `{"id":"j","command":"../evil"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("command with '../': want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_TooManyArgs_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// Build 513 args — one over the limit.
	var b strings.Builder
	b.WriteString(`{"id":"j","command":"echo","args":[`)
	for i := 0; i < 513; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"x"`)
	}
	b.WriteString(`]}`)
	rr := do(srv, "POST", "/jobs", b.String())
	if rr.Code != http.StatusBadRequest {
		t.Errorf("513 args: want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_TooManyEnvKeys_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	var b strings.Builder
	b.WriteString(`{"id":"j","command":"echo","env":{`)
	for i := 0; i < 129; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"K%d":"v"`, i)
	}
	b.WriteString(`}}`)
	rr := do(srv, "POST", "/jobs", b.String())
	if rr.Code != http.StatusBadRequest {
		t.Errorf("129 env keys: want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_EnvKeyWithEquals_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs",
		`{"id":"j","command":"echo","env":{"BAD=KEY":"v"}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("env key with '=': want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_EnvValueWithNUL_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// NUL in a JSON string is encoded as \u0000.
	rr := do(srv, "POST", "/jobs",
		`{"id":"j","command":"echo","env":{"K":"bad\u0000value"}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("env value with NUL: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_MemoryBelowMinimum_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// 1 MiB is below the 4 MiB floor.
	rr := do(srv, "POST", "/jobs",
		`{"id":"j","command":"echo","limits":{"memory_bytes":1048576}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("memory below floor: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_CPUPeriodTooSmall_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs",
		`{"id":"j","command":"echo","limits":{"cpu_period_us":500}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("cpu_period_us=500: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_CPUPeriodTooLarge_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs",
		`{"id":"j","command":"echo","limits":{"cpu_period_us":2000000}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("cpu_period_us=2,000,000: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_CPUQuotaTooHigh_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// period=100000, quota=100000*513 exceeds 512-core cap.
	rr := do(srv, "POST", "/jobs",
		`{"id":"j","command":"echo","limits":{"cpu_period_us":100000,"cpu_quota_us":51300000}}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("cpu_quota_us > 512×period: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_OversizedBody_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)

	// Build a payload larger than maxSubmitBodyBytes (1 MiB).
	prefix := `{"id":"big","command":"echo","args":["`
	suffix := `"]}`
	padding := strings.Repeat("x", 1<<20+1) // just over 1 MiB
	body := prefix + padding + suffix

	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("oversized body: want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_NegativeTimeout_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"j","command":"ls","timeout_seconds":-1}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("negative timeout: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_ExcessiveTimeout_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"j","command":"ls","timeout_seconds":3601}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("excessive timeout: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_ZeroTimeout_Accepted(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"j-zero","command":"ls","timeout_seconds":0}`)
	if rr.Code != http.StatusCreated {
		t.Errorf("zero timeout: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_MaxTimeout_Accepted(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"j-max","command":"ls","timeout_seconds":3600}`)
	if rr.Code != http.StatusCreated {
		t.Errorf("max timeout: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_DuplicateID_Returns409(t *testing.T) {
	p := cluster.NewMemJobPersister()
	cs := cluster.NewJobStore(p, nil)
	adapter := api.NewJobStoreAdapter(cs)
	srv := api.NewServer(adapter, nil, nil, nil, nil, nil, nil, nil)
	srv.DisableAuth()

	body := `{"id":"dup-job","command":"echo"}`

	rr1 := do(srv, "POST", "/jobs", body)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first submit: want 201, got %d: %s", rr1.Code, rr1.Body.String())
	}

	rr2 := do(srv, "POST", "/jobs", body)
	if rr2.Code != http.StatusConflict {
		t.Errorf("duplicate submit: want 409, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

// ── GET /jobs/{id} ────────────────────────────────────────────────────────────

func TestGetJob_ExistingJob_Returns200(t *testing.T) {
	js := newMockJobStore()
	js.jobs["job-42"] = &cpb.Job{ID: "job-42", Command: "ls", Status: cpb.JobStatusRunning}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs/job-42", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "job-42" {
		t.Errorf("want id=job-42, got %q", resp.ID)
	}
}

func TestGetJob_NotFound_Returns404(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs/nonexistent", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestJobResponse_ContentTypeJSON(t *testing.T) {
	js := newMockJobStore()
	js.jobs["j"] = &cpb.Job{ID: "j", Command: "pwd"}
	srv := newServer(js, nil, nil)
	rr := do(srv, "GET", "/jobs/j", "")
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type: application/json, got %q", ct)
	}
}

func TestGetJob_WithFinishedAt_ResponseIncludesFinishedAt(t *testing.T) {
	js := newMockJobStore()
	finishedAt := time.Now().Add(-5 * time.Minute)
	js.jobs["j-done"] = &cpb.Job{
		ID:         "j-done",
		Command:    "ls",
		Status:     cpb.JobStatusCompleted,
		FinishedAt: finishedAt,
	}
	srv := newServer(js, nil, nil)
	rr := do(srv, "GET", "/jobs/j-done", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "finished_at") {
		t.Error("want finished_at in response for completed job")
	}
}

func TestGetJob_EnvAndTimeoutRoundtrip(t *testing.T) {
	js := newMockJobStore()
	js.jobs["job-get-env"] = &cpb.Job{
		ID:             "job-get-env",
		Command:        "bash",
		Env:            map[string]string{"KEY": "val"},
		TimeoutSeconds: 60,
		Status:         cpb.JobStatusPending,
	}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs/job-get-env", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Env["KEY"] != "val" {
		t.Errorf("env KEY: want 'val', got %q", resp.Env["KEY"])
	}
	if resp.TimeoutSeconds != 60 {
		t.Errorf("timeout_seconds: want 60, got %d", resp.TimeoutSeconds)
	}
}

func TestGetJob_LimitsRoundtrip(t *testing.T) {
	js := newMockJobStore()
	js.jobs["job-get-limits"] = &cpb.Job{
		ID:      "job-get-limits",
		Command: "bench",
		Status:  cpb.JobStatusPending,
		Limits:  cpb.ResourceLimits{MemoryBytes: 1073741824, CPUQuotaUS: 25000},
	}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs/job-get-limits", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Limits.MemoryBytes != 1073741824 {
		t.Errorf("memory_bytes: want 1073741824, got %d", resp.Limits.MemoryBytes)
	}
	if resp.Limits.CPUQuotaUS != 25000 {
		t.Errorf("cpu_quota_us: want 25000, got %d", resp.Limits.CPUQuotaUS)
	}
}

func TestGetJob_EmptyID_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	req := httptest.NewRequest("GET", "/jobs/", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	// Either 400 or 404 is acceptable; we just want no panic.
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("unexpected status %d", rr.Code)
	}
}

// ── GET /jobs ─────────────────────────────────────────────────────────────────

func TestListJobs_Returns200WithJobs(t *testing.T) {
	js := newMockJobStore()
	js.jobs["j1"] = &cpb.Job{ID: "j1", Command: "ls"}
	js.jobs["j2"] = &cpb.Job{ID: "j2", Command: "echo"}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}

	var resp api.JobListResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("want total=2, got %d", resp.Total)
	}
}

func TestListJobs_StoreError_Returns500(t *testing.T) {
	js := newMockJobStore()
	js.listErr = errors.New("db error")
	srv := newServer(js, nil, nil)
	rr := do(srv, "GET", "/jobs", "")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

func TestListJobs_PageZero_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?page=0", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("page=0: want 400, got %d", rr.Code)
	}
}

func TestListJobs_NegativePage_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?page=-1", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("page=-1: want 400, got %d", rr.Code)
	}
}

func TestListJobs_SizeZero_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?size=0", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("size=0: want 400, got %d", rr.Code)
	}
}

func TestListJobs_SizeOverMax_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?size=101", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("size=101: want 400, got %d", rr.Code)
	}
}

func TestListJobs_InvalidStatus_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?status=BOGUS", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=BOGUS: want 400, got %d", rr.Code)
	}
}

func TestListJobs_NonIntegerPage_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?page=abc", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("page=abc: want 400, got %d", rr.Code)
	}
}

func TestListJobs_NonIntegerSize_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?size=big", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("size=big: want 400, got %d", rr.Code)
	}
}

func TestListJobs_PageOverMax_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs?page=10001", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("page=10001: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── JobStoreAdapter ───────────────────────────────────────────────────────────

func TestJobStoreAdapter_List_WithStatusFilter_ReturnsFiltered(t *testing.T) {
	p := cluster.NewMemJobPersister()
	cs := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	for _, id := range []string{"a1", "a2"} {
		if err := cs.Submit(ctx, &cpb.Job{ID: id, Command: "ls"}); err != nil {
			t.Fatalf("Submit %s: %v", id, err)
		}
	}

	adapter := api.NewJobStoreAdapter(cs)

	jobs, total, err := adapter.List(ctx, "PENDING", 1, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Errorf("want total=2, got %d", total)
	}
	if len(jobs) != 2 {
		t.Errorf("want 2 jobs, got %d", len(jobs))
	}
}

func TestJobStoreAdapter_List_PageBeyondEnd_ReturnsEmpty(t *testing.T) {
	p := cluster.NewMemJobPersister()
	cs := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	_ = cs.Submit(ctx, &cpb.Job{ID: "b1", Command: "ls"})

	adapter := api.NewJobStoreAdapter(cs)

	jobs, total, err := adapter.List(ctx, "", 2, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Errorf("want total=1, got %d", total)
	}
	if len(jobs) != 0 {
		t.Errorf("want 0 jobs for out-of-range page, got %d", len(jobs))
	}
}

// ── AUDIT L1: GET /jobs/{id} RBAC ─────────────────────────────────────────────

// rbacFixture builds an auth-enabled server wired around a real cluster.JobStore
// and returns the server plus the token manager so tests can issue JWTs.
func rbacFixture(t *testing.T) (*api.Server, *auth.TokenManager, *cluster.JobStore) {
	t.Helper()
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	cs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(cs)
	srv := api.NewServer(adapter, nil, nil, nil, tm, nil, nil, nil)
	// Note: NO DisableAuth() — we want the real auth path for RBAC tests.
	return srv, tm, cs
}

func TestGetJob_OwnerCanReadOwnJob(t *testing.T) {
	srv, tm, _ := rbacFixture(t)
	tok, _ := tm.GenerateToken(context.Background(), "alice", "node", time.Minute)

	// Submit a job as alice.
	body := `{"id":"rbac-1","command":"echo"}`
	rr := doWithToken(srv, "POST", "/jobs", body, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit as alice: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// alice reads her own job — must succeed.
	rr = doWithToken(srv, "GET", "/jobs/rbac-1", "", tok)
	if rr.Code != http.StatusOK {
		t.Errorf("alice reading own job: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.JobResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.SubmittedBy != "alice" {
		t.Errorf("submitted_by: want alice, got %q", resp.SubmittedBy)
	}
}

func TestGetJob_ForbiddenForNonOwner_Returns403(t *testing.T) {
	srv, tm, _ := rbacFixture(t)
	aliceTok, _ := tm.GenerateToken(context.Background(), "alice", "node", time.Minute)
	bobTok, _ := tm.GenerateToken(context.Background(), "bob", "node", time.Minute)

	// Alice submits.
	rr := doWithToken(srv, "POST", "/jobs", `{"id":"rbac-2","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("alice submit: want 201, got %d", rr.Code)
	}

	// Bob (non-admin) tries to read alice's job — must be forbidden.
	rr = doWithToken(srv, "GET", "/jobs/rbac-2", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("bob reading alice's job: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetJob_AdminCanReadAnyJob(t *testing.T) {
	srv, tm, _ := rbacFixture(t)
	aliceTok, _ := tm.GenerateToken(context.Background(), "alice", "node", time.Minute)
	adminTok, _ := tm.GenerateToken(context.Background(), "root", "admin", time.Minute)

	// Alice submits.
	rr := doWithToken(srv, "POST", "/jobs", `{"id":"rbac-3","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("alice submit: want 201, got %d", rr.Code)
	}

	// Admin reads — must succeed regardless of ownership.
	rr = doWithToken(srv, "GET", "/jobs/rbac-3", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Errorf("admin reading alice's job: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetJob_DevMode_SkipsRBAC(t *testing.T) {
	// Using newServer (which calls DisableAuth), the RBAC check is skipped
	// entirely so any caller can read any job. This is the dev-mode path.
	js := newMockJobStore()
	js.jobs["dev-1"] = &cpb.Job{ID: "dev-1", Command: "echo", SubmittedBy: "alice"}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs/dev-1", "")
	if rr.Code != http.StatusOK {
		t.Errorf("dev mode: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestGetJob_LegacyJobWithoutSubmittedBy_Returns403ForNonAdmin documents
// backward compatibility: an old BadgerDB entry deserializes with an empty
// SubmittedBy, and a non-admin caller's JWT subject will never match the
// empty string, so the safe default is 403.
func TestGetJob_LegacyJobWithoutSubmittedBy_Returns403ForNonAdmin(t *testing.T) {
	srv, tm, cs := rbacFixture(t)
	ctx := context.Background()

	// Simulate a legacy job (no SubmittedBy set).
	if err := cs.Submit(ctx, &cpb.Job{ID: "legacy", Command: "echo"}); err != nil {
		t.Fatalf("legacy submit: %v", err)
	}

	tok, _ := tm.GenerateToken(ctx, "alice", "node", time.Minute)
	rr := doWithToken(srv, "GET", "/jobs/legacy", "", tok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("legacy job, non-admin: want 403, got %d: %s", rr.Code, rr.Body.String())
	}

	// Admin can still read it.
	adminTok, _ := tm.GenerateToken(ctx, "root", "admin", time.Minute)
	rr = doWithToken(srv, "GET", "/jobs/legacy", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Errorf("legacy job, admin: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}
