package cluster_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── ShouldRetry ──────────────────────────────────────────────────────────────

func TestShouldRetry_NoPolicy(t *testing.T) {
	job := &cpb.Job{ID: "j1", Attempt: 1}
	if cluster.ShouldRetry(job) {
		t.Error("expected false for job without retry policy")
	}
}

func TestShouldRetry_AttemptsRemaining(t *testing.T) {
	job := &cpb.Job{
		ID:      "j2",
		Attempt: 1,
		RetryPolicy: &cpb.RetryPolicy{
			MaxAttempts: 3,
		},
	}
	if !cluster.ShouldRetry(job) {
		t.Error("expected true: attempt 1 < max 3")
	}
}

func TestShouldRetry_AttemptsExhausted(t *testing.T) {
	job := &cpb.Job{
		ID:      "j3",
		Attempt: 3,
		RetryPolicy: &cpb.RetryPolicy{
			MaxAttempts: 3,
		},
	}
	if cluster.ShouldRetry(job) {
		t.Error("expected false: attempt 3 == max 3")
	}
}

// ── NextRetryDelay ───────────────────────────────────────────────────────────

func TestNextRetryDelay_NilPolicy(t *testing.T) {
	if d := cluster.NextRetryDelay(nil, 1); d != 0 {
		t.Errorf("expected 0, got %v", d)
	}
}

func TestNextRetryDelay_BackoffNone(t *testing.T) {
	p := &cpb.RetryPolicy{
		Backoff:        cpb.BackoffNone,
		InitialDelayMs: 2000,
		MaxDelayMs:     60000,
		Jitter:         false,
	}
	d1 := cluster.NextRetryDelay(p, 1)
	d3 := cluster.NextRetryDelay(p, 3)
	if d1 != 2*time.Second {
		t.Errorf("attempt 1: expected 2s, got %v", d1)
	}
	if d3 != 2*time.Second {
		t.Errorf("attempt 3: expected 2s (fixed), got %v", d3)
	}
}

func TestNextRetryDelay_BackoffLinear(t *testing.T) {
	p := &cpb.RetryPolicy{
		Backoff:        cpb.BackoffLinear,
		InitialDelayMs: 1000,
		MaxDelayMs:     60000,
		Jitter:         false,
	}
	if d := cluster.NextRetryDelay(p, 1); d != 1*time.Second {
		t.Errorf("attempt 1: expected 1s, got %v", d)
	}
	if d := cluster.NextRetryDelay(p, 3); d != 3*time.Second {
		t.Errorf("attempt 3: expected 3s, got %v", d)
	}
}

func TestNextRetryDelay_BackoffExponential(t *testing.T) {
	p := &cpb.RetryPolicy{
		Backoff:        cpb.BackoffExponential,
		InitialDelayMs: 1000,
		MaxDelayMs:     60000,
		Jitter:         false,
	}
	if d := cluster.NextRetryDelay(p, 1); d != 1*time.Second {
		t.Errorf("attempt 1: expected 1s, got %v", d)
	}
	if d := cluster.NextRetryDelay(p, 2); d != 2*time.Second {
		t.Errorf("attempt 2: expected 2s, got %v", d)
	}
	if d := cluster.NextRetryDelay(p, 4); d != 8*time.Second {
		t.Errorf("attempt 4: expected 8s, got %v", d)
	}
}

func TestNextRetryDelay_CappedAtMax(t *testing.T) {
	p := &cpb.RetryPolicy{
		Backoff:        cpb.BackoffExponential,
		InitialDelayMs: 10000,
		MaxDelayMs:     15000,
		Jitter:         false,
	}
	d := cluster.NextRetryDelay(p, 5) // 10s * 16 = 160s, capped at 15s
	if d != 15*time.Second {
		t.Errorf("expected 15s cap, got %v", d)
	}
}

func TestNextRetryDelay_DefaultValues(t *testing.T) {
	p := &cpb.RetryPolicy{
		Backoff: cpb.BackoffNone,
		Jitter:  false,
		// InitialDelayMs and MaxDelayMs both 0 → defaults to 1s and 60s.
	}
	if d := cluster.NextRetryDelay(p, 1); d != 1*time.Second {
		t.Errorf("expected default 1s, got %v", d)
	}
}

func TestNextRetryDelay_WithJitter(t *testing.T) {
	p := &cpb.RetryPolicy{
		Backoff:        cpb.BackoffNone,
		InitialDelayMs: 4000,
		MaxDelayMs:     60000,
		Jitter:         true,
	}
	d := cluster.NextRetryDelay(p, 1)
	// Base is 4s, jitter adds 0-25% → range [4s, 5s)
	if d < 4*time.Second || d >= 5*time.Second {
		t.Errorf("jitter delay out of range: %v (expected [4s, 5s))", d)
	}
}

// ── RetryIfEligible ──────────────────────────────────────────────────────────

