// internal/api/handlers_workflows_test.go
//
// Unit tests for workflow API handlers: POST/GET/DELETE /workflows.

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
)

func newWorkflowServer() *api.Server {
	jobs := newMockJobStore()
	srv := newServer(jobs, nil, nil)

	p := cluster.NewMemWorkflowPersister()
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ws := cluster.NewWorkflowStore(p, nil)
	srv.SetWorkflowStore(ws, js)
	return srv
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
