// internal/api/handlers_workflows_test.go
//
// Unit tests for workflow API handlers: POST/GET/DELETE /workflows.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
)

func newWorkflowServer() *api.Server {
	jobs := newMockJobStore()
	srv := newServer(jobs, nil, nil)

	p := cluster.NewMemWorkflowPersister()
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ws := cluster.NewWorkflowStore(p, nil)
	srv.SetWorkflowStore(ws, js)

	// Wire event bus (covers SetEventBus).
	bus := events.NewBus(10, nil)
	srv.SetEventBus(bus)

	return srv
}

// newWorkflowServerWithAudit is a variant for feature 24 dry-run tests:
// returns both the server AND the underlying audit store so a test can
// verify which event types were emitted.
func newWorkflowServerWithAudit() (*api.Server, *cluster.WorkflowStore, *inMemoryAuditStore) {
	jobs := newMockJobStore()
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(jobs, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	p := cluster.NewMemWorkflowPersister()
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ws := cluster.NewWorkflowStore(p, nil)
	srv.SetWorkflowStore(ws, js)

	bus := events.NewBus(10, nil)
	srv.SetEventBus(bus)

	return srv, ws, store
}

// ── POST /workflows ─────────────────────────────────────────────────────────

func TestWorkflowAPI_Submit_Valid(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-1",
		"name": "build pipeline",
		"jobs": [
			{"name": "build", "command": "echo", "args": ["building"]},
			{"name": "test", "command": "echo", "depends_on": ["build"]}
		]
	}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /workflows = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	var resp api.WorkflowResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "wf-1" {
		t.Errorf("id = %q, want wf-1", resp.ID)
	}
	if resp.Status != "running" {
		t.Errorf("status = %q, want running", resp.Status)
	}
	if len(resp.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(resp.Jobs))
	}
	for _, j := range resp.Jobs {
		if j.JobID == "" {
			t.Errorf("job %q has empty job_id", j.Name)
		}
	}
}

