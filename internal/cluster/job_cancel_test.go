package cluster_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestCancelJob_Pending_TransitionsToCancelled(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "cancel-1", Command: "echo"})
	if err := js.CancelJob(ctx, "cancel-1", "user requested"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	j, _ := js.Get("cancel-1")
	if j.Status != cpb.JobStatusCancelled {
		t.Errorf("status = %s, want cancelled", j.Status)
	}
}

func TestCancelJob_Running_TransitionsToCancelled(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "cancel-2", Command: "echo"})
	_ = js.Transition(ctx, "cancel-2", cpb.JobStatusScheduled, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "cancel-2", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "cancel-2", cpb.JobStatusRunning, cluster.TransitionOptions{})

	if err := js.CancelJob(ctx, "cancel-2", "user requested"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	j, _ := js.Get("cancel-2")
	if j.Status != cpb.JobStatusCancelled {
		t.Errorf("status = %s, want cancelled", j.Status)
	}
}

func TestCancelJob_NotFound(t *testing.T) {
	js := newTestJobStore()
	err := js.CancelJob(context.Background(), "nonexistent", "test")
	if !errors.Is(err, cluster.ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
}

func TestCancelJob_AlreadyTerminal(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "cancel-3", Command: "echo"})
	_ = js.Transition(ctx, "cancel-3", cpb.JobStatusScheduled, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "cancel-3", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "cancel-3", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "cancel-3", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	err := js.CancelJob(ctx, "cancel-3", "too late")
	if !errors.Is(err, cluster.ErrJobAlreadyTerminal) {
		t.Fatalf("expected ErrJobAlreadyTerminal, got %v", err)
	}
}

func TestCancelJob_Dispatching_FallsBackToLost(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "cancel-4", Command: "echo"})
	_ = js.Transition(ctx, "cancel-4", cpb.JobStatusScheduled, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "cancel-4", cpb.JobStatusDispatching, cluster.TransitionOptions{})

	// Dispatching doesn't have a direct cancelled transition, so CancelJob
	// falls back to MarkLost.
	if err := js.CancelJob(ctx, "cancel-4", "cancel during dispatch"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	j, _ := js.Get("cancel-4")
	if j.Status != cpb.JobStatusLost {
		t.Errorf("status = %s, want lost (fallback for dispatching)", j.Status)
	}
}

// ── Scheduled state ──────────────────────────────────────────────────────────

func TestScheduledState_TransitionsCorrectly(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "sched-1", Command: "echo"})

	// pending → scheduled
	err := js.Transition(ctx, "sched-1", cpb.JobStatusScheduled, cluster.TransitionOptions{NodeID: "node-1"})
	if err != nil {
		t.Fatalf("pending → scheduled: %v", err)
	}

	j, _ := js.Get("sched-1")
	if j.Status != cpb.JobStatusScheduled {
		t.Errorf("status = %s, want scheduled", j.Status)
	}
	if j.NodeID != "node-1" {
		t.Errorf("node_id = %q, want node-1", j.NodeID)
	}

	// scheduled → dispatching
	err = js.Transition(ctx, "sched-1", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	if err != nil {
		t.Fatalf("scheduled → dispatching: %v", err)
	}
}

// ── Skipped state ────────────────────────────────────────────────────────────

func TestSkippedState_IsPendingToSkipped(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "skip-1", Command: "echo"})

	err := js.Transition(ctx, "skip-1", cpb.JobStatusSkipped, cluster.TransitionOptions{
		ErrMsg: "upstream failed",
	})
	if err != nil {
		t.Fatalf("pending → skipped: %v", err)
	}

	j, _ := js.Get("skip-1")
	if j.Status != cpb.JobStatusSkipped {
		t.Errorf("status = %s, want skipped", j.Status)
	}
	if !j.Status.IsTerminal() {
		t.Error("skipped should be terminal")
	}
}
