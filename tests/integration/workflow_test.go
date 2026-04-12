// tests/integration/workflow_test.go
//
// End-to-end tests for workflow/DAG API endpoints.
//
// These tests start the HTTP API server in-process (same pattern as
// helion_run_test.go) and exercise the full workflow lifecycle:
//   POST /workflows   — submit
//   GET  /workflows   — list
//   GET  /workflows/{id} — get with job statuses
//   DELETE /workflows/{id} — cancel

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

// startWorkflowAPIServer starts the HTTP API server with workflow support enabled.
func startWorkflowAPIServer(t *testing.T, jobs *cluster.JobStore, wfs *cluster.WorkflowStore) string {
	t.Helper()
	addr := freePort(t)

	jobsAdapter := api.NewJobStoreAdapter(jobs)
	rateLimiter := ratelimit.NewNodeLimiter()
	srv := api.NewServer(jobsAdapter, nil, nil, nil, nil, rateLimiter, nil, nil)
	srv.DisableAuth()
	srv.SetWorkflowStore(wfs, jobs)

	go func() {
		if err := srv.Serve(addr); err != nil && err != http.ErrServerClosed {
			t.Logf("API server: %v", err)
		}
	}()

	// Wait for the server to be ready.
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
		if err := srv.Shutdown(ctx); err != nil {
			t.Logf("API server shutdown: %v", err)
		}
	})

	return addr
}

// ── TestWorkflow_SubmitAndGet ────────────────────────────────────────────────

func TestWorkflow_SubmitAndGet(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	wfs := cluster.NewWorkflowStore(persister, nil)
	addr := startWorkflowAPIServer(t, jobs, wfs)

	// Submit a workflow via API.
	body, _ := json.Marshal(map[string]interface{}{
		"id":   "wf-e2e-1",
		"name": "build pipeline",
		"jobs": []map[string]interface{}{
			{"name": "build", "command": "echo", "args": []string{"building"}},
			{"name": "test", "command": "echo", "args": []string{"testing"}, "depends_on": []string{"build"}},
			{"name": "deploy", "command": "echo", "args": []string{"deploying"}, "depends_on": []string{"test"}},
		},
	})

	resp, err := http.Post("http://"+addr+"/workflows", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /workflows: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("POST /workflows returned HTTP %d: %s", resp.StatusCode, errResp.Error)
	}

	var wfResp api.WorkflowResponse
	if err := json.NewDecoder(resp.Body).Decode(&wfResp); err != nil {
		t.Fatalf("decode workflow response: %v", err)
	}

	if wfResp.ID != "wf-e2e-1" {
		t.Errorf("workflow id = %q, want %q", wfResp.ID, "wf-e2e-1")
	}
	if wfResp.Status != "running" {
		t.Errorf("workflow status = %q, want %q", wfResp.Status, "running")
	}
	if len(wfResp.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(wfResp.Jobs))
	}

	// Verify individual jobs were created.
	for _, j := range wfResp.Jobs {
		if j.JobID == "" {
			t.Errorf("job %q has no job_id", j.Name)
		}
		if j.JobStatus != "pending" {
			t.Errorf("job %q status = %q, want pending", j.Name, j.JobStatus)
		}
	}

	// GET the workflow back.
	getResp, err := http.Get(fmt.Sprintf("http://%s/workflows/wf-e2e-1", addr))
	if err != nil {
		t.Fatalf("GET /workflows/wf-e2e-1: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET returned HTTP %d, want 200", getResp.StatusCode)
	}

	var getWf api.WorkflowResponse
	json.NewDecoder(getResp.Body).Decode(&getWf)
	if getWf.Status != "running" {
		t.Errorf("GET status = %q, want running", getWf.Status)
	}

	t.Logf("workflow %s submitted and retrieved: status=%s, jobs=%d", getWf.ID, getWf.Status, len(getWf.Jobs))
}

// ── TestWorkflow_ListWorkflows ───────────────────────────────────────────────

func TestWorkflow_ListWorkflows(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	wfs := cluster.NewWorkflowStore(persister, nil)
	addr := startWorkflowAPIServer(t, jobs, wfs)

	// Submit two workflows.
	for _, id := range []string{"wf-list-1", "wf-list-2"} {
		body, _ := json.Marshal(map[string]interface{}{
			"id":   id,
			"name": id,
			"jobs": []map[string]interface{}{
				{"name": "a", "command": "echo"},
			},
		})
		resp, err := http.Post("http://"+addr+"/workflows", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /workflows %s: %v", id, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST /workflows %s returned %d", id, resp.StatusCode)
		}
	}

	// List workflows.
	resp, err := http.Get(fmt.Sprintf("http://%s/workflows?page=1&size=10", addr))
	if err != nil {
		t.Fatalf("GET /workflows: %v", err)
	}
	defer resp.Body.Close()

	var listResp api.WorkflowListResponse
	json.NewDecoder(resp.Body).Decode(&listResp)
	if listResp.Total != 2 {
		t.Errorf("total = %d, want 2", listResp.Total)
	}
	if len(listResp.Workflows) != 2 {
		t.Errorf("workflows count = %d, want 2", len(listResp.Workflows))
	}
}

