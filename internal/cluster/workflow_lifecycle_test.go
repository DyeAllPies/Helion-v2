// internal/cluster/workflow_lifecycle_test.go
//
// Tests for EligibleJobs, OnJobCompleted, Cancel, and dependency conditions.

package cluster_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── EligibleJobs ─────────────────────────────────────────────────────────────

func TestWorkflowStore_EligibleJobs_RootsFirst(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-elig",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
			{Name: "deploy", Command: "make", DependsOn: []string{"test"}},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-elig", js)

	// Initially, only root jobs should be eligible.
	eligible := ws.EligibleJobs("wf-elig", js)
	if len(eligible) != 1 || eligible[0] != "wf-elig/build" {
		t.Fatalf("expected only build to be eligible, got %v", eligible)
	}
}

func TestWorkflowStore_EligibleJobs_AfterDependencyCompletes(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-dep",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-dep", js)

	// Complete the build job.
	_ = js.Transition(ctx, "wf-dep/build", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-dep/build", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-dep/build", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	// Now test should be eligible.
	eligible := ws.EligibleJobs("wf-dep", js)
	if len(eligible) != 1 || eligible[0] != "wf-dep/test" {
		t.Fatalf("expected test to be eligible after build completed, got %v", eligible)
	}
}

func TestWorkflowStore_EligibleJobs_BlockedByRunningDep(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-block",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-block", js)

	// Build is running but not completed — test should not be eligible.
	_ = js.Transition(ctx, "wf-block/build", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-block/build", cpb.JobStatusRunning, cluster.TransitionOptions{})

	eligible := ws.EligibleJobs("wf-block", js)
	if len(eligible) != 0 {
		t.Fatalf("expected no eligible jobs while build is running, got %v", eligible)
	}
}

// ── OnJobCompleted ───────────────────────────────────────────────────────────

func TestWorkflowStore_OnJobCompleted_WorkflowCompletes(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-done",
		Jobs: []cpb.WorkflowJob{
			{Name: "only", Command: "echo"},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-done", js)

	_ = js.Transition(ctx, "wf-done/only", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-done/only", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-done/only", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	ws.OnJobCompleted(ctx, "wf-done/only", cpb.JobStatusCompleted, js)

	got, _ := ws.Get("wf-done")
	if got.Status != cpb.WorkflowStatusCompleted {
		t.Fatalf("expected workflow completed, got %s", got.Status)
	}
}

func TestWorkflowStore_OnJobCompleted_WorkflowFails(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-fail",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-fail", js)

	_ = js.Transition(ctx, "wf-fail/build", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-fail/build", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-fail/build", cpb.JobStatusFailed, cluster.TransitionOptions{ErrMsg: "build error"})

	ws.OnJobCompleted(ctx, "wf-fail/build", cpb.JobStatusFailed, js)

	testJob, _ := js.Get("wf-fail/test")
	if testJob.Status != cpb.JobStatusSkipped {
		t.Fatalf("expected test to be skipped (cascading), got %s", testJob.Status)
	}

	got, _ := ws.Get("wf-fail")
	if got.Status != cpb.WorkflowStatusFailed {
		t.Fatalf("expected workflow failed, got %s", got.Status)
	}
}

func TestWorkflowStore_OnJobCompleted_IgnoresStandaloneJobs(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "standalone", Command: "echo"})
	ws.OnJobCompleted(ctx, "standalone", cpb.JobStatusCompleted, js)
}

// ── Cancel ───────────────────────────────────────────────────────────────────

func TestWorkflowStore_Cancel(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-cancel",
		Jobs: []cpb.WorkflowJob{
			{Name: "build", Command: "make"},
			{Name: "test", Command: "make", DependsOn: []string{"build"}},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-cancel", js)

	if err := ws.Cancel(ctx, "wf-cancel", js); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	got, _ := ws.Get("wf-cancel")
	if got.Status != cpb.WorkflowStatusCancelled {
		t.Fatalf("expected cancelled, got %s", got.Status)
	}

	buildJob, _ := js.Get("wf-cancel/build")
	if buildJob.Status != cpb.JobStatusLost {
		t.Fatalf("expected build job lost, got %s", buildJob.Status)
	}
}

func TestWorkflowStore_Cancel_NotFound(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	err := ws.Cancel(context.Background(), "nonexistent", js)
	if !errors.Is(err, cluster.ErrWorkflowNotFound) {
		t.Fatalf("expected ErrWorkflowNotFound, got %v", err)
	}
}

func TestWorkflowStore_Cancel_AlreadyTerminal(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID:   "wf-term",
		Jobs: []cpb.WorkflowJob{{Name: "a", Command: "echo"}},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-term", js)
	_ = ws.Cancel(ctx, "wf-term", js)

	err := ws.Cancel(ctx, "wf-term", js)
	if !errors.Is(err, cluster.ErrWorkflowAlreadyTerminal) {
		t.Fatalf("expected ErrWorkflowAlreadyTerminal, got %v", err)
	}
}

// ── DependencyCondition ──────────────────────────────────────────────────────

func TestWorkflowStore_EligibleJobs_OnFailureCondition(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-cond",
		Jobs: []cpb.WorkflowJob{
			{Name: "risky", Command: "make"},
			{Name: "cleanup", Command: "make", DependsOn: []string{"risky"}, Condition: cpb.DependencyOnFailure},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-cond", js)

	eligible := ws.EligibleJobs("wf-cond", js)
	if len(eligible) != 1 || eligible[0] != "wf-cond/risky" {
		t.Fatalf("expected only risky to be eligible, got %v", eligible)
	}

	// Complete risky successfully — cleanup should NOT be eligible (on_failure).
	_ = js.Transition(ctx, "wf-cond/risky", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-cond/risky", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-cond/risky", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	eligible = ws.EligibleJobs("wf-cond", js)
	if len(eligible) != 0 {
		t.Fatalf("cleanup should not be eligible when risky succeeded, got %v", eligible)
	}
}

func TestWorkflowStore_EligibleJobs_OnFailureCondition_DepFails(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-cond2",
		Jobs: []cpb.WorkflowJob{
			{Name: "risky", Command: "make"},
			{Name: "cleanup", Command: "make", DependsOn: []string{"risky"}, Condition: cpb.DependencyOnFailure},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-cond2", js)

	_ = js.Transition(ctx, "wf-cond2/risky", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-cond2/risky", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-cond2/risky", cpb.JobStatusFailed, cluster.TransitionOptions{})

	eligible := ws.EligibleJobs("wf-cond2", js)
	if len(eligible) != 1 || eligible[0] != "wf-cond2/cleanup" {
		t.Fatalf("expected cleanup to be eligible after risky failed, got %v", eligible)
	}
}

func TestWorkflowStore_EligibleJobs_OnCompleteCondition(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	js := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID: "wf-cond3",
		Jobs: []cpb.WorkflowJob{
			{Name: "main", Command: "make"},
			{Name: "notify", Command: "make", DependsOn: []string{"main"}, Condition: cpb.DependencyOnComplete},
		},
	}
	_ = ws.Submit(ctx, wf)
	_ = ws.Start(ctx, "wf-cond3", js)

	_ = js.Transition(ctx, "wf-cond3/main", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-cond3/main", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "wf-cond3/main", cpb.JobStatusFailed, cluster.TransitionOptions{})

	eligible := ws.EligibleJobs("wf-cond3", js)
	if len(eligible) != 1 || eligible[0] != "wf-cond3/notify" {
		t.Fatalf("expected notify to be eligible after main failed (on_complete), got %v", eligible)
	}
}
