// internal/cluster/workflow_read.go
//
// Read-only WorkflowStore operations: Get, List, RunningWorkflowIDs, Restore.

package cluster

import (
	"context"
	"fmt"
	"log/slog"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Get returns a snapshot of the workflow with the given ID.
// Returns ErrWorkflowNotFound if the ID is unknown.
func (s *WorkflowStore) Get(workflowID string) (*cpb.Workflow, error) {
	s.mu.RLock()
	w, ok := s.workflows[workflowID]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrWorkflowNotFound
	}
	snap := *w
	snapJobs := make([]cpb.WorkflowJob, len(w.Jobs))
	copy(snapJobs, w.Jobs)
	snap.Jobs = snapJobs
	s.mu.RUnlock()
	return &snap, nil
}

// List returns snapshots of all workflows currently in the store.
func (s *WorkflowStore) List() []*cpb.Workflow {
	s.mu.RLock()
	out := make([]*cpb.Workflow, 0, len(s.workflows))
	for _, w := range s.workflows {
		snap := *w
		snapJobs := make([]cpb.WorkflowJob, len(w.Jobs))
		copy(snapJobs, w.Jobs)
		snap.Jobs = snapJobs
		out = append(out, &snap)
	}
	s.mu.RUnlock()
	return out
}

// RunningWorkflowIDs returns the IDs of all workflows in running state.
// Used by the dispatch loop to build the eligible job set.
func (s *WorkflowStore) RunningWorkflowIDs() []string {
	s.mu.RLock()
	var ids []string
	for _, w := range s.workflows {
		if w.Status == cpb.WorkflowStatusRunning {
			ids = append(ids, w.ID)
		}
	}
	s.mu.RUnlock()
	return ids
}

// Restore loads persisted workflows into memory on startup.
//
// Called once during coordinator boot, before any RPCs are served.
func (s *WorkflowStore) Restore(ctx context.Context) error {
	workflows, err := s.persister.LoadAllWorkflows(ctx)
	if err != nil {
		return fmt.Errorf("WorkflowStore.Restore: %w", err)
	}

	s.mu.Lock()
	for _, w := range workflows {
		s.workflows[w.ID] = w
	}
	s.mu.Unlock()

	s.log.Info("workflow store restored from persistence",
		slog.Int("total", len(workflows)),
	)
	return nil
}
