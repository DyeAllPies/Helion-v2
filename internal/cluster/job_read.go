// internal/cluster/job_read.go
//
// Read-only JobStore operations: Get, List, NonTerminal, Restore, and the
// Phase 4 metrics counters (GetJobsByStatus, CountByStatus, CountTotal).

package cluster

import (
	"context"
	"fmt"
	"log/slog"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Get returns a snapshot of the job with the given ID.
// Returns ErrJobNotFound if the ID is unknown.
func (s *JobStore) Get(jobID string) (*cpb.Job, error) {
	s.mu.RLock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrJobNotFound
	}
	snap := *j // copy while holding the lock — prevents races with Transition
	s.mu.RUnlock()
	return &snap, nil
}

// List returns snapshots of all jobs currently in the store.
func (s *JobStore) List() []*cpb.Job {
	s.mu.RLock()
	out := make([]*cpb.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		snap := *j
		out = append(out, &snap)
	}
	s.mu.RUnlock()
	return out
}

// NonTerminal returns all jobs that have not yet reached a terminal state.
// Used by crash recovery to build the retry queue.
func (s *JobStore) NonTerminal() []*cpb.Job {
	s.mu.RLock()
	var out []*cpb.Job
	for _, j := range s.jobs {
		if !j.Status.IsTerminal() {
			snap := *j
			out = append(out, &snap)
		}
	}
	s.mu.RUnlock()
	return out
}

// Restore loads persisted jobs into memory on startup.
//
// Called once during coordinator boot, before any RPCs are served.
// Jobs that were non-terminal at shutdown are loaded in their persisted state;
// the caller (crash recovery) then decides whether to requeue or mark lost.
func (s *JobStore) Restore(ctx context.Context) error {
	jobs, err := s.persister.LoadAllJobs(ctx)
	if err != nil {
		return fmt.Errorf("JobStore.Restore: %w", err)
	}

	s.mu.Lock()
	for _, j := range jobs {
		s.jobs[j.ID] = j
	}
	s.mu.Unlock()

	s.log.Info("job store restored from persistence",
		slog.Int("total", len(jobs)),
	)
	return nil
}

// ── Phase 4: metrics counters ─────────────────────────────────────────────────

// GetJobsByStatus returns all jobs with the given status.
// This is used by the metrics provider to count jobs by status.
func (s *JobStore) GetJobsByStatus(ctx context.Context, status string) ([]*cpb.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Parse status string to protobuf enum.
	var targetStatus cpb.JobStatus
	switch status {
	case "PENDING":
		targetStatus = cpb.JobStatusPending
	case "DISPATCHING":
		targetStatus = cpb.JobStatusDispatching
	case "RUNNING":
		targetStatus = cpb.JobStatusRunning
	case "COMPLETED":
		targetStatus = cpb.JobStatusCompleted
	case "FAILED":
		targetStatus = cpb.JobStatusFailed
	case "TIMEOUT":
		targetStatus = cpb.JobStatusTimeout
	case "LOST":
		targetStatus = cpb.JobStatusLost
	case "RETRYING":
		targetStatus = cpb.JobStatusRetrying
	default:
		return nil, fmt.Errorf("unknown job status: %s", status)
	}

	var result []*cpb.Job
	for _, j := range s.jobs {
		if j.Status == targetStatus {
			result = append(result, j)
		}
	}
	return result, nil
}

// CountByStatus returns the number of jobs with the given status.
// Implements metrics.JobCounter interface.
func (s *JobStore) CountByStatus(ctx context.Context, status string) (int, error) {
	jobs, err := s.GetJobsByStatus(ctx, status)
	if err != nil {
		return 0, err
	}
	return len(jobs), nil
}

// CountTotal returns the total number of jobs.
// Implements metrics.JobCounter interface.
func (s *JobStore) CountTotal(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs), nil
}
