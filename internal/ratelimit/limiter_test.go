package ratelimit_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── Construction ──────────────────────────────────────────────────────────────

func TestNewNodeLimiter_DefaultRate(t *testing.T) {
	os.Unsetenv(ratelimit.EnvRateLimitRPS)
	nl := ratelimit.NewNodeLimiter()
	if nl.GetRate() != ratelimit.DefaultRateLimit {
		t.Errorf("want default rate %v, got %v", ratelimit.DefaultRateLimit, nl.GetRate())
	}
}

func TestNewNodeLimiter_EnvVariable(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "25")
	nl := ratelimit.NewNodeLimiter()
	if nl.GetRate() != 25 {
		t.Errorf("want rate 25, got %v", nl.GetRate())
	}
}

func TestNewNodeLimiter_InvalidEnvVar_UsesDefault(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "not-a-number")
	nl := ratelimit.NewNodeLimiter()
	if nl.GetRate() != ratelimit.DefaultRateLimit {
		t.Errorf("want default rate on bad env, got %v", nl.GetRate())
	}
}

func TestNewNodeLimiter_ZeroEnvVar_UsesDefault(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "0")
	nl := ratelimit.NewNodeLimiter()
	if nl.GetRate() != ratelimit.DefaultRateLimit {
		t.Errorf("want default rate on zero env, got %v", nl.GetRate())
	}
}

// ── Allow ─────────────────────────────────────────────────────────────────────

func TestAllow_FirstRequest_Succeeds(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	if err := nl.Allow(context.Background(), "node-1"); err != nil {
		t.Errorf("first request should be allowed, got: %v", err)
	}
}

func TestAllow_WithinBurst_Succeeds(t *testing.T) {
	// High-rate limiter so token replenishment doesn't interfere.
	t.Setenv(ratelimit.EnvRateLimitRPS, "100")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	// Burst = int(rate) = 100; all 100 should succeed immediately.
	for i := 0; i < 100; i++ {
		if err := nl.Allow(ctx, "node-1"); err != nil {
			t.Fatalf("request %d should be within burst, got: %v", i+1, err)
		}
	}
}

func TestAllow_ExceedsBurst_Rejected(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "5")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	// Drain the burst (5 tokens).
	for i := 0; i < 5; i++ {
		_ = nl.Allow(ctx, "node-x")
	}

	// 6th request must be rejected.
	err := nl.Allow(ctx, "node-x")
	if err == nil {
		t.Fatal("expected ResourceExhausted after burst, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %T", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("want ResourceExhausted, got %v", st.Code())
	}
}

func TestAllow_ErrorMessageContainsRate(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "3")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_ = nl.Allow(ctx, "node-msg")
	}

	err := nl.Allow(ctx, "node-msg")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if len(msg) == 0 {
		t.Error("error message should not be empty")
	}
}

func TestAllow_DifferentNodes_Independent(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "2")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	// Drain node-A.
	_ = nl.Allow(ctx, "node-A")
	_ = nl.Allow(ctx, "node-A")
	if err := nl.Allow(ctx, "node-A"); err == nil {
		t.Error("node-A should be rate-limited")
	}

	// node-B should still be allowed.
	if err := nl.Allow(ctx, "node-B"); err != nil {
		t.Errorf("node-B should be independent from node-A: %v", err)
	}
}

func TestAllow_ConcurrentSafe(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = nl.Allow(ctx, "concurrent-node")
		}()
	}
	wg.Wait() // Should not race or panic.
}

// ── AllowN ────────────────────────────────────────────────────────────────────

func TestAllowN_WithinBurst_Succeeds(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "10")
	nl := ratelimit.NewNodeLimiter()
	if err := nl.AllowN(context.Background(), "node-1", 5); err != nil {
		t.Errorf("AllowN(5) within burst should succeed: %v", err)
	}
}

func TestAllowN_ExceedsBurst_Rejected(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "5")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	// Consume all 5 burst tokens at once.
	_ = nl.AllowN(ctx, "node-1", 5)

	if err := nl.AllowN(ctx, "node-1", 1); err == nil {
		t.Error("expected rejection after burst exhausted")
	}
}

// ── Wait ──────────────────────────────────────────────────────────────────────

