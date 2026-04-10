// internal/ratelimit/limiter.go
//
// Per-node sliding-window rate limiter for job dispatch RPC.
//
// Phase 4 security hardening:
// ──────────────────────────
// Prevents a compromised or malicious node from flooding the coordinator
// with job dispatch requests. Each node has an independent rate limit,
// configured via HELION_RATE_LIMIT_RPS environment variable.
//
// Default: 10 jobs/second per node.
//
// Design:
// ──────
// We use golang.org/x/time/rate.Limiter with a token bucket algorithm:
//   - Each node has its own limiter (map[nodeID]*rate.Limiter)
//   - Burst size = rate (allows short bursts up to the rate limit)
//   - Limiter is created on first request from a node
//   - Limiters are not cleaned up (acceptable for small clusters)
//
// When the limit is exceeded, the coordinator:
//   1. Returns gRPC ResourceExhausted status
//   2. Logs a rate_limit_hit audit event
//   3. Does NOT dispatch the job
//
// Load test verification:
// ──────────────────────
// A test submits 100 jobs/s from a single node with a 10 jobs/s limit.
// Expected outcome:
//   - First 10 jobs succeed immediately (bucket full)
//   - Jobs 11-100 are rate limited (returns ResourceExhausted)
//   - Over 10 seconds, ~100 jobs succeed (rate = 10/s sustained)
//
// The limiter allows short bursts (the first 10 jobs) but enforces the
// long-term rate. This is the desired behavior: normal nodes can burst
// briefly without being penalized, but sustained flooding is blocked.

package ratelimit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultRateLimit is the default jobs per second per node.
	DefaultRateLimit = 10

	// EnvRateLimitRPS is the environment variable for rate limit config.
	EnvRateLimitRPS = "HELION_RATE_LIMIT_RPS"
)

// NodeLimiter manages per-node rate limiters.
type NodeLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*rate.Limiter
	rate     float64 // jobs per second
	burst    int     // burst size (= rate for simplicity)
}

// NewNodeLimiter creates a NodeLimiter with the configured rate.
// Reads HELION_RATE_LIMIT_RPS env var, defaults to DefaultRateLimit.
func NewNodeLimiter() *NodeLimiter {
	rateLimit := float64(DefaultRateLimit)

	if env := os.Getenv(EnvRateLimitRPS); env != "" {
		if parsed, err := strconv.ParseFloat(env, 64); err == nil && parsed > 0 {
			rateLimit = parsed
		}
	}

	return &NodeLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rateLimit,
		burst:    int(rateLimit), // Allow bursts up to the rate limit
	}
}

// Allow checks if a request from nodeID is allowed under the rate limit.
// Returns nil if allowed, gRPC ResourceExhausted status if rate limited.
//
// This method is safe for concurrent use and creates limiters lazily.
func (nl *NodeLimiter) Allow(ctx context.Context, nodeID string) error {
	// Fast path: read lock to check if limiter exists
	nl.mu.RLock()
	limiter, exists := nl.limiters[nodeID]
	nl.mu.RUnlock()

	// Slow path: create limiter if it doesn't exist
	if !exists {
		nl.mu.Lock()
		// Double-check after acquiring write lock (race condition)
		limiter, exists = nl.limiters[nodeID]
		if !exists {
			limiter = rate.NewLimiter(rate.Limit(nl.rate), nl.burst)
			nl.limiters[nodeID] = limiter
		}
		nl.mu.Unlock()
	}

	// Check if request is allowed
	if !limiter.Allow() {
		return status.Errorf(codes.ResourceExhausted,
			"rate limit exceeded for node %s (limit: %.1f jobs/s)", nodeID, nl.rate)
	}

	return nil
}

// AllowN checks if N requests from nodeID are allowed.
// Used for batch operations (future enhancement).
func (nl *NodeLimiter) AllowN(ctx context.Context, nodeID string, n int) error {
	nl.mu.RLock()
	limiter, exists := nl.limiters[nodeID]
	nl.mu.RUnlock()

	if !exists {
		nl.mu.Lock()
		limiter, exists = nl.limiters[nodeID]
		if !exists {
			limiter = rate.NewLimiter(rate.Limit(nl.rate), nl.burst)
			nl.limiters[nodeID] = limiter
		}
		nl.mu.Unlock()
	}

	if !limiter.AllowN(timeFromContext(ctx), n) {
		return status.Errorf(codes.ResourceExhausted,
			"rate limit exceeded for node %s (requested: %d, limit: %.1f jobs/s)",
			nodeID, n, nl.rate)
	}

	return nil
}

