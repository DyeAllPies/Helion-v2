// internal/cluster/job_transition.go
//
// JobStore state-change operations: Transition (normal lifecycle), MarkLost
// (crash recovery), and resetToPending (recovery re-entry).

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Transition moves a job from its current state to the target state.
//
// Rules:
//   - Only transitions listed in allowedTransitions are accepted.
//   - Transitioning a terminal job returns ErrJobAlreadyTerminal.
//   - An unknown job ID returns ErrJobNotFound.
//
// The new state is persisted inside the write lock before returning.
// A "job.transition" audit record is written asynchronously.
func (s *JobStore) Transition(ctx context.Context, jobID string, to cpb.JobStatus, opts TransitionOptions) error {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}

	from := j.Status
	if from.IsTerminal() {
		s.mu.Unlock()
		return fmt.Errorf("%w: job %s is %s", ErrJobAlreadyTerminal, jobID, from)
	}
	if !isAllowed(from, to) {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s → %s not permitted", ErrInvalidTransition, from, to)
	}

	// Apply mutation.
	j.Status = to
	now := time.Now()
	switch to {
	case cpb.JobStatusDispatching:
		j.DispatchedAt = now
		if opts.NodeID != "" {
			j.NodeID = opts.NodeID
		}
	case cpb.JobStatusCompleted, cpb.JobStatusFailed, cpb.JobStatusTimeout:
		j.FinishedAt = now
		j.ExitCode = opts.ExitCode
		if opts.ErrMsg != "" {
			j.Error = opts.ErrMsg
		}
	}

	if err := s.persister.SaveJob(ctx, j); err != nil {
		// Roll back the in-memory mutation on persist failure so callers do
		// not observe a state that is not on disk.
		j.Status = from
		s.mu.Unlock()
		return fmt.Errorf("JobStore.Transition persist: %w", err)
	}

	// Take a snapshot for the audit goroutine before releasing the lock.
	snap := *j
	s.mu.Unlock()

	s.log.Info("job state transition",
		slog.String("job_id", jobID),
		slog.String("from", from.String()),
		slog.String("to", to.String()),
	)

	detail := fmt.Sprintf("from=%s to=%s", from, to)
	if opts.NodeID != "" {
		detail += " node=" + opts.NodeID
	}
	if snap.Error != "" {
		detail += " error=" + snap.Error
	}
	s.appendAuditAsync("job.transition", "coordinator", jobID, detail)

	return nil
}

// MarkLost forcibly moves a job to the lost terminal state.
//
// This is the crash-recovery path: on coordinator restart, jobs that were
// in-flight (pending, dispatching, running) and have no node to complete them
// are marked lost. The normal transition table is bypassed — lost is reachable
// from any non-terminal state.
func (s *JobStore) MarkLost(ctx context.Context, jobID string, reason string) error {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if j.Status.IsTerminal() {
		s.mu.Unlock()
		return nil // already terminal — idempotent
	}

	prev := j.Status
	j.Status = cpb.JobStatusLost
	j.FinishedAt = time.Now()
	j.Error = reason

	if err := s.persister.SaveJob(ctx, j); err != nil {
		j.Status = prev
		s.mu.Unlock()
		return fmt.Errorf("JobStore.MarkLost persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Warn("job marked lost",
		slog.String("job_id", jobID),
		slog.String("prev_status", prev.String()),
		slog.String("reason", reason),
	)

	s.appendAuditAsync("job.lost", "coordinator", jobID,
		fmt.Sprintf("prev=%s reason=%s", prev, reason))

	return nil
}

// resetToPending forcibly sets a non-terminal job's status back to pending so
// that the normal pending→dispatching transition can be applied.
//
// This is used exclusively by RecoveryManager to re-enter the dispatch pipeline
// for a job that was in-flight (dispatching or running) at coordinator shutdown.
// It is NOT a normal lifecycle transition — it bypasses the transition table
// intentionally and is therefore unexported.
func (s *JobStore) resetToPending(ctx context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}
	if j.Status == cpb.JobStatusPending {
		return nil // already pending, nothing to do
	}
	if j.Status.IsTerminal() {
		return nil // terminal jobs are not re-entered
	}

	j.Status = cpb.JobStatusPending
	if err := s.persister.SaveJob(ctx, j); err != nil {
		j.Status = cpb.JobStatusDispatching // best-effort rollback
		return fmt.Errorf("resetToPending persist: %w", err)
	}
	return nil
}
