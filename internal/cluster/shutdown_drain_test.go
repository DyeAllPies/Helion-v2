// internal/cluster/shutdown_drain_test.go
//
// Regression tests for AUDIT 2026-04-11/M1: fire-and-forget background
// persister writes must run under a bounded timeout, and JobStore.Close
// must drain them during shutdown instead of leaking goroutines past
// process exit.

package cluster_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// stallingPersister blocks on AppendAudit until ctx is cancelled, then
// returns ctx.Err() so callers can observe whether the write deadline fired.
type stallingPersister struct {
	inner     *cluster.MemJobPersister
	calls     atomic.Int32
	cancelled atomic.Int32
}

func newStallingPersister() *stallingPersister {
	return &stallingPersister{inner: cluster.NewMemJobPersister()}
}

func (p *stallingPersister) SaveJob(ctx context.Context, j *cpb.Job) error {
	return p.inner.SaveJob(ctx, j)
}

func (p *stallingPersister) LoadAllJobs(ctx context.Context) ([]*cpb.Job, error) {
	return p.inner.LoadAllJobs(ctx)
}

func (p *stallingPersister) AppendAudit(ctx context.Context, _, _, _, _ string) error {
	p.calls.Add(1)
	<-ctx.Done()
	p.cancelled.Add(1)
	return ctx.Err()
}

// TestJobStore_Close_DrainsStalledWrite verifies that a stalled background
// write does not leak past Close and that Close enforces its own timeout
// so the coordinator's shutdown path is bounded.
func TestJobStore_Close_DrainsStalledWrite(t *testing.T) {
	p := newStallingPersister()
	s := cluster.NewJobStore(p, nil)

	if err := s.Submit(context.Background(), &cpb.Job{ID: "j1", Command: "ls"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Give the goroutine a moment to enter AppendAudit.
	deadline := time.Now().Add(2 * time.Second)
	for p.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.calls.Load() == 0 {
		t.Fatal("background goroutine never called AppendAudit")
	}

	// Close with a very short budget — the write is still blocked, so Close
	// must return on its own timeout rather than wait the full 5s write deadline.
	start := time.Now()
	s.Close(100 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Close blocked too long: %v (expected <500ms)", elapsed)
	}
}

// TestJobStore_WriteDeadline_BoundsStalledGoroutine verifies the per-write
// timeout fires even when Close is never called, so a stalled persister
// can never leak goroutines forever.
func TestJobStore_WriteDeadline_BoundsStalledGoroutine(t *testing.T) {
	p := newStallingPersister()
	s := cluster.NewJobStore(p, nil)

	if err := s.Submit(context.Background(), &cpb.Job{ID: "j1", Command: "ls"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The production write deadline is 5s. We wait up to 7s for the
	// goroutine to observe ctx.Done and return. This is long for a unit
	// test but the whole point of the regression is that an unbounded
	// write would NEVER unblock — 7s is a clear signal either way.
	deadline := time.Now().Add(7 * time.Second)
	for p.cancelled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if p.cancelled.Load() == 0 {
		t.Fatal("background write did not time out within 7s — write deadline not enforced")
	}

	// Close should now drain cleanly.
	s.Close(1 * time.Second)
}
