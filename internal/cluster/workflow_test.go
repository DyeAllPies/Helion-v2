// internal/cluster/workflow_test.go
//
// Tests for WorkflowStore Submit, Start, and basic operations.

package cluster_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func newTestWorkflowStore() (*cluster.WorkflowStore, *cluster.MemWorkflowPersister) {
	p := cluster.NewMemWorkflowPersister()
	return cluster.NewWorkflowStore(p, nil), p
}

func newTestJobStore() *cluster.JobStore {
	return cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
}

// ── Submit ───────────────────────────────────────────────────────────────────

func TestWorkflowStore_Submit_Valid(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID:   "wf-1",
		Name: "build pipeline",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
		},
	}

	if err := ws.Submit(ctx, wf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ws.Get("wf-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Status != cpb.WorkflowStatusPending {
		t.Fatalf("expected pending, got %s", got.Status)
	}
	if len(got.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(got.Jobs))
	}
}

func TestWorkflowStore_Submit_Duplicate(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID:   "wf-1",
		Jobs: []cpb.WorkflowJob{{Name: "a", Command: "echo"}},
	}
	_ = ws.Submit(ctx, wf)

	err := ws.Submit(ctx, &cpb.Workflow{
		ID:   "wf-1",
		Jobs: []cpb.WorkflowJob{{Name: "b", Command: "echo"}},
	})
	if !errors.Is(err, cluster.ErrWorkflowExists) {
		t.Fatalf("expected ErrWorkflowExists, got %v", err)
	}
}

func TestWorkflowStore_Submit_EmptyJobs(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	err := ws.Submit(context.Background(), &cpb.Workflow{ID: "wf-empty", Jobs: nil})
	if !errors.Is(err, cluster.ErrWorkflowEmpty) {
		t.Fatalf("expected ErrWorkflowEmpty, got %v", err)
	}
}

func TestWorkflowStore_Submit_InvalidDAG(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	wf := &cpb.Workflow{
		ID: "wf-cycle",
		Jobs: []cpb.WorkflowJob{
			{Name: "a", Command: "echo", DependsOn: []string{"b"}},
			{Name: "b", Command: "echo", DependsOn: []string{"a"}},
		},
	}
	err := ws.Submit(context.Background(), wf)
	if !errors.Is(err, cluster.ErrDAGCycle) {
		t.Fatalf("expected ErrDAGCycle, got %v", err)
	}
}

// ── Start ────────────────────────────────────────────────────────────────────

func TestWorkflowStore_Start_CreatesJobs(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-start",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
		},
	}
	_ = ws.Submit(ctx, wf)

	if err := ws.Start(ctx, "wf-start", js); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify workflow status.
	got, _ := ws.Get("wf-start")
	if got.Status != cpb.WorkflowStatusRunning {
		t.Fatalf("expected running, got %s", got.Status)
	}

	// Verify jobs were created.
	buildJob, err := js.Get("wf-start/build")
	if err != nil {
		t.Fatalf("build job not found: %v", err)
	}
	if buildJob.WorkflowID != "wf-start" {
		t.Fatalf("expected workflow_id=wf-start, got %s", buildJob.WorkflowID)
	}
	if buildJob.Status != cpb.JobStatusPending {
		t.Fatalf("expected pending, got %s", buildJob.Status)
	}

	testJob, err := js.Get("wf-start/test")
	if err != nil {
		t.Fatalf("test job not found: %v", err)
	}
	if testJob.Status != cpb.JobStatusPending {
		t.Fatalf("expected pending, got %s", testJob.Status)
	}
}

// ── List + Restore ───────────────────────────────────────────────────────────

func TestWorkflowStore_List(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	ctx := context.Background()

	for _, id := range []string{"wf-1", "wf-2", "wf-3"} {
		_ = ws.Submit(ctx, &cpb.Workflow{
			ID:   id,
			Jobs: []cpb.WorkflowJob{{Name: "a", Command: "echo"}},
		})
	}

	list := ws.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 workflows, got %d", len(list))
	}
}

func TestWorkflowStore_Restore(t *testing.T) {
	p := cluster.NewMemWorkflowPersister()
	ctx := context.Background()

	// Pre-populate the persister.
	_ = p.SaveWorkflow(ctx, &cpb.Workflow{
		ID:     "wf-restored",
		Status: cpb.WorkflowStatusRunning,
		Jobs:   []cpb.WorkflowJob{{Name: "a", Command: "echo", JobID: "wf-restored/a"}},
	})

	ws := cluster.NewWorkflowStore(p, nil)
	if err := ws.Restore(ctx); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	got, err := ws.Get("wf-restored")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Status != cpb.WorkflowStatusRunning {
		t.Fatalf("expected running, got %s", got.Status)
	}
}
