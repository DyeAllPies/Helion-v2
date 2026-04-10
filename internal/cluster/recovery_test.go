// internal/cluster/recovery_test.go
//
// Tests for RecoveryManager — NewRecoveryManager, Run (no jobs, cancel, dispatch).

package cluster_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── mockDispatcher ─────────────────────────────────────────────────────────────

type mockDispatcher struct {
	nodeID string
	err    error
}

func (m *mockDispatcher) Dispatch(_ context.Context, _ *cpb.Job) (string, error) {
	return m.nodeID, m.err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newRecoveryJobStore(t *testing.T) *cluster.JobStore {
	t.Helper()
	return cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
}

// ── NewRecoveryManager ────────────────────────────────────────────────────────

func TestNewRecoveryManager_ReturnsNonNil(t *testing.T) {
	js := newRecoveryJobStore(t)
	d := &mockDispatcher{nodeID: "n1"}
	rm := cluster.NewRecoveryManager(js, d, time.Millisecond, nil)
	if rm == nil {
		t.Fatal("expected non-nil RecoveryManager")
	}
}

func TestNewRecoveryManager_NilLogger_UsesDefault(t *testing.T) {
	// Pass nil logger — should not panic.
	js := newRecoveryJobStore(t)
	rm := cluster.NewRecoveryManager(js, &mockDispatcher{nodeID: "n1"}, time.Millisecond, nil)
	if rm == nil {
		t.Fatal("expected non-nil RecoveryManager")
	}
}

// ── Run: no non-terminal jobs ─────────────────────────────────────────────────

func TestRecoveryManager_Run_NoJobs_ReturnsNil(t *testing.T) {
	js := newRecoveryJobStore(t)
	rm := cluster.NewRecoveryManager(js, &mockDispatcher{nodeID: "n1"}, time.Millisecond, nil)

	if err := rm.Run(context.Background()); err != nil {
		t.Errorf("Run with no jobs: %v", err)
	}
}

// ── Run: context cancelled during grace period ────────────────────────────────

func TestRecoveryManager_Run_CancelDuringGrace_ReturnsNil(t *testing.T) {
	js := newRecoveryJobStore(t)
	ctx := context.Background()

	// Submit a non-terminal job so Run doesn't exit early.
	_ = js.Submit(ctx, &cpb.Job{ID: "recovery-j1", Command: "ls"})

	rm := cluster.NewRecoveryManager(js, &mockDispatcher{nodeID: "n1"},
		500*time.Millisecond, nil)

	cancelCtx, cancel := context.WithCancel(ctx)

	done := make(chan error, 1)
	go func() { done <- rm.Run(cancelCtx) }()

	// Cancel before grace period elapses.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run with cancelled ctx should return nil, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// ── Run: successful dispatch ───────────────────────────────────────────────────

func TestRecoveryManager_Run_DispatchSuccess_JobTransitionsToDispatching(t *testing.T) {
	js := newRecoveryJobStore(t)
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "disp-j1", Command: "echo"})

	d := &mockDispatcher{nodeID: "healthy-node"}
	rm := cluster.NewRecoveryManager(js, d, time.Millisecond, nil)

	if err := rm.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// After dispatch the job should be in DISPATCHING state.
	job, err := js.Get("disp-j1")
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if job.Status != cpb.JobStatusDispatching {
		t.Errorf("want DISPATCHING, got %s", job.Status.String())
	}
}

// ── Run: dispatch fails → job marked lost ─────────────────────────────────────

func TestRecoveryManager_Run_DispatchFails_JobMarkedLost(t *testing.T) {
	js := newRecoveryJobStore(t)
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "fail-j1", Command: "echo"})

	d := &mockDispatcher{err: errors.New("no healthy nodes")}
	rm := cluster.NewRecoveryManager(js, d, time.Millisecond, nil)

	if err := rm.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	job, err := js.Get("fail-j1")
	if err != nil {
		t.Fatalf("Get after failed dispatch: %v", err)
	}
	if job.Status != cpb.JobStatusLost {
		t.Errorf("want LOST after dispatch failure, got %s", job.Status.String())
	}
}

// ── Run: already-terminal job in snapshot is skipped ─────────────────────────

func TestRecoveryManager_Run_TerminalJobInStore_Skipped(t *testing.T) {
	js := newRecoveryJobStore(t)
	ctx := context.Background()

	// Submit and drive to completed before recovery.
	_ = js.Submit(ctx, &cpb.Job{ID: "done-j1", Command: "ls"})
	_ = js.Transition(ctx, "done-j1", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "n1"})
	_ = js.Transition(ctx, "done-j1", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "done-j1", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	d := &mockDispatcher{nodeID: "n1"}
	rm := cluster.NewRecoveryManager(js, d, time.Millisecond, nil)

	// Run should exit immediately (no non-terminal jobs).
	if err := rm.Run(ctx); err != nil {
		t.Errorf("Run: %v", err)
	}

	// Job should still be COMPLETED.
	job, _ := js.Get("done-j1")
	if job.Status != cpb.JobStatusCompleted {
		t.Errorf("want COMPLETED, got %s", job.Status.String())
	}
}

// ── Run: multiple jobs, mixed outcomes ────────────────────────────────────────

func TestRecoveryManager_Run_MultipleJobs_AllDispatched(t *testing.T) {
	js := newRecoveryJobStore(t)
	ctx := context.Background()

	for _, id := range []string{"m1", "m2", "m3"} {
		_ = js.Submit(ctx, &cpb.Job{ID: id, Command: "ls"})
	}

	d := &mockDispatcher{nodeID: "n1"}
	rm := cluster.NewRecoveryManager(js, d, time.Millisecond, nil)

	if err := rm.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, id := range []string{"m1", "m2", "m3"} {
		job, err := js.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if job.Status != cpb.JobStatusDispatching {
			t.Errorf("job %s: want DISPATCHING, got %s", id, job.Status.String())
		}
	}
}

// ── Run: job that was in RUNNING state (non-pending) at recovery ──────────────

func TestRecoveryManager_Run_RunningJob_ResetsAndRedispatches(t *testing.T) {
	js := newRecoveryJobStore(t)
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "run-j1", Command: "ls"})
	_ = js.Transition(ctx, "run-j1", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "n1"})
	_ = js.Transition(ctx, "run-j1", cpb.JobStatusRunning, cluster.TransitionOptions{})

	d := &mockDispatcher{nodeID: "n2"}
	rm := cluster.NewRecoveryManager(js, d, time.Millisecond, nil)

	if err := rm.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	job, err := js.Get("run-j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Status != cpb.JobStatusDispatching {
		t.Errorf("want DISPATCHING after recovery, got %s", job.Status.String())
	}
}
