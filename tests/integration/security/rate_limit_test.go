// tests/integration/security/rate_limit_test.go
//
// Integration tests for per-node rate limiting.
//
// Phase 4 exit criteria:
//   - Load test: node submits 100 jobs/s
//   - Limiter allows configured rate (10 jobs/s default)
//   - Excess returns ResourceExhausted gRPC status

package security

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRateLimitBasic(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node"
	ctx := context.Background()

	// First request should succeed (bucket full)
	err := limiter.Allow(ctx, nodeID)
	if err != nil {
		t.Errorf("First request should succeed: %v", err)
	}

	// Rapid subsequent requests should eventually be rate limited
	allowed := 1
	rejected := 0

	for i := 0; i < 99; i++ {
		err := limiter.Allow(ctx, nodeID)
		if err == nil {
			allowed++
		} else {
			rejected++
			
			// Verify it's the correct error code
			st, ok := status.FromError(err)
			if !ok {
				t.Errorf("Expected gRPC status error, got: %v", err)
			} else if st.Code() != codes.ResourceExhausted {
				t.Errorf("Expected ResourceExhausted, got: %v", st.Code())
			}
		}
	}

	t.Logf("Allowed: %d, Rejected: %d", allowed, rejected)

	// With default rate of 10/s and burst of 10:
	// First 10 should succeed (burst), rest should be rejected
	if allowed < 10 {
		t.Errorf("Expected at least 10 allowed (burst), got %d", allowed)
	}

	if rejected == 0 {
		t.Error("Expected some requests to be rejected")
	}
}

func TestRateLimitSustainedRate(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node-sustained"
	ctx := context.Background()

	// Submit requests at 5/s for 2 seconds (10 total)
	// This is below the default 10/s limit, so all should succeed
	allowed := 0
	rejected := 0

	ticker := time.NewTicker(200 * time.Millisecond) // 5 per second
	defer ticker.Stop()

	done := time.After(2 * time.Second)

	for {
		select {
		case <-ticker.C:
			err := limiter.Allow(ctx, nodeID)
			if err == nil {
				allowed++
			} else {
				rejected++
			}

		case <-done:
			goto end
		}
	}

end:
	t.Logf("Sustained rate test: Allowed: %d, Rejected: %d", allowed, rejected)

	// At 5/s for 2s, we expect ~10 requests, all allowed
	if allowed < 8 {
		t.Errorf("Expected at least 8 allowed, got %d", allowed)
	}

	if rejected > 2 {
		t.Errorf("Expected at most 2 rejected, got %d", rejected)
	}
}

func TestRateLimitPerNode(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	ctx := context.Background()

	// Two different nodes should have independent limits
	node1 := "node-1"
	node2 := "node-2"

	// Exhaust node1's burst
	for i := 0; i < 20; i++ {
		_ = limiter.Allow(ctx, node1)
	}

	// Node2 should still have its full burst available
	allowed := 0
	for i := 0; i < 10; i++ {
		err := limiter.Allow(ctx, node2)
		if err == nil {
			allowed++
		}
	}

	if allowed < 10 {
		t.Errorf("Node2 should have full burst available, got %d/10", allowed)
	}
}

func TestRateLimitConfigurable(t *testing.T) {
	// Set custom rate limit via environment variable
	os.Setenv("HELION_RATE_LIMIT_RPS", "5")
	defer os.Unsetenv("HELION_RATE_LIMIT_RPS")

	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node-custom"
	ctx := context.Background()

	// Verify the configured rate is used
	if limiter.GetRate() != 5.0 {
		t.Errorf("Expected rate 5.0, got %.1f", limiter.GetRate())
	}

	// First 5 should succeed (burst = rate)
	allowed := 0
	for i := 0; i < 10; i++ {
		err := limiter.Allow(ctx, nodeID)
		if err == nil {
			allowed++
		}
	}

	if allowed != 5 {
		t.Errorf("Expected exactly 5 allowed (burst), got %d", allowed)
	}
}

func TestRateLimitLoadTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node-load"
	ctx := context.Background()

	// Submit 100 jobs/s for 10 seconds = 1000 total requests
	// With default 10 jobs/s limit, we expect ~100 to succeed
	allowed := 0
	rejected := 0

	start := time.Now()
	end := start.Add(10 * time.Second)

	// Submit requests as fast as possible
	for time.Now().Before(end) {
		err := limiter.Allow(ctx, nodeID)
		if err == nil {
			allowed++
		} else {
			rejected++
		}
	}

	duration := time.Since(start)
	t.Logf("Load test over %v: Allowed: %d, Rejected: %d", duration, allowed, rejected)

	// Over 10 seconds at 10 jobs/s, we expect ~100 allowed
	// Allow 20% variance (80-120 allowed)
	if allowed < 80 || allowed > 120 {
		t.Errorf("Expected ~100 allowed, got %d", allowed)
	}

	// The vast majority should be rejected
	if rejected < 800 {
		t.Errorf("Expected at least 800 rejected, got %d", rejected)
	}

	// Verify sustained rate
	rate := float64(allowed) / duration.Seconds()
	t.Logf("Sustained rate: %.2f jobs/s", rate)

	if rate < 8.0 || rate > 12.0 {
		t.Errorf("Expected rate ~10 jobs/s, got %.2f", rate)
	}
}

func TestRateLimitReset(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node-reset"
	ctx := context.Background()

	// Exhaust burst
	for i := 0; i < 20; i++ {
		_ = limiter.Allow(ctx, nodeID)
	}

	// Should be rate limited
	err := limiter.Allow(ctx, nodeID)
	if err == nil {
		t.Error("Expected rate limit after exhausting burst")
	}

	// Reset limiter for this node
	limiter.Reset(nodeID)

	// Burst should be available again
	allowed := 0
	for i := 0; i < 10; i++ {
		err := limiter.Allow(ctx, nodeID)
		if err == nil {
			allowed++
		}
	}

	if allowed < 10 {
		t.Errorf("After reset, expected full burst, got %d/10", allowed)
	}
}

func TestRateLimitStats(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node-stats"
	ctx := context.Background()

	// Make some requests
	for i := 0; i < 5; i++ {
		_ = limiter.Allow(ctx, nodeID)
	}

	// Get stats
	stats := limiter.GetStats(nodeID)

	if stats.NodeID != nodeID {
		t.Errorf("Expected node ID %s, got %s", nodeID, stats.NodeID)
	}

	if stats.Rate != 10.0 {
		t.Errorf("Expected rate 10.0, got %.1f", stats.Rate)
	}

	if stats.Burst != 10 {
		t.Errorf("Expected burst 10, got %d", stats.Burst)
	}

	// Tokens should be reduced (5 consumed out of 10)
	if stats.Tokens > 5.5 || stats.Tokens < 4.5 {
		t.Errorf("Expected ~5 tokens remaining, got %.1f", stats.Tokens)
	}

	t.Logf("Stats: %+v", stats)
}

func TestRateLimitConcurrent(t *testing.T) {
	limiter := ratelimit.NewNodeLimiter()
	nodeID := "test-node-concurrent"
	ctx := context.Background()

	// Multiple goroutines submitting concurrently
	const numGoroutines = 10
	const requestsPerGoroutine = 10

	done := make(chan int, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func() {
			allowed := 0
			for i := 0; i < requestsPerGoroutine; i++ {
				err := limiter.Allow(ctx, nodeID)
				if err == nil {
					allowed++
				}
			}
			done <- allowed
		}()
	}

	totalAllowed := 0
	for g := 0; g < numGoroutines; g++ {
		totalAllowed += <-done
	}

	t.Logf("Concurrent test: Total allowed: %d/%d", totalAllowed, numGoroutines*requestsPerGoroutine)

	// With burst of 10, first 10 should succeed, rest rejected
	// Allow some variance due to concurrent access
	if totalAllowed < 8 || totalAllowed > 15 {
		t.Errorf("Expected ~10 allowed, got %d", totalAllowed)
	}
}
