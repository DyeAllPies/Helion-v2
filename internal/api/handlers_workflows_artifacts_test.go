package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
)

// TestWorkflowAPI_Submit_WithMLFields exercises the happy path: a
// workflow whose job templates declare working_dir, inputs, outputs,
// and node_selector is accepted and materialised onto real jobs.
func TestWorkflowAPI_Submit_WithMLFields(t *testing.T) {
	srv := newWorkflowServer()
	body := `{
		"id": "wf-ml",
		"name": "ml",
		"jobs": [
			{
				"name": "preprocess",
				"command": "echo",
				"working_dir": "pre",
				"outputs": [{"name": "TRAIN", "local_path": "out/train.parquet"}],
				"node_selector": {"role": "cpu"}
			},
			{
				"name": "train",
				"command": "echo",
				"depends_on": ["preprocess"],
				"inputs":  [{"name": "TRAIN_DATA", "uri": "s3://b/x", "local_path": "in/train.parquet"}],
				"outputs": [{"name": "MODEL", "local_path": "out/model.pt"}],
				"node_selector": {"gpu": "a100"}
			}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp api.WorkflowResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp.Jobs) != 2 {
		t.Fatalf("expected 2 jobs: %+v", resp.Jobs)
	}
}

// The validator rules applied to standalone-submit bindings must apply
// to workflow-job bindings too — the handler plumbs them through the
// shared validators. A small sample is enough because the per-rule
// coverage already lives in handlers_jobs_step2_test.go.

func TestWorkflowAPI_Submit_InvalidInputName(t *testing.T) {
	srv := newWorkflowServer()
	body := `{"id":"wf","name":"","jobs":[
		{"name":"a","command":"echo","inputs":[{"name":"lowercase","uri":"s3://b/x","local_path":"in/x"}]}
	]}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "inputs[0].name") {
		t.Fatalf("expected inputs[0].name error, got: %s", rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_OutputWithURIRejected(t *testing.T) {
	srv := newWorkflowServer()
	body := `{"id":"wf","name":"","jobs":[
		{"name":"a","command":"echo","outputs":[{"name":"M","uri":"s3://b/already","local_path":"out/m"}]}
	]}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "must be empty on submit") {
		t.Fatalf("unexpected: %s", rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_TraversalInLocalPath(t *testing.T) {
	srv := newWorkflowServer()
	body := `{"id":"wf","name":"","jobs":[
		{"name":"a","command":"echo","inputs":[{"name":"X","uri":"s3://b/x","local_path":"../escape"}]}
	]}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_InvalidWorkingDir(t *testing.T) {
	srv := newWorkflowServer()
	body := `{"id":"wf","name":"","jobs":[
		{"name":"a","command":"echo","working_dir":"has\u0000nul"}
	]}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
}