func TestRetryIfEligible_NoPolicy_ReturnsFalse(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{ID: "no-retry", Command: "echo"})
	_ = js.Transition(ctx, "no-retry", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "no-retry", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "no-retry", cpb.JobStatusFailed, cluster.TransitionOptions{ErrMsg: "oops"})

	if js.RetryIfEligible(ctx, "no-retry") {
		t.Error("expected false for job without retry policy")
	}

	j, _ := js.Get("no-retry")
	if j.Status != cpb.JobStatusFailed {
		t.Errorf("expected failed, got %s", j.Status)
	}
}

func TestRetryIfEligible_WithPolicy_RetriesJob(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{
		ID:      "retry-me",
		Command: "echo",
		RetryPolicy: &cpb.RetryPolicy{
			MaxAttempts:    3,
			Backoff:        cpb.BackoffNone,
			InitialDelayMs: 100,
			Jitter:         false,
		},
	})
	_ = js.Transition(ctx, "retry-me", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "retry-me", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "retry-me", cpb.JobStatusFailed, cluster.TransitionOptions{ErrMsg: "transient"})

	if !js.RetryIfEligible(ctx, "retry-me") {
		t.Fatal("expected retry to succeed")
	}

	j, _ := js.Get("retry-me")
	if j.Status != cpb.JobStatusPending {
		t.Errorf("expected pending after retry, got %s", j.Status)
	}
	if j.Attempt != 2 {
		t.Errorf("expected attempt=2, got %d", j.Attempt)
	}
	if j.RetryAfter.IsZero() {
		t.Error("expected non-zero RetryAfter")
	}
	if j.Error != "" {
		t.Errorf("expected cleared error, got %q", j.Error)
	}
}

func TestRetryIfEligible_ExhaustedAttempts_ReturnsFalse(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{
		ID:      "exhaust",
		Command: "echo",
		RetryPolicy: &cpb.RetryPolicy{
			MaxAttempts:    2,
			Backoff:        cpb.BackoffNone,
			InitialDelayMs: 100,
			Jitter:         false,
		},
	})

	// First attempt fails → retry succeeds.
	_ = js.Transition(ctx, "exhaust", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "exhaust", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "exhaust", cpb.JobStatusFailed, cluster.TransitionOptions{})
	if !js.RetryIfEligible(ctx, "exhaust") {
		t.Fatal("first retry should succeed")
	}

	// Second attempt fails → retry should NOT succeed (2/2 attempts used).
	_ = js.Transition(ctx, "exhaust", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "exhaust", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "exhaust", cpb.JobStatusFailed, cluster.TransitionOptions{})
	if js.RetryIfEligible(ctx, "exhaust") {
		t.Error("retry should fail: all attempts exhausted")
	}

	j, _ := js.Get("exhaust")
	if j.Status != cpb.JobStatusFailed {
		t.Errorf("expected failed (terminal), got %s", j.Status)
	}
}

func TestRetryIfEligible_TimeoutIsRetryable(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{
		ID:      "timeout-retry",
		Command: "echo",
		RetryPolicy: &cpb.RetryPolicy{
			MaxAttempts:    2,
			Backoff:        cpb.BackoffNone,
			InitialDelayMs: 100,
			Jitter:         false,
		},
	})
	_ = js.Transition(ctx, "timeout-retry", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "timeout-retry", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "timeout-retry", cpb.JobStatusTimeout, cluster.TransitionOptions{})

	if !js.RetryIfEligible(ctx, "timeout-retry") {
		t.Fatal("timeout should be retryable")
	}

	j, _ := js.Get("timeout-retry")
	if j.Status != cpb.JobStatusPending {
		t.Errorf("expected pending after timeout retry, got %s", j.Status)
	}
}

func TestRetryIfEligible_CompletedNotRetryable(t *testing.T) {
	js := newTestJobStore()
	ctx := context.Background()

	_ = js.Submit(ctx, &cpb.Job{
		ID:      "completed",
		Command: "echo",
		RetryPolicy: &cpb.RetryPolicy{MaxAttempts: 3},
	})
	_ = js.Transition(ctx, "completed", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "completed", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = js.Transition(ctx, "completed", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	if js.RetryIfEligible(ctx, "completed") {
		t.Error("completed jobs should not be retried")
	}
}

func TestRetryIfEligible_NonexistentJob_ReturnsFalse(t *testing.T) {
	js := newTestJobStore()
	if js.RetryIfEligible(context.Background(), "nope") {
		t.Error("expected false for nonexistent job")
	}
}

// ── DefaultRetryPolicy ───────────────────────────────────────────────────────

func TestDefaultRetryPolicy(t *testing.T) {
	p := cluster.DefaultRetryPolicy()
	if p.MaxAttempts != 1 {
		t.Errorf("MaxAttempts = %d, want 1", p.MaxAttempts)
	}
	if p.Backoff != cpb.BackoffExponential {
		t.Errorf("Backoff = %v, want exponential", p.Backoff)
	}
	if !p.Jitter {
		t.Error("Jitter should be true by default")
	}
}
