package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// End-to-end workflow submit with a From reference: accepted and
// materialised. The DAG validator signs off before submission; the
// resolver hook in dispatch.go runs later at dispatch time (not
// exercised by the HTTP layer).
func TestWorkflowAPI_Submit_WithFromReference(t *testing.T) {
	srv := newWorkflowServer()
	body := `{
		"id": "wf-step3",
		"name": "ml",
		"jobs": [
			{
				"name": "preprocess",
				"command": "echo",
				"outputs": [{"name": "TRAIN", "local_path": "out/train.parquet"}]
			},
			{
				"name": "train",
				"command": "echo",
				"depends_on": ["preprocess"],
				"inputs":  [{"name": "DATA", "from": "preprocess.TRAIN", "local_path": "in/train.parquet"}],
				"outputs": [{"name": "MODEL", "local_path": "out/model.pt"}]
			}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST = %d, body: %s", rr.Code, rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_FromUnknownUpstream(t *testing.T) {
	srv := newWorkflowServer()
	body := `{
		"id": "wf-bad",
		"jobs": [
			{"name": "train", "command": "echo",
			 "inputs": [{"name": "D", "from": "ghost.OUT", "local_path": "in/d"}]}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "unknown upstream") {
		t.Fatalf("expected unknown-upstream message: %s", rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_FromNonAncestor(t *testing.T) {
	srv := newWorkflowServer()
	// `train` references `preprocess.TRAIN` but forgets to list it in
	// depends_on. The DAG validator rejects: scheduling order
	// undefined, upstream may not be done when downstream runs.
	body := `{
		"id": "wf-no-dep",
		"jobs": [
			{"name": "preprocess", "command": "echo",
			 "outputs": [{"name": "TRAIN", "local_path": "out/t"}]},
			{"name": "train", "command": "echo",
			 "inputs": [{"name": "D", "from": "preprocess.TRAIN", "local_path": "in/t"}]}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "non-ancestor") {
		t.Fatalf("expected non-ancestor message: %s", rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_FromUnknownOutput(t *testing.T) {
	srv := newWorkflowServer()
	body := `{
		"id": "wf-bad-out",
		"jobs": [
			{"name": "preprocess", "command": "echo",
			 "outputs": [{"name": "TRAIN", "local_path": "out/t"}]},
			{"name": "train", "command": "echo", "depends_on": ["preprocess"],
			 "inputs": [{"name": "D", "from": "preprocess.VALIDATION", "local_path": "in/v"}]}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "unknown output") {
		t.Fatalf("expected unknown-output message: %s", rr.Body.String())
	}
}

func TestWorkflowAPI_Submit_MalformedFromShape(t *testing.T) {
	srv := newWorkflowServer()
	// Missing dot — the API validator catches this before the DAG
	// validator even sees it.
	body := `{
		"id": "wf-shape",
		"jobs": [
			{"name": "preprocess", "command": "echo",
			 "outputs": [{"name": "TRAIN", "local_path": "out/t"}]},
			{"name": "train", "command": "echo", "depends_on": ["preprocess"],
			 "inputs": [{"name": "D", "from": "preprocess-TRAIN", "local_path": "in/d"}]}
		]
	}`
	rr := do(srv, "POST", "/workflows", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "from") {
		t.Fatalf("expected from-shape message: %s", rr.Body.String())
	}
}
