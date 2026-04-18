// internal/api/handlers_workflows_lineage_test.go
//
// Feature 18 — HTTP-layer tests for GET /workflows/{id}/lineage.
// The business logic (BuildWorkflowLineage) is covered by
// internal/cluster/workflow_lineage_test.go; this file covers the
// handler's own branches: auth enforcement, unknown-workflow 404,
// and the happy-path 200 with a JSON-decodable WorkflowLineage
// response. Without these, a regression dropping the authMiddleware
// wrapper, a branch reordering in the error-translation switch, or
// a response-shape change would slip past the library-level tests.

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
)

// TestGetWorkflowLineage_NoAuth_Returns401 pins the authMiddleware
// wrapper at server.go:167. Every other workflow test disables auth
// via newWorkflowServer; a refactor that accidentally dropped the
// wrapper from SetWorkflowStore's lineage registration would leave
// the endpoint publicly enumerable (workflow topology + job IDs +
// registered-model names). Information disclosure — no existing
// test catches it.
func TestGetWorkflowLineage_NoAuth_Returns401(t *testing.T) {
	srv, _ := newAuthServer(t)
	p := cluster.NewMemWorkflowPersister()
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ws := cluster.NewWorkflowStore(p, nil)
	srv.SetWorkflowStore(ws, js)

	rr := doWithToken(srv, "GET", "/workflows/any-id/lineage", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestGetWorkflowLineage_UnknownWorkflow_Returns404 pins the
// ErrWorkflowLineageNotFound translation at
// handlers_workflows_lineage.go:45-47. Drives the handler with a
// fully-wired store but a bogus ID; asserts 404 (not 500). A
// regression that swapped the error translation for a generic
// 500 would silently turn "workflow doesn't exist" into "internal
// error" — misleading for clients.
func TestGetWorkflowLineage_UnknownWorkflow_Returns404(t *testing.T) {
	srv := newWorkflowServer()
	rr := do(srv, "GET", "/workflows/does-not-exist/lineage", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestGetWorkflowLineage_Success_Returns200 pins the happy-path
// handler → library → JSON-encode chain. Submits a minimal
// two-job workflow (so there's at least one dependency edge to
// render), drives GET /workflows/{id}/lineage, and asserts the
// response is 200 with the expected top-level shape. Field-level
// coverage of the lineage join is in
// cluster/workflow_lineage_test.go; this test's job is to prove
// the handler correctly forwards that payload to clients.
func TestGetWorkflowLineage_Success_Returns200(t *testing.T) {
	srv := newWorkflowServer()

	// Submit a minimal workflow to populate the store.
	body := `{
		"id": "lineage-happy",
		"name": "lineage happy path",
		"jobs": [
			{"name": "build", "command": "echo"},
			{"name": "test", "command": "echo", "depends_on": ["build"]}
		]
	}`
	if rr := do(srv, "POST", "/workflows", body); rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	rr := do(srv, "GET", "/workflows/lineage-happy/lineage", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		WorkflowID    string                  `json:"workflow_id"`
		Name          string                  `json:"name"`
		Jobs          []cluster.LineageJob    `json:"jobs"`
		ArtifactEdges []cluster.ArtifactEdge  `json:"artifact_edges"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.WorkflowID != "lineage-happy" {
		t.Errorf("workflow_id: got %q, want %q", got.WorkflowID, "lineage-happy")
	}
	if len(got.Jobs) != 2 {
		t.Fatalf("jobs: got %d, want 2", len(got.Jobs))
	}
	// The dependency edge is captured via DependsOn on the
	// downstream job; the ArtifactEdges slice is only populated
	// for `from:` references, which this minimal workflow does
	// not declare.
	var haveDepend bool
	for _, j := range got.Jobs {
		if j.Name == "test" {
			for _, dep := range j.DependsOn {
				if dep == "build" {
					haveDepend = true
				}
			}
		}
	}
	if !haveDepend {
		t.Errorf("expected test's depends_on to include build; jobs=%+v", got.Jobs)
	}
}

