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
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// limiterEntry pairs a token-bucket limiter with a last-seen timestamp so
// GarbageCollect can evict stale entries without holding mu for long.
type limiterEntry struct {
	limiter      *rate.Limiter
	lastSeenNano int64 // Unix nanoseconds; updated atomically on every Allow call
}

const (
	// DefaultRateLimit is the default jobs per second per node.
	DefaultRateLimit = 10

	// EnvRateLimitRPS is the environment variable for rate limit config.
	EnvRateLimitRPS = "HELION_RATE_LIMIT_RPS"
)

// NodeLimiter manages per-node rate limiters.
type NodeLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*limiterEntry
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
		limiters: make(map[string]*limiterEntry),
		rate:     rateLimit,
		burst:    int(rateLimit), // Allow bursts up to the rate limit
	}
}

// getOrCreate returns the limiterEntry for nodeID, creating one if absent.
func (nl *NodeLimiter) getOrCreate(nodeID string) *limiterEntry {
	nl.mu.RLock()
	entry, exists := nl.limiters[nodeID]
	nl.mu.RUnlock()
	if exists {
		return entry
	}
	nl.mu.Lock()
	defer nl.mu.Unlock()
	if entry, exists = nl.limiters[nodeID]; exists {
		return entry
	}
	entry = &limiterEntry{
		limiter:      rate.NewLimiter(rate.Limit(nl.rate), nl.burst),
		lastSeenNano: time.Now().UnixNano(),
	}
	nl.limiters[nodeID] = entry
	return entry
}

// Allow checks if a request from nodeID is allowed under the rate limit.
// Returns nil if allowed, gRPC ResourceExhausted status if rate limited.
//
// This method is safe for concurrent use and creates limiters lazily.
func (nl *NodeLimiter) Allow(ctx context.Context, nodeID string) error {
	entry := nl.getOrCreate(nodeID)
	atomic.StoreInt64(&entry.lastSeenNano, time.Now().UnixNano())

	// Check if request is allowed
	if !entry.limiter.Allow() {
		return status.Errorf(codes.ResourceExhausted,
			"rate limit exceeded for node %s (limit: %.1f jobs/s)", nodeID, nl.rate)
	}

	return nil
}

// AllowN checks if N requests from nodeID are allowed.
// Used for batch operations (future enhancement).
func (nl *NodeLimiter) AllowN(ctx context.Context, nodeID string, n int) error {
	entry := nl.getOrCreate(nodeID)
	atomic.StoreInt64(&entry.lastSeenNano, time.Now().UnixNano())

	if !entry.limiter.AllowN(timeFromContext(ctx), n) {
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
	entry := nl.getOrCreate(nodeID)
	atomic.StoreInt64(&entry.lastSeenNano, time.Now().UnixNano())

	if err := entry.limiter.Wait(ctx); err != nil {
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

	entry, exists := nl.limiters[nodeID]
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
		Tokens:     float64(entry.limiter.Tokens()),
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
	for nodeID, entry := range nl.limiters {
		stats = append(stats, &Stats{
			NodeID:     nodeID,
			Tokens:     float64(entry.limiter.Tokens()),
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

	nl.limiters = make(map[string]*limiterEntry)
}

// timeFromContext extracts a deadline from context for rate.Limiter.
// Returns time.Now() if no deadline is set.
func timeFromContext(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now()
}

// GarbageCollect removes limiters for nodes that have not sent a request
// within staleThreshold. Returns the number of entries evicted.
// Call this periodically (e.g. every 2× heartbeat interval) to prevent
// unbounded memory growth when ephemeral nodes come and go.
func (nl *NodeLimiter) GarbageCollect(staleThreshold time.Duration) int {
	cutoffNano := time.Now().Add(-staleThreshold).UnixNano()

	// Collect stale keys under read lock to minimise contention.
	nl.mu.RLock()
	var stale []string
	for nodeID, entry := range nl.limiters {
		if atomic.LoadInt64(&entry.lastSeenNano) < cutoffNano {
			stale = append(stale, nodeID)
		}
	}
	nl.mu.RUnlock()

	if len(stale) == 0 {
		return 0
	}

	nl.mu.Lock()
	defer nl.mu.Unlock()
	removed := 0
	for _, nodeID := range stale {
		// Re-check under write lock: entry may have been refreshed since the
		// read pass above.
		if entry, ok := nl.limiters[nodeID]; ok {
			if atomic.LoadInt64(&entry.lastSeenNano) < cutoffNano {
				delete(nl.limiters, nodeID)
				removed++
			}
		}
	}
	return removed
}
