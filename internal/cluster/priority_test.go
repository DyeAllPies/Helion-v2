package cluster_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── EffectivePriority ────────────────────────────────────────────────────────

func TestEffectivePriority_DefaultPriority(t *testing.T) {
	job := &cpb.Job{CreatedAt: time.Now()}
	if p := cluster.EffectivePriority(job); p != 50 {
		t.Errorf("expected 50 for zero-priority job, got %d", p)
	}
}

func TestEffectivePriority_ExplicitPriority(t *testing.T) {
	job := &cpb.Job{Priority: 90, CreatedAt: time.Now()}
	if p := cluster.EffectivePriority(job); p != 90 {
		t.Errorf("expected 90, got %d", p)
	}
}

func TestEffectivePriority_AgeBoost(t *testing.T) {
	// Job created 5 minutes ago with priority 30 → effective = 30 + 5 = 35.
	job := &cpb.Job{Priority: 30, CreatedAt: time.Now().Add(-5 * time.Minute)}
	p := cluster.EffectivePriority(job)
	if p < 35 || p > 36 {
		t.Errorf("expected ~35, got %d", p)
	}
}

func TestEffectivePriority_CappedAt100(t *testing.T) {
	// Job with priority 90 created 60 minutes ago → 90 + 60 = 150, capped at 100.
	job := &cpb.Job{Priority: 90, CreatedAt: time.Now().Add(-60 * time.Minute)}
	if p := cluster.EffectivePriority(job); p != 100 {
		t.Errorf("expected 100 (capped), got %d", p)
	}
}

// ── PendingByPriority ────────────────────────────────────────────────────────

func TestPendingByPriority_SortedDescending(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	// Submit jobs with different priorities.
	_ = js.Submit(ctx, &cpb.Job{ID: "low", Command: "echo", Priority: 10})
	time.Sleep(time.Millisecond)
	_ = js.Submit(ctx, &cpb.Job{ID: "high", Command: "echo", Priority: 90})
	time.Sleep(time.Millisecond)
	_ = js.Submit(ctx, &cpb.Job{ID: "normal", Command: "echo", Priority: 50})

	pending := js.PendingByPriority()
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}

	// High priority first.
	if pending[0].ID != "high" {
		t.Errorf("first = %q, want high", pending[0].ID)
	}
	if pending[1].ID != "normal" {
		t.Errorf("second = %q, want normal", pending[1].ID)
	}
	if pending[2].ID != "low" {
		t.Errorf("third = %q, want low", pending[2].ID)
	}
}

func TestPendingByPriority_FIFO_WithinSamePriority(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "first", Command: "echo", Priority: 50})
	time.Sleep(2 * time.Millisecond)
	_ = js.Submit(ctx, &cpb.Job{ID: "second", Command: "echo", Priority: 50})
	time.Sleep(2 * time.Millisecond)
	_ = js.Submit(ctx, &cpb.Job{ID: "third", Command: "echo", Priority: 50})

	pending := js.PendingByPriority()
	if len(pending) != 3 {
		t.Fatalf("expected 3, got %d", len(pending))
	}

	// Same priority → FIFO (oldest first).
	if pending[0].ID != "first" {
		t.Errorf("first = %q, want first (FIFO)", pending[0].ID)
	}
	if pending[2].ID != "third" {
		t.Errorf("last = %q, want third", pending[2].ID)
	}
}

func TestPendingByPriority_ExcludesNonPending(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "pending-job", Command: "echo", Priority: 50})
	_ = js.Submit(ctx, &cpb.Job{ID: "dispatched-job", Command: "echo", Priority: 90})
	_ = js.Transition(ctx, "dispatched-job", cpb.JobStatusScheduled, cluster.TransitionOptions{})

	pending := js.PendingByPriority()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].ID != "pending-job" {
		t.Errorf("expected pending-job, got %q", pending[0].ID)
	}
}

func TestPendingByPriority_DefaultPriorityIs50(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "default-pri", Command: "echo"})

	pending := js.PendingByPriority()
	if len(pending) != 1 {
		t.Fatalf("expected 1, got %d", len(pending))
	}
	if pending[0].Priority != 50 {
		t.Errorf("default priority = %d, want 50", pending[0].Priority)
	}
}