func TestWorkflowAPI_Submit_MissingID(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{"name":"x","jobs":[{"name":"a","command":"echo"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_EmptyJobs(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{"id":"wf-empty","jobs":[]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_MissingCommand(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{"id":"wf-nocmd","jobs":[{"name":"a"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_CycleRejected(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-cycle",
		"jobs": [
			{"name": "a", "command": "echo", "depends_on": ["b"]},
			{"name": "b", "command": "echo", "depends_on": ["a"]}
		]
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_Duplicate(t *testing.T) {
	srv := newWorkflowServer()
	body := `{"id":"wf-dup","jobs":[{"name":"a","command":"echo"}]}`
	rr1 := do(srv, "POST", "/workflows", body)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first submit = %d, want 201", rr1.Code)
	}
	rr2 := do(srv, "POST", "/workflows", body)
	if rr2.Code != http.StatusConflict {
		t.Errorf("second submit = %d, want 409", rr2.Code)
	}
}

func TestWorkflowAPI_Submit_ForbiddenCommand(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{"id":"wf-bad","jobs":[{"name":"a","command":"/bin/sh"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_TooManyJobs(t *testing.T) {
	srv := newWorkflowServer()
	// Build a request with 101 jobs.
	jobs := make([]map[string]string, 101)
	for i := range jobs {
		jobs[i] = map[string]string{"name": "j" + string(rune('a'+i%26)) + string(rune('0'+i/26)), "command": "echo"}
	}
	// This is impractical to construct with unique names, so just test the limit.
	rr := do(srv, "POST", "/workflows", `{"id":"wf-big","jobs":[`+
		repeatJSON(`{"name":"j%d","command":"echo"}`, 101)+`]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for >100 jobs", rr.Code)
	}
}

func TestWorkflowAPI_Submit_InvalidBody(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{invalid json`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_Conditions(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-cond",
		"jobs": [
			{"name": "main", "command": "echo"},
			{"name": "cleanup", "command": "echo", "depends_on": ["main"], "condition": "on_failure"},
			{"name": "notify", "command": "echo", "depends_on": ["main"], "condition": "on_complete"}
		]
	}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	var resp api.WorkflowResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	for _, j := range resp.Jobs {
		switch j.Name {
		case "main":
			if j.Condition != "on_success" {
				t.Errorf("main condition = %q, want on_success", j.Condition)
			}
		case "cleanup":
			if j.Condition != "on_failure" {
				t.Errorf("cleanup condition = %q, want on_failure", j.Condition)
			}
		case "notify":
			if j.Condition != "on_complete" {
				t.Errorf("notify condition = %q, want on_complete", j.Condition)
			}
		}
	}
}

// ── GET /workflows/{id} ─────────────────────────────────────────────────────

func TestWorkflowAPI_Get_Exists(t *testing.T) {
	srv := newWorkflowServer()
	do(srv, "POST", "/workflows", `{"id":"wf-get","jobs":[{"name":"a","command":"echo"}]}`)

	rr := do(srv, "GET", "/workflows/wf-get", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rr.Code)
	}

	var resp api.WorkflowResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.ID != "wf-get" {
		t.Errorf("id = %q, want wf-get", resp.ID)
	}
}

func TestWorkflowAPI_Get_NotFound(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "GET", "/workflows/nonexistent", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ── GET /workflows ──────────────────────────────────────────────────────────

func TestWorkflowAPI_List(t *testing.T) {
	srv := newWorkflowServer()
	do(srv, "POST", "/workflows", `{"id":"wf-l1","jobs":[{"name":"a","command":"echo"}]}`)
	do(srv, "POST", "/workflows", `{"id":"wf-l2","jobs":[{"name":"a","command":"echo"}]}`)

	rr := do(srv, "GET", "/workflows?page=1&size=10", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rr.Code)
	}

	var resp api.WorkflowListResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
}

func TestWorkflowAPI_List_Pagination(t *testing.T) {
	srv := newWorkflowServer()
	for i := 0; i < 5; i++ {
		do(srv, "POST", "/workflows", `{"id":"wf-p`+string(rune('0'+i))+`","jobs":[{"name":"a","command":"echo"}]}`)
	}

	rr := do(srv, "GET", "/workflows?page=1&size=2", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rr.Code)
	}
	var resp api.WorkflowListResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp.Workflows) != 2 {
		t.Errorf("page size = %d, want 2", len(resp.Workflows))
	}
	if resp.Total != 5 {
		t.Errorf("total = %d, want 5", resp.Total)
	}
}

func TestWorkflowAPI_List_InvalidPage(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "GET", "/workflows?page=-1", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── DELETE /workflows/{id} ──────────────────────────────────────────────────

func TestWorkflowAPI_Cancel(t *testing.T) {
	srv := newWorkflowServer()
	do(srv, "POST", "/workflows", `{"id":"wf-del","jobs":[{"name":"a","command":"echo"}]}`)

	rr := do(srv, "DELETE", "/workflows/wf-del", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify it's cancelled.
	rr2 := do(srv, "GET", "/workflows/wf-del", "")
	var resp api.WorkflowResponse
	_ = json.NewDecoder(rr2.Body).Decode(&resp)
	if resp.Status != "cancelled" {
		t.Errorf("status after cancel = %q, want cancelled", resp.Status)
	}
}

func TestWorkflowAPI_Cancel_NotFound(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "DELETE", "/workflows/nope", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestWorkflowAPI_Cancel_AlreadyTerminal(t *testing.T) {
	srv := newWorkflowServer()
	do(srv, "POST", "/workflows", `{"id":"wf-term","jobs":[{"name":"a","command":"echo"}]}`)
	do(srv, "DELETE", "/workflows/wf-term", "")

	rr := do(srv, "DELETE", "/workflows/wf-term", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestWorkflowAPI_List_InvalidSize(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "GET", "/workflows?size=999", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for size > 100", rr.Code)
	}
}

func TestWorkflowAPI_Submit_UnknownDep(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-badref",
		"jobs": [
			{"name": "a", "command": "echo", "depends_on": ["nonexistent"]}
		]
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_DuplicateJobName(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-dupname",
		"jobs": [
			{"name": "a", "command": "echo"},
			{"name": "a", "command": "echo"}
		]
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_SelfDep(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-self",
		"jobs": [
			{"name": "a", "command": "echo", "depends_on": ["a"]}
		]
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowAPI_Submit_BadTimeout(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-timeout",
		"jobs": [
			{"name": "a", "command": "echo", "timeout_seconds": 99999}
		]
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for excessive timeout", rr.Code)
	}
}

func TestWorkflowAPI_Submit_EmptyJobName(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "POST", "/workflows", `{
		"id": "wf-noname",
		"jobs": [
			{"name": "", "command": "echo"}
		]
	}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// repeatJSON builds a comma-separated list of n JSON objects with %d replaced by index.
func repeatJSON(template string, n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		s := template
		// Replace %d with index
		result := ""
		for j := 0; j < len(s); j++ {
			if j+1 < len(s) && s[j] == '%' && s[j+1] == 'd' {
				result += intToStr(i)
				j++ // skip 'd'
			} else {
				result += string(s[j])
			}
		}
		parts = append(parts, result)
	}
	return joinStrings(parts, ",")
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}

// ── Feature 24 — POST /workflows?dry_run=true ────────────────────────────────
//
// See the jobs dry-run tests above for the invariant catalogue. Workflow
// dry-run adds one: the DAG validator (cycles, unknown deps) must STILL
// fire in dry-run mode — dry_run is not a "skip DAG check" probe oracle.

func TestWorkflowAPI_DryRun_Returns200AndDoesNotPersist(t *testing.T) {
	srv, ws, _ := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-dry-happy",
		"name": "dry happy",
		"jobs": [
			{"name": "build", "command": "echo"},
			{"name": "test",  "command": "echo", "depends_on": ["build"]}
		]
	}`
	rr := do(srv, "POST", "/workflows?dry_run=true", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if dr, ok := resp["dry_run"].(bool); !ok || !dr {
		t.Errorf("response missing `dry_run: true`: %#v", resp)
	}
	if id, _ := resp["id"].(string); id != "wf-dry-happy" {
		t.Errorf("response id: want wf-dry-happy, got %v", resp["id"])
	}

	// Invariant: the workflow must NOT exist in the store.
	if _, err := ws.Get("wf-dry-happy"); err == nil {
		t.Error("dry-run must not persist the workflow")
	}
}

func TestWorkflowAPI_DryRun_CycleStillRejected(t *testing.T) {
	// Regression guard: dry_run must NOT skip DAG validation.
	srv, _, _ := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-dry-cycle",
		"jobs": [
			{"name": "a", "command": "echo", "depends_on": ["b"]},
			{"name": "b", "command": "echo", "depends_on": ["a"]}
		]
	}`
	rr := do(srv, "POST", "/workflows?dry_run=true", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("dry-run with cycle: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWorkflowAPI_DryRun_UnknownDepStillRejected(t *testing.T) {
	srv, _, _ := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-dry-unknowndep",
		"jobs": [
			{"name": "a", "command": "echo", "depends_on": ["does-not-exist"]}
		]
	}`
	rr := do(srv, "POST", "/workflows?dry_run=true", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("dry-run unknown dep: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWorkflowAPI_DryRun_EmitsDistinctAuditEvent(t *testing.T) {
	srv, _, store := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-dry-audit",
		"jobs": [
			{"name": "a", "command": "echo"}
		]
	}`
	rr := do(srv, "POST", "/workflows?dry_run=true", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, err := store.Scan(context.Background(), "audit:", 0)
	if err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	var ev audit.Event
	if err := json.Unmarshal(entries[0], &ev); err != nil {
		t.Fatalf("unmarshal audit event: %v", err)
	}
	if ev.Type != audit.EventWorkflowDryRun {
		t.Errorf("audit type: want %q, got %q", audit.EventWorkflowDryRun, ev.Type)
	}
	if ev.Type == audit.EventWorkflowSubmit {
		t.Errorf("dry-run must NOT emit %q — distinct type invariant broken", audit.EventWorkflowSubmit)
	}
}

func TestWorkflowAPI_DryRunInvalidValue_Returns400(t *testing.T) {
	srv, _, _ := newWorkflowServerWithAudit()
	rr := do(srv, "POST", "/workflows?dry_run=maybe",
		`{"id":"wf-typo","jobs":[{"name":"a","command":"echo"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on unrecognised dry_run value, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "dry_run") {
		t.Errorf("error body should mention dry_run: %s", rr.Body.String())
	}
}

// ── Feature 25 — denylist on workflow child jobs ─────────────────────────────
//
// Invariants:
//
//   1. A child job whose env contains a denylisted key fails the
//      whole workflow submit at 400 (not just skipping that job).
//   2. The error body names the offending child job so the operator
//      can find it.
//   3. The workflow is NOT persisted.
//   4. audit emits env_denylist_reject with workflow_id + job_name.
//   5. Per-node override works per-child: a child pinned to role=gpu
//      can legitimately set LD_LIBRARY_PATH while a sibling cannot.

func TestWorkflowAPI_DenylistedChild_Rejected(t *testing.T) {
	srv, ws, _ := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-evil",
		"jobs": [
			{"name": "a", "command": "echo"},
			{"name": "b", "command": "echo", "env": {"LD_PRELOAD": "/tmp/evil.so"}, "depends_on": ["a"]}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("workflow denylist: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	// The JSON-encoded error body escapes quotes around the job name
	// ("job \"b\":"), so match the escaped form.
	if !strings.Contains(rr.Body.String(), `job \"b\"`) {
		t.Errorf("error should name child job 'b': %s", rr.Body.String())
	}
	// Invariant: the workflow must not be persisted.
	if _, err := ws.Get("wf-evil"); err == nil {
		t.Error("denylisted workflow must not be persisted")
	}
}

func TestWorkflowAPI_DenylistedChild_EmitsAuditEvent(t *testing.T) {
	srv, _, store := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-audit-evil",
		"jobs": [
			{"name": "main", "command": "echo", "env": {"LD_PRELOAD": "/tmp/x"}}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400: %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var found bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventEnvDenylistReject {
			found = true
			if wid, _ := ev.Details["workflow_id"].(string); wid != "wf-audit-evil" {
				t.Errorf("workflow_id: want wf-audit-evil, got %v", ev.Details["workflow_id"])
			}
			if jn, _ := ev.Details["job_name"].(string); jn != "main" {
				t.Errorf("job_name: want main, got %v", ev.Details["job_name"])
			}
		}
	}
	if !found {
		t.Error("expected env_denylist_reject audit event")
	}
}

func TestWorkflowAPI_DenylistUnderDryRun_StillRejected(t *testing.T) {
	srv, _, _ := newWorkflowServerWithAudit()
	body := `{
		"id": "wf-dry-evil",
		"jobs": [{"name": "main", "command": "echo", "env": {"LD_PRELOAD": "/tmp/x"}}]
	}`
	rr := do(srv, "POST", "/workflows?dry_run=true", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("dry-run denylist: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWorkflowAPI_PerChildOverride(t *testing.T) {
	// One child pinned to role=gpu gets LD_LIBRARY_PATH via the
	// override; a sibling without that selector carrying the same
	// env would be rejected. Here we only verify the positive case
	// — the negative case is covered by TestSubmitJob_Override_*.
	jobs := newMockJobStore()
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(jobs, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	p := cluster.NewMemWorkflowPersister()
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ws := cluster.NewWorkflowStore(p, nil)
	srv.SetWorkflowStore(ws, js)
	srv.SetEventBus(events.NewBus(16, nil))

	ex, err := api.ParseEnvDenylistExceptions("role=gpu:LD_LIBRARY_PATH")
	if err != nil {
		t.Fatalf("parse exceptions: %v", err)
	}
	srv.SetEnvDenylistExceptions(ex)

	body := `{
		"id": "wf-mix",
		"jobs": [
			{"name": "prep",  "command": "echo"},
			{"name": "train", "command": "echo", "env": {"LD_LIBRARY_PATH": "/opt/cuda/lib64"}, "node_selector": {"role": "gpu"}, "depends_on": ["prep"]}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("override on pinned child: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var overrideCount int
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventEnvDenylistOverride {
			overrideCount++
		}
	}
	if overrideCount != 1 {
		t.Errorf("want 1 env_denylist_override audit event, got %d", overrideCount)
	}
}