// ── TestWorkflow_CancelWorkflow ──────────────────────────────────────────────

func TestWorkflow_CancelWorkflow(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	wfs := cluster.NewWorkflowStore(persister, nil)
	addr := startWorkflowAPIServer(t, jobs, wfs)

	// Submit a workflow.
	body, _ := json.Marshal(map[string]interface{}{
		"id":   "wf-cancel-e2e",
		"name": "cancel test",
		"jobs": []map[string]interface{}{
			{"name": "long", "command": "sleep", "args": []string{"3600"}},
		},
	})
	resp, _ := http.Post("http://"+addr+"/workflows", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	// Cancel it.
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://%s/workflows/wf-cancel-e2e", addr), nil)
	cancelResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /workflows/wf-cancel-e2e: %v", err)
	}
	defer cancelResp.Body.Close()

	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE returned HTTP %d, want 200", cancelResp.StatusCode)
	}

	// Verify workflow is cancelled.
	getResp, err := http.Get(fmt.Sprintf("http://%s/workflows/wf-cancel-e2e", addr))
	if err != nil {
		t.Fatalf("GET after cancel: %v", err)
	}
	defer getResp.Body.Close()

	var wf api.WorkflowResponse
	json.NewDecoder(getResp.Body).Decode(&wf)
	if wf.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", wf.Status)
	}

	t.Logf("workflow %s cancelled successfully", wf.ID)
}

// ── TestWorkflow_InvalidDAG ──────────────────────────────────────────────────

func TestWorkflow_InvalidDAG(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	wfs := cluster.NewWorkflowStore(persister, nil)
	addr := startWorkflowAPIServer(t, jobs, wfs)

	cases := []struct {
		name string
		body string
		code int
	}{
		{
			"cycle",
			`{"id":"wf-cycle","name":"cycle","jobs":[{"name":"a","command":"echo","depends_on":["b"]},{"name":"b","command":"echo","depends_on":["a"]}]}`,
			http.StatusBadRequest,
		},
		{
			"unknown dep",
			`{"id":"wf-bad-dep","name":"bad","jobs":[{"name":"a","command":"echo","depends_on":["nonexistent"]}]}`,
			http.StatusBadRequest,
		},
		{
			"empty jobs",
			`{"id":"wf-empty","name":"empty","jobs":[]}`,
			http.StatusBadRequest,
		},
		{
			"missing command",
			`{"id":"wf-nocmd","name":"nocmd","jobs":[{"name":"a"}]}`,
			http.StatusBadRequest,
		},
		{
			"duplicate id",
			`{"id":"wf-dup","name":"first","jobs":[{"name":"a","command":"echo"}]}`,
			http.StatusCreated, // first submit succeeds
		},
		{
			"duplicate id (conflict)",
			`{"id":"wf-dup","name":"second","jobs":[{"name":"a","command":"echo"}]}`,
			http.StatusConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post("http://"+addr+"/workflows", "application/json",
				bytes.NewBufferString(tc.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.code {
				var errResp api.ErrorResponse
				json.NewDecoder(resp.Body).Decode(&errResp)
				t.Errorf("status = %d, want %d (error: %s)", resp.StatusCode, tc.code, errResp.Error)
			}
		})
	}
}

// ── TestWorkflow_NotFound ────────────────────────────────────────────────────

func TestWorkflow_NotFound(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	wfs := cluster.NewWorkflowStore(persister, nil)
	addr := startWorkflowAPIServer(t, jobs, wfs)

	resp, err := http.Get(fmt.Sprintf("http://%s/workflows/does-not-exist", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ── TestWorkflow_DependencyConditions ────────────────────────────────────────

func TestWorkflow_DependencyConditions(t *testing.T) {
	dbPath := t.TempDir()
	persister, err := cluster.NewBadgerJSONPersister(dbPath, 10*time.Second)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}
	t.Cleanup(func() { _ = persister.Close() })

	jobs := cluster.NewJobStore(persister, nil)
	wfs := cluster.NewWorkflowStore(persister, nil)
	addr := startWorkflowAPIServer(t, jobs, wfs)

	// Submit a workflow with on_failure and on_complete conditions.
	body, _ := json.Marshal(map[string]interface{}{
		"id":   "wf-conditions",
		"name": "condition test",
		"jobs": []map[string]interface{}{
			{"name": "main", "command": "echo"},
			{"name": "cleanup", "command": "echo", "depends_on": []string{"main"}, "condition": "on_failure"},
			{"name": "notify", "command": "echo", "depends_on": []string{"main"}, "condition": "on_complete"},
		},
	})

	resp, err := http.Post("http://"+addr+"/workflows", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp api.ErrorResponse
		json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("POST returned %d: %s", resp.StatusCode, errResp.Error)
	}

	var wfResp api.WorkflowResponse
	json.NewDecoder(resp.Body).Decode(&wfResp)

	// Verify conditions were stored correctly.
	for _, j := range wfResp.Jobs {
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

	t.Logf("workflow %s: conditions verified", wfResp.ID)
}
