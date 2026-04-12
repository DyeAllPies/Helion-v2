// internal/cluster/job_retry.go
//
// JobStore retry operations: RetryIfEligible transitions a failed/timed-out
// job through the retrying state back to pending with a backoff delay.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// RetryIfEligible checks whether a job that just reached a terminal state
// (failed or timeout) should be retried. If the job has a retry policy with
// remaining attempts, it transitions failed/timeout → retrying → pending
// and sets the RetryAfter timestamp based on the backoff calculation.
//
// Returns true if the job was retried, false if it stays terminal.
func (s *JobStore) RetryIfEligible(ctx context.Context, jobID string) bool {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return false
	}

	if !isRetryable(j.Status) || !ShouldRetry(j) {
		s.mu.Unlock()
		return false
	}

	prevStatus := j.Status

	// Transition to retrying.
	j.Status = cpb.JobStatusRetrying
	if err := s.persister.SaveJob(ctx, j); err != nil {
		j.Status = prevStatus
		s.mu.Unlock()
		return false
	}

	s.log.Info("job entering retry",
		slog.String("job_id", jobID),
		slog.Uint64("attempt", uint64(j.Attempt)),
		slog.Uint64("max_attempts", uint64(j.RetryPolicy.MaxAttempts)),
	)

	// Increment attempt counter and calculate backoff delay.
	j.Attempt++
	delay := NextRetryDelay(j.RetryPolicy, j.Attempt)
	j.RetryAfter = time.Now().Add(delay)

	// Transition retrying → pending for re-dispatch.
	j.Status = cpb.JobStatusPending
	j.NodeID = ""          // clear previous node assignment
	j.DispatchedAt = time.Time{} // clear dispatch timestamp
	j.FinishedAt = time.Time{}   // clear finish timestamp
	j.Error = ""           // clear previous error

	if err := s.persister.SaveJob(ctx, j); err != nil {
		j.Status = cpb.JobStatusRetrying
		s.mu.Unlock()
		return false
	}

	s.mu.Unlock()

	s.appendAuditAsync("job.retry", "coordinator", jobID,
		fmt.Sprintf("attempt=%d max=%d delay=%s prev=%s",
			j.Attempt, j.RetryPolicy.MaxAttempts, delay, prevStatus))

	return true
}
