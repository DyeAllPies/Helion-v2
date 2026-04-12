// internal/cluster/dispatch_test.go
//
// Tests for DispatchLoop: constructor, Run with context cancellation,
// dispatching pending jobs, and error paths.

package cluster_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// ── mock NodeDispatcher ──────────────────────────────────────────────────────

type mockNodeDispatcher struct {
	mu       sync.Mutex
	calls    []string // job IDs dispatched
	err      error    // if non-nil, DispatchToNode returns this
}

func (m *mockNodeDispatcher) DispatchToNode(_ context.Context, _ string, job *cpb.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, job.ID)
	return m.err
}

func (m *mockNodeDispatcher) dispatched() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

// ── helpers ──────────────────────────────────────────────────────────────────

func newDispatchJobStore(t *testing.T) *cluster.JobStore {
	t.Helper()
	return cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
}

func newSchedulerWithNodes(t *testing.T) (*cluster.Scheduler, *cluster.Registry) {
	t.Helper()
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()
	_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: "node-1", Address: "127.0.0.1:9090"})
	_ = r.HandleHeartbeat(ctx, &pb.HeartbeatMessage{NodeId: "node-1"})
	s := cluster.NewScheduler(r, cluster.NewRoundRobinPolicy())
	return s, r
}

// ── NewDispatchLoop ──────────────────────────────────────────────────────────

func TestNewDispatchLoop_ReturnsNonNil(t *testing.T) {
	js := newDispatchJobStore(t)
	sched, _ := newSchedulerWithNodes(t)
	d := &mockNodeDispatcher{}
	dl := cluster.NewDispatchLoop(js, sched, d, 100*time.Millisecond, slog.Default())
	if dl == nil {
		t.Fatal("expected non-nil DispatchLoop")
	}
}

// ── Run: context cancellation ────────────────────────────────────────────────

func TestDispatchLoop_Run_StopsOnCancel(t *testing.T) {
	js := newDispatchJobStore(t)
	sched, _ := newSchedulerWithNodes(t)
	d := &mockNodeDispatcher{}
	dl := cluster.NewDispatchLoop(js, sched, d, 50*time.Millisecond, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		dl.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// good — Run returned
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

// ── Run: dispatches pending jobs ─────────────────────────────────────────────

func TestDispatchLoop_DispatchesPendingJobs(t *testing.T) {
	js := newDispatchJobStore(t)
	sched, _ := newSchedulerWithNodes(t)
	d := &mockNodeDispatcher{}
	dl := cluster.NewDispatchLoop(js, sched, d, 50*time.Millisecond, slog.Default())

	// Submit a pending job.
	_ = js.Submit(context.Background(), &cpb.Job{ID: "dispatch-j1", Command: "echo"})

	ctx, cancel := context.WithCancel(context.Background())
	go dl.Run(ctx)

	// Wait for the job to be dispatched.
	deadline := time.Now().Add(2 * time.Second)
	for len(d.dispatched()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	dispatched := d.dispatched()
	if len(dispatched) == 0 {
		t.Fatal("expected at least 1 dispatch call")
	}
	if dispatched[0] != "dispatch-j1" {
		t.Errorf("dispatched wrong job: got %s, want dispatch-j1", dispatched[0])
	}
}

// ── Run: no healthy nodes — no dispatch ──────────────────────────────────────

func TestDispatchLoop_NoHealthyNodes_NoPanic(t *testing.T) {
	js := newDispatchJobStore(t)
	// Empty registry — no nodes.
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	sched := cluster.NewScheduler(r, cluster.NewRoundRobinPolicy())
	d := &mockNodeDispatcher{}
	dl := cluster.NewDispatchLoop(js, sched, d, 50*time.Millisecond, slog.Default())

	_ = js.Submit(context.Background(), &cpb.Job{ID: "no-nodes-j", Command: "echo"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	dl.Run(ctx) // should not panic

	if len(d.dispatched()) != 0 {
		t.Error("should not dispatch when no healthy nodes")
	}
}

// ── Run: dispatch failure marks job FAILED ───────────────────────────────────

func TestDispatchLoop_DispatchFailure_MarksJobFailed(t *testing.T) {
	js := newDispatchJobStore(t)
	sched, _ := newSchedulerWithNodes(t)
	d := &mockNodeDispatcher{err: fmt.Errorf("node unreachable")}
	dl := cluster.NewDispatchLoop(js, sched, d, 50*time.Millisecond, slog.Default())

	_ = js.Submit(context.Background(), &cpb.Job{ID: "fail-j1", Command: "echo"})

	ctx, cancel := context.WithCancel(context.Background())
	go dl.Run(ctx)

	// Wait for the dispatch attempt.
	deadline := time.Now().Add(2 * time.Second)
	for len(d.dispatched()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	// The job should be FAILED after dispatch error.
	j, err := js.Get("fail-j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if j.Status != cpb.JobStatusFailed {
		t.Errorf("want FAILED, got %s", j.Status.String())
	}
}

// ── Run: skips non-pending jobs ──────────────────────────────────────────────

func TestDispatchLoop_SkipsNonPendingJobs(t *testing.T) {
	js := newDispatchJobStore(t)
	sched, _ := newSchedulerWithNodes(t)
	d := &mockNodeDispatcher{}
	dl := cluster.NewDispatchLoop(js, sched, d, 50*time.Millisecond, slog.Default())

	ctx := context.Background()
	_ = js.Submit(ctx, &cpb.Job{ID: "running-j", Command: "echo"})
	// Transition to dispatching then running so it's no longer pending.
	_ = js.Transition(ctx, "running-j", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "node-1"})
	_ = js.Transition(ctx, "running-j", cpb.JobStatusRunning, cluster.TransitionOptions{})

	runCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	dl.Run(runCtx)

	if len(d.dispatched()) != 0 {
		t.Error("should not dispatch non-pending jobs")
	}
}
