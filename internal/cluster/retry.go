// internal/cluster/retry.go
//
// Retry policy evaluation and backoff delay calculation.
//
// Pure functions — no side effects, no I/O. All randomness is injected
// via the jitter parameter so tests are deterministic.

package cluster

import (
	"math/rand"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ShouldRetry returns true if the job has a retry policy with remaining attempts.
func ShouldRetry(job *cpb.Job) bool {
	if job.RetryPolicy == nil {
		return false
	}
	return job.Attempt < job.RetryPolicy.MaxAttempts
}

// NextRetryDelay calculates the backoff delay for the next retry attempt.
// attempt is the attempt number that just failed (1-indexed).
func NextRetryDelay(policy *cpb.RetryPolicy, attempt uint32) time.Duration {
	if policy == nil {
		return 0
	}

	initialDelay := policy.InitialDelayMs
	if initialDelay == 0 {
		initialDelay = 1000 // default 1s
	}
	maxDelay := policy.MaxDelayMs
	if maxDelay == 0 {
		maxDelay = 60000 // default 60s
	}

	base := time.Duration(initialDelay) * time.Millisecond
	var delay time.Duration

	switch policy.Backoff {
	case cpb.BackoffLinear:
		delay = base * time.Duration(attempt)
	case cpb.BackoffExponential:
		delay = base * time.Duration(uint64(1)<<(attempt-1))
	default: // BackoffNone or unknown
		delay = base
	}

	cap := time.Duration(maxDelay) * time.Millisecond
	if delay > cap {
		delay = cap
	}

	if policy.Jitter && delay > 0 {
		// Add 0-25% jitter to prevent thundering herd.
		jitter := time.Duration(rand.Int63n(int64(delay) / 4))
		delay += jitter
	}

	return delay
}

// DefaultRetryPolicy returns a sensible default retry policy for workflows
// that don't specify one per-job.
func DefaultRetryPolicy() *cpb.RetryPolicy {
	return &cpb.RetryPolicy{
		MaxAttempts:    1,
		Backoff:        cpb.BackoffExponential,
		InitialDelayMs: 1000,
		MaxDelayMs:     60000,
		Jitter:         true,
	}
}
