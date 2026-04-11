// internal/cluster/job_submit.go
//
// JobStore.Submit inserts a new job in the pending state, persists it, and
// writes a "job.submitted" audit record asynchronously.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Submit inserts a new job in the pending state.
//
// The job is persisted before this call returns. An audit event
// "job.submitted" is written asynchronously.
func (s *JobStore) Submit(ctx context.Context, j *cpb.Job) error {
	j.Status = cpb.JobStatusPending
	j.CreatedAt = time.Now()

	s.mu.Lock()
	if _, exists := s.jobs[j.ID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrJobExists, j.ID)
	}
	s.jobs[j.ID] = j
	// Persist inside the lock so the in-memory and on-disk states are always
	// consistent — no reader can observe the new job before it is durable.
	if err := s.persister.SaveJob(ctx, j); err != nil {
		delete(s.jobs, j.ID)
		s.mu.Unlock()
		return fmt.Errorf("JobStore.Submit persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Info("job submitted",
		slog.String("job_id", j.ID),
		slog.String("command", j.Command),
	)

	s.appendAuditAsync("job.submitted", "coordinator", j.ID,
		fmt.Sprintf("command=%q", j.Command))

	return nil
}
