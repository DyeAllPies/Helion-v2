// internal/cluster/job_cancel.go
//
// JobStore.CancelJob transitions a non-terminal job to cancelled.

package cluster

import (
	"context"
	"fmt"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// CancelJob transitions a job to cancelled from any non-terminal state.
// Returns ErrJobNotFound if the ID is unknown, ErrJobAlreadyTerminal if
// already in a terminal state.
func (s *JobStore) CancelJob(ctx context.Context, jobID, reason string) error {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if j.Status.IsTerminal() {
		s.mu.Unlock()
		return fmt.Errorf("%w: job %s is %s", ErrJobAlreadyTerminal, jobID, j.Status)
	}

	// For non-terminal states that don't have a direct cancelled transition
	// (e.g. dispatching, retrying), use MarkLost-style bypass.
	if !isAllowed(j.Status, cpb.JobStatusCancelled) {
		// Force cancel via lost path for states like dispatching/retrying.
		s.mu.Unlock()
		return s.MarkLost(ctx, jobID, "cancelled: "+reason)
	}

	opts := TransitionOptions{ErrMsg: reason}
	s.mu.Unlock()
	return s.Transition(ctx, jobID, cpb.JobStatusCancelled, opts)
}