// Wait blocks until a request from nodeID is allowed under the rate limit.
// Returns error if context is canceled before request is allowed.
//
// This is used for cooperative rate limiting where the caller wants to wait
// rather than being rejected immediately. Not used in Phase 4 (we reject
// immediately), but available for future enhancements.
func (nl *NodeLimiter) Wait(ctx context.Context, nodeID string) error {
	nl.mu.RLock()
	limiter, exists := nl.limiters[nodeID]
	nl.mu.RUnlock()

	if !exists {
		nl.mu.Lock()
		limiter, exists = nl.limiters[nodeID]
		if !exists {
			limiter = rate.NewLimiter(rate.Limit(nl.rate), nl.burst)
			nl.limiters[nodeID] = limiter
		}
		nl.mu.Unlock()
	}

	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait canceled: %w", err)
	}

	return nil
}

// GetRate returns the configured rate limit (jobs per second).
func (nl *NodeLimiter) GetRate() float64 {
	return nl.rate
}

// Stats returns rate limiter statistics for a node.
// Returns the current token count and burst size.
type Stats struct {
	NodeID       string
	Tokens       float64 // Current tokens available (0 to burst)
	Burst        int     // Maximum burst size
	Rate         float64 // Sustained rate (jobs/s)
	TotalNodes   int     // Total number of nodes with limiters
}

// GetStats returns statistics for a specific node.
func (nl *NodeLimiter) GetStats(nodeID string) *Stats {
	nl.mu.RLock()
	defer nl.mu.RUnlock()

	limiter, exists := nl.limiters[nodeID]
	if !exists {
		return &Stats{
			NodeID:     nodeID,
			Tokens:     nl.rate,
			Burst:      nl.burst,
			Rate:       nl.rate,
			TotalNodes: len(nl.limiters),
		}
	}

	return &Stats{
		NodeID:     nodeID,
		Tokens:     float64(limiter.Tokens()),
		Burst:      nl.burst,
		Rate:       nl.rate,
		TotalNodes: len(nl.limiters),
	}
}

// AllStats returns statistics for all nodes.
func (nl *NodeLimiter) AllStats() []*Stats {
	nl.mu.RLock()
	defer nl.mu.RUnlock()

	stats := make([]*Stats, 0, len(nl.limiters))
	for nodeID, limiter := range nl.limiters {
		stats = append(stats, &Stats{
			NodeID:     nodeID,
			Tokens:     float64(limiter.Tokens()),
			Burst:      nl.burst,
			Rate:       nl.rate,
			TotalNodes: len(nl.limiters),
		})
	}

	return stats
}

// Reset resets the rate limiter for a specific node.
// Used for testing and admin operations.
func (nl *NodeLimiter) Reset(nodeID string) {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	delete(nl.limiters, nodeID)
}

// ResetAll resets all rate limiters.
// Used for testing and coordinator restart.
func (nl *NodeLimiter) ResetAll() {
	nl.mu.Lock()
	defer nl.mu.Unlock()

	nl.limiters = make(map[string]*rate.Limiter)
}

// timeFromContext extracts a deadline from context for rate.Limiter.
// Returns time.Now() if no deadline is set.
func timeFromContext(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now()
}

// GarbageCollect removes limiters for nodes that haven't been seen in a while.
// Called periodically by the coordinator to prevent unbounded memory growth.
//
// NOTE: For Phase 4, we skip garbage collection because:
//   1. Small cluster size (<100 nodes) means memory usage is negligible
//   2. Nodes are long-lived (not ephemeral)
//   3. Coordination with node health checking is complex
//
// A future version could track last-seen timestamps and evict stale limiters.
func (nl *NodeLimiter) GarbageCollect(staleThreshold time.Duration) int {
	// TODO(Phase 5): Implement GC using node health tracker
	// For now, return 0 (no limiters removed)
	return 0
}