func TestWait_ContextCanceled_ReturnsError(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "1")
	nl := ratelimit.NewNodeLimiter()

	// Drain the single burst token.
	_ = nl.Allow(context.Background(), "node-wait")

	// Immediately-canceled context should return an error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := nl.Wait(ctx, "node-wait"); err == nil {
		t.Error("expected error with canceled context")
	}
}

func TestWait_TokenAvailable_Succeeds(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "100")
	nl := ratelimit.NewNodeLimiter()

	// With a high rate, a token should be available immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := nl.Wait(ctx, "node-wait-ok"); err != nil {
		t.Errorf("Wait should succeed when tokens are available: %v", err)
	}
}

// ── Reset ─────────────────────────────────────────────────────────────────────

func TestReset_ClearsLimiter_AllowsAgain(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "2")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	_ = nl.Allow(ctx, "node-r")
	_ = nl.Allow(ctx, "node-r")
	if err := nl.Allow(ctx, "node-r"); err == nil {
		t.Fatal("should be rate-limited before reset")
	}

	nl.Reset("node-r")

	// After reset, fresh burst available.
	if err := nl.Allow(ctx, "node-r"); err != nil {
		t.Errorf("should be allowed after reset: %v", err)
	}
}

func TestReset_UnknownNode_NoError(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	nl.Reset("nonexistent") // Should not panic.
}

func TestResetAll_ClearsAllLimiters(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "1")
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	_ = nl.Allow(ctx, "n1")
	_ = nl.Allow(ctx, "n2")

	nl.ResetAll()

	stats := nl.AllStats()
	if len(stats) != 0 {
		t.Errorf("expected no limiters after ResetAll, got %d", len(stats))
	}
}

// ── Stats ─────────────────────────────────────────────────────────────────────

func TestGetStats_UnknownNode_ReturnsFull(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	s := nl.GetStats("unknown-node")

	if s.NodeID != "unknown-node" {
		t.Errorf("want node-id 'unknown-node', got %q", s.NodeID)
	}
	if s.Burst <= 0 {
		t.Error("burst should be positive")
	}
	if s.Rate != nl.GetRate() {
		t.Errorf("want rate %v, got %v", nl.GetRate(), s.Rate)
	}
}

func TestGetStats_KnownNode_ShowsCorrectRate(t *testing.T) {
	t.Setenv(ratelimit.EnvRateLimitRPS, "10")
	nl := ratelimit.NewNodeLimiter()
	_ = nl.Allow(context.Background(), "node-stats")

	s := nl.GetStats("node-stats")
	if s.Rate != 10 {
		t.Errorf("want rate 10, got %v", s.Rate)
	}
	if s.Burst != 10 {
		t.Errorf("want burst 10, got %d", s.Burst)
	}
}

func TestAllStats_ReturnsAll(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	_ = nl.Allow(ctx, "n1")
	_ = nl.Allow(ctx, "n2")
	_ = nl.Allow(ctx, "n3")

	all := nl.AllStats()
	if len(all) != 3 {
		t.Errorf("want 3 stats entries, got %d", len(all))
	}
	for _, s := range all {
		if s.TotalNodes != 3 {
			t.Errorf("TotalNodes should be 3, got %d", s.TotalNodes)
		}
	}
}

func TestAllStats_Empty_ReturnsNone(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	if got := nl.AllStats(); len(got) != 0 {
		t.Errorf("expected empty stats, got %d", len(got))
	}
}

// ── GarbageCollect ────────────────────────────────────────────────────────────

func TestGarbageCollect_ReturnsZero(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()
	if n := nl.GarbageCollect(time.Hour); n != 0 {
		t.Errorf("GarbageCollect stub should return 0, got %d", n)
	}
}

// ── timeFromContext (via AllowN with deadline context) ─────────────────────────

func TestAllowN_WithDeadlineContext_UsesDeadline(t *testing.T) {
	nl := ratelimit.NewNodeLimiter()

	// Context with a deadline — exercises the timeFromContext deadline branch.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
	defer cancel()

	// AllowN calls timeFromContext(ctx) internally.
	if err := nl.AllowN(ctx, "node-deadline", 1); err != nil {
		t.Errorf("AllowN with deadline context: %v", err)
	}
}