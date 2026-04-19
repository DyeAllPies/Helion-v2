// internal/cluster/shares.go
//
// Feature 38 — share-mutation primitives for JobStore and
// WorkflowStore. Kept in a dedicated file because neither
// store had an "update arbitrary field in place" primitive —
// the rest of the lifecycle goes through Transition /
// StateChange / Start / Cancel.
//
// Pattern
// ───────
// Read-modify-write under the store's existing mutex. Persist
// before releasing the mutex so in-memory + on-disk states
// stay consistent. This matches the ownership-preservation
// invariant from feature 36 — we do NOT mutate OwnerPrincipal
// or any other field; only Shares changes.

package cluster

import (
	"context"
	"fmt"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
)

// UpdateShares replaces j.Shares atomically and persists the
// result. Returns ErrJobNotFound when the job isn't in the
// store. A nil / empty shares slice is valid (revoke-all);
// callers that want idempotent revoke-on-missing-grantee
// should compute the replacement list and pass it here.
func (s *JobStore) UpdateShares(ctx context.Context, jobID string, shares []authz.Share) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}
	// Copy the slice so a caller mutating the passed-in slice
	// later does not silently change our state.
	if shares == nil {
		j.Shares = nil
	} else {
		j.Shares = append([]authz.Share(nil), shares...)
	}
	if err := s.persister.SaveJob(ctx, j); err != nil {
		return fmt.Errorf("UpdateShares persist: %w", err)
	}
	return nil
}

// UpdateShares on WorkflowStore. Same contract as
// JobStore.UpdateShares.
func (s *WorkflowStore) UpdateShares(ctx context.Context, workflowID string, shares []authz.Share) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, ok := s.workflows[workflowID]
	if !ok {
		return ErrWorkflowNotFound
	}
	if shares == nil {
		wf.Shares = nil
	} else {
		wf.Shares = append([]authz.Share(nil), shares...)
	}
	if err := s.persister.SaveWorkflow(ctx, wf); err != nil {
		return fmt.Errorf("UpdateShares persist: %w", err)
	}
	return nil
}
