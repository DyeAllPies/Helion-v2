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

// ── Feature 24 — POST /jobs?dry_run=true ──────────────────────────────────────
//
// Invariants under test (see docs/planned-features/24-dry-run-preflight.md):
//
//   1. Validators still run. A body that would be rejected on the real
//      path is still rejected on the dry-run path — we do NOT let
//      dry-run become a "skip validation" probe oracle.
//   2. jobs.Submit() is never called. Mock store records zero writes.
//   3. A DISTINCT audit event type (`job_dry_run`) is emitted so reviewers
//      can tell probes apart from real submissions in the audit log.
//   4. Response is 200 OK (not 201 Created — nothing was created) with
//      a top-level `"dry_run": true` boolean and the same keys the real
//      201 response would carry.
//   5. An unparseable `?dry_run=maybe` returns 400 — silent fallback to
//      the real path would turn a typo into an unintended submission.

func TestSubmitJob_DryRun_Returns200WithFlag(t *testing.T) {
	js := newMockJobStore()
	srv := newServer(js, nil, nil)

	body := `{"id":"dry-job-1","command":"echo","args":["hi"]}`
	rr := do(srv, "POST", "/jobs?dry_run=true", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Body carries `dry_run: true` plus the would-be 201 response shape.
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if dr, ok := resp["dry_run"].(bool); !ok || !dr {
		t.Errorf("response missing `dry_run: true`: %#v", resp)
	}
	if id, ok := resp["id"].(string); !ok || id != "dry-job-1" {
		t.Errorf("response missing id=dry-job-1: %#v", resp)
	}
	if _, ok := resp["command"]; !ok {
		t.Errorf("response missing command key: %#v", resp)
	}
	if _, ok := resp["status"]; !ok {
		t.Errorf("response missing status key: %#v", resp)
	}
}

func TestSubmitJob_DryRun_DoesNotPersist(t *testing.T) {
	js := newMockJobStore()
	srv := newServer(js, nil, nil)

	body := `{"id":"ghost-job","command":"echo"}`
	rr := do(srv, "POST", "/jobs?dry_run=true", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Invariant: mock Submit never called — store must be empty.
	if len(js.jobs) != 0 {
		t.Errorf("dry-run must NOT persist, but %d jobs were stored", len(js.jobs))
	}
}

func TestSubmitJob_DryRun_EmitsDryRunAuditEvent(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	body := `{"id":"audit-dry","command":"echo"}`
	rr := do(srv, "POST", "/jobs?dry_run=true", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, err := store.Scan(context.Background(), "audit:", 0)
	if err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d", len(entries))
	}
	var ev audit.Event
	if err := json.Unmarshal(entries[0], &ev); err != nil {
		t.Fatalf("unmarshal audit event: %v", err)
	}
	if ev.Type != audit.EventJobDryRun {
		t.Errorf("audit type: want %q, got %q", audit.EventJobDryRun, ev.Type)
	}
	if ev.Type == audit.EventJobSubmit {
		t.Errorf("dry-run must NOT emit %q — reviewers can't filter probes otherwise", audit.EventJobSubmit)
	}
	if jid, _ := ev.Details["job_id"].(string); jid != "audit-dry" {
		t.Errorf("audit detail job_id: want audit-dry, got %v", ev.Details["job_id"])
	}
}

func TestSubmitJob_DryRun_ValidatorsStillRun_MissingIDRejected(t *testing.T) {
	// Regression guard: dry_run must NOT become a validation-skip probe.
	// A body missing required fields must still 400 on the dry-run path.
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"command":"echo"}` // missing id
	rr := do(srv, "POST", "/jobs?dry_run=true", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("dry-run missing id: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_DryRun_ValidatorsStillRun_ShellMetaRejected(t *testing.T) {
	// Regression guard: the command-shape validator (AUDIT C4/C5) must
	// still fire under dry_run. Otherwise dry_run could be used to probe
	// which shell-meta characters are treated as valid.
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"shell-dry","command":"echo; rm -rf /"}`
	rr := do(srv, "POST", "/jobs?dry_run=true", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("dry-run shell meta: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_DryRunInvalidValue_Returns400(t *testing.T) {
	// Typo guard: `?dry_run=yees` must 400, not silently submit.
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"typo-job","command":"echo"}`
	rr := do(srv, "POST", "/jobs?dry_run=yees", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on unrecognised dry_run value, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "dry_run") {
		t.Errorf("error body should mention dry_run: %s", rr.Body.String())
	}
}

func TestSubmitJob_DryRunFalsy_HitsRealPath(t *testing.T) {
	// `?dry_run=false` and `?dry_run=0` must route through the real
	// submit path (201 Created, persisted).
	for _, val := range []string{"false", "0", "no", ""} {
		t.Run("dry_run="+val, func(t *testing.T) {
			js := newMockJobStore()
			srv := newServer(js, nil, nil)
			body := `{"id":"real-` + val + `","command":"echo"}`
			path := "/jobs?dry_run=" + val
			rr := do(srv, "POST", path, body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("falsy dry_run=%q: want 201, got %d: %s", val, rr.Code, rr.Body.String())
			}
			if len(js.jobs) != 1 {
				t.Errorf("falsy dry_run=%q: expected 1 persisted job, got %d", val, len(js.jobs))
			}
		})
	}
}

func TestSubmitJob_DryRun_ResponseKeysSupersetOfReal(t *testing.T) {
	// The dry-run response must carry the same keys as the real 201
	// response, plus one extra `dry_run` boolean. A client should be
	// able to point the same decoder at either shape.
	srv := newServer(newMockJobStore(), nil, nil)
	realBody := `{"id":"shape-real","command":"echo"}`
	rrReal := do(srv, "POST", "/jobs", realBody)
	if rrReal.Code != http.StatusCreated {
		t.Fatalf("real submit: want 201, got %d", rrReal.Code)
	}
	var real map[string]interface{}
	if err := json.Unmarshal(rrReal.Body.Bytes(), &real); err != nil {
		t.Fatalf("decode real: %v", err)
	}

	srv2 := newServer(newMockJobStore(), nil, nil)
	dryBody := `{"id":"shape-dry","command":"echo"}`
	rrDry := do(srv2, "POST", "/jobs?dry_run=1", dryBody)
	if rrDry.Code != http.StatusOK {
		t.Fatalf("dry submit: want 200, got %d", rrDry.Code)
	}
	var dry map[string]interface{}
	if err := json.Unmarshal(rrDry.Body.Bytes(), &dry); err != nil {
		t.Fatalf("decode dry: %v", err)
	}

	for k := range real {
		if _, ok := dry[k]; !ok {
			t.Errorf("dry-run response missing key %q that real response has", k)
		}
	}
	if _, ok := dry["dry_run"]; !ok {
		t.Errorf("dry-run response missing `dry_run` flag: %#v", dry)
	}
}

// ── Feature 25 — env-var denylist on POST /jobs ──────────────────────────────
//
// Invariants (see docs/planned-features/implemented/25-env-var-denylist.md):
//
//   1. LD_PRELOAD and every other loader-injection env var on the
//      denylist is rejected at 400 on the real path.
//   2. The same rejection fires on the dry-run path — dry-run is not
//      a "skip validation" probe oracle.
//   3. Rejection emits an env_denylist_reject audit event so reviewers
//      can spot attempted probes.
//   4. Non-denylisted env vars (PYTHONPATH, HELION_TOKEN, …) remain
//      accepted. No over-matching regression.
//   5. The error body names the denylist reason so an operator can
//      diagnose without grepping source.
//   6. Admin role is subject to the denylist too — no bypass.

func TestSubmitJob_LDPRELOAD_Rejected(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"evil","command":"echo","env":{"LD_PRELOAD":"/tmp/evil.so"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "dynamic-loader") {
		t.Errorf("error body must mention dynamic-loader for operator diagnosis: %s", rr.Body.String())
	}
}

func TestSubmitJob_DenylistedKeys_AllRejected(t *testing.T) {
	// One test per denylist entry so a future regression surfaces the
	// exact key that slipped through.
	cases := []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "LD_DEBUG",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH",
		"GCONV_PATH", "GIO_EXTRA_MODULES", "HOSTALIASES", "NLSPATH", "RES_OPTIONS",
	}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			srv := newServer(newMockJobStore(), nil, nil)
			body := fmt.Sprintf(`{"id":"j-%s","command":"echo","env":{%q:"x"}}`, strings.ToLower(key), key)
			rr := do(srv, "POST", "/jobs", body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d: %s", key, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSubmitJob_DenylistUnderDryRun_StillRejected(t *testing.T) {
	// Regression guard: dry-run must not bypass the denylist.
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"dry-evil","command":"echo","env":{"LD_PRELOAD":"/tmp/evil.so"}}`
	rr := do(srv, "POST", "/jobs?dry_run=true", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("dry-run denylist: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_Denylist_EmitsAuditEvent(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	body := `{"id":"audit-evil","command":"echo","env":{"LD_PRELOAD":"/tmp/evil.so"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	entries, err := store.Scan(context.Background(), "audit:", 0)
	if err != nil {
		t.Fatalf("audit scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	var ev audit.Event
	if err := json.Unmarshal(entries[0], &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != audit.EventEnvDenylistReject {
		t.Errorf("audit type: want %q, got %q", audit.EventEnvDenylistReject, ev.Type)
	}
	if bk, _ := ev.Details["blocked_key"].(string); bk != "LD_PRELOAD" {
		t.Errorf("audit blocked_key: want LD_PRELOAD, got %v", ev.Details["blocked_key"])
	}
	// Invariant: denylist rejection must NOT be persisted.
	if len(js.jobs) != 0 {
		t.Errorf("denylist reject must not persist; got %d jobs", len(js.jobs))
	}
}

func TestSubmitJob_NonDenylistedEnv_Accepted(t *testing.T) {
	// Regression guard: a common ML env (PYTHONPATH + HELION_TOKEN)
	// must still go through after the denylist wiring.
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"ok","command":"echo","env":{"PYTHONPATH":"/app","HELION_TOKEN":"abc","CUDA_VISIBLE_DEVICES":"0"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Errorf("non-denylisted env: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── Feature 25 — per-node overrides ──────────────────────────────────────────

func TestSubmitJob_Override_MatchingSelector_Accepted(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	ex, err := api.ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	if err != nil {
		t.Fatalf("parse exceptions: %v", err)
	}
	srv.SetEnvDenylistExceptions(ex)

	body := `{"id":"gpu-job","command":"echo","env":{"LD_LIBRARY_PATH":"/opt/cuda/lib64"},"node_selector":{"role":"gpu"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201 with override, got %d: %s", rr.Code, rr.Body.String())
	}

	// Must emit env_denylist_override AND job_submit — one per key, plus the normal submit event.
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	types := make(map[string]int)
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		types[ev.Type]++
	}
	if types[audit.EventEnvDenylistOverride] != 1 {
		t.Errorf("want 1 env_denylist_override audit event, got %d (all: %v)", types[audit.EventEnvDenylistOverride], types)
	}
	if types[audit.EventJobSubmit] != 1 {
		t.Errorf("want 1 job_submit audit event (real path still fires), got %d", types[audit.EventJobSubmit])
	}
}

func TestSubmitJob_Override_WrongSelector_Rejected(t *testing.T) {
	// Rule says role=gpu allows LD_LIBRARY_PATH. A job with role=cpu
	// carrying LD_LIBRARY_PATH must still be rejected.
	srv := newServer(newMockJobStore(), nil, nil)
	ex, _ := api.ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	srv.SetEnvDenylistExceptions(ex)

	body := `{"id":"cpu-trying","command":"echo","env":{"LD_LIBRARY_PATH":"/cuda"},"node_selector":{"role":"cpu"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("wrong selector: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_Override_NoSelector_Rejected(t *testing.T) {
	// No selector at all — denylist applies absolute.
	srv := newServer(newMockJobStore(), nil, nil)
	ex, _ := api.ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	srv.SetEnvDenylistExceptions(ex)

	body := `{"id":"nosel","command":"echo","env":{"LD_LIBRARY_PATH":"/cuda"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no selector: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_Override_DoesNotExtendToUnlistedKeys(t *testing.T) {
	// Rule allows only LD_LIBRARY_PATH; LD_PRELOAD must still reject
	// even on matching selector.
	srv := newServer(newMockJobStore(), nil, nil)
	ex, _ := api.ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	srv.SetEnvDenylistExceptions(ex)

	body := `{"id":"mix","command":"echo","env":{"LD_PRELOAD":"/tmp/evil"},"node_selector":{"role":"gpu"}}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unlisted key on matching selector: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── Feature 25 — artifact-staging safety (deferred: symlinks at dangerous libs) ──

func TestSubmitJob_ArtifactURI_SystemPath_Rejected(t *testing.T) {
	cases := []string{
		"file:///lib/libc.so.6",
		"file:///usr/lib/libm.so.6",
		"file:///proc/self/environ",
		"file:///etc/passwd",
		"file:///var/run/secrets/kubernetes.io/serviceaccount/token",
		"file://host/lib64/ld-linux-x86-64.so.2",
	}
	for _, uri := range cases {
		t.Run(uri, func(t *testing.T) {
			srv := newServer(newMockJobStore(), nil, nil)
			body := fmt.Sprintf(`{"id":"a","command":"echo","inputs":[{"name":"IN","local_path":"d/in","uri":%q}]}`, uri)
			rr := do(srv, "POST", "/jobs", body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d: %s", uri, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "feature 25") {
				t.Errorf("%s: err should cite feature 25: %s", uri, rr.Body.String())
			}
		})
	}
}

func TestSubmitJob_ArtifactURI_SafePath_Accepted(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"safe","command":"echo","inputs":[{"name":"DATA","local_path":"inputs/train.parquet","uri":"file:///mnt/data/train.parquet"}]}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Errorf("safe file URI: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_ArtifactLocalPath_DangerousLibBasename_Rejected(t *testing.T) {
	cases := []string{
		"libc.so",
		"libc.so.6",
		"ld-linux-x86-64.so.2",
		"libpthread.so.0",
	}
	for _, base := range cases {
		t.Run(base, func(t *testing.T) {
			srv := newServer(newMockJobStore(), nil, nil)
			body := fmt.Sprintf(`{"id":"a","command":"echo","inputs":[{"name":"IN","local_path":"stage/%s","uri":"s3://bucket/k"}]}`, base)
			rr := do(srv, "POST", "/jobs", body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("basename %q: want 400, got %d: %s", base, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "feature 25") {
				t.Errorf("basename %q: err should cite feature 25: %s", base, rr.Body.String())
			}
		})
	}
}

func TestSubmitJob_ArtifactLocalPath_BenignLibBasename_Accepted(t *testing.T) {
	// Common ML lib names must NOT be caught — libcudart, libtorch,
	// libcuda, etc. are legitimately shipped alongside jobs.
	cases := []string{
		"libcudart.so.11.0",
		"libtorch.so",
		"libcuda.so.1",
	}
	for _, base := range cases {
		t.Run(base, func(t *testing.T) {
			srv := newServer(newMockJobStore(), nil, nil)
			body := fmt.Sprintf(`{"id":"a-%s","command":"echo","inputs":[{"name":"LIB","local_path":"stage/%s","uri":"s3://bucket/k"}]}`, strings.ReplaceAll(base, ".", "-"), base)
			rr := do(srv, "POST", "/jobs", body)
			if rr.Code != http.StatusCreated {
				t.Errorf("legitimate ML lib %q: want 201, got %d: %s", base, rr.Code, rr.Body.String())
			}
		})
	}
}
