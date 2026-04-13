// internal/cluster/workflow_submit.go
//
// WorkflowStore.Submit and Start — workflow creation and job materialisation.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Submit validates the DAG structure and persists a new workflow in pending state.
// It does NOT create the individual jobs — that happens when the workflow is started.
func (s *WorkflowStore) Submit(ctx context.Context, w *cpb.Workflow) error {
	if len(w.Jobs) == 0 {
		return ErrWorkflowEmpty
	}

	// Validate DAG structure.
	if err := ValidateDAG(w.Jobs); err != nil {
		return fmt.Errorf("workflow %s: %w", w.ID, err)
	}

	w.Status = cpb.WorkflowStatusPending
	w.CreatedAt = time.Now()

	s.mu.Lock()
	if _, exists := s.workflows[w.ID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrWorkflowExists, w.ID)
	}
	s.workflows[w.ID] = w
	if err := s.persister.SaveWorkflow(ctx, w); err != nil {
		delete(s.workflows, w.ID)
		s.mu.Unlock()
		return fmt.Errorf("WorkflowStore.Submit persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Info("workflow submitted",
		slog.String("workflow_id", w.ID),
		slog.String("name", w.Name),
		slog.Int("job_count", len(w.Jobs)),
	)

	return nil
}

// Start creates real jobs in the JobStore for each WorkflowJob and transitions
// the workflow to running. Root jobs (no dependencies) start as pending;
// dependent jobs start as pending too but won't be dispatched until their
// dependencies are satisfied (checked by EligibleJobs).
func (s *WorkflowStore) Start(ctx context.Context, workflowID string, jobs *JobStore) error {
	s.mu.Lock()
	w, ok := s.workflows[workflowID]
	if !ok {
		s.mu.Unlock()
		return ErrWorkflowNotFound
	}
	if w.Status != cpb.WorkflowStatusPending {
		s.mu.Unlock()
		return fmt.Errorf("workflow %s: cannot start from %s state", workflowID, w.Status)
	}

	// Create a job in the JobStore for each workflow job.
	for i := range w.Jobs {
		wj := &w.Jobs[i]
		jobID := fmt.Sprintf("%s/%s", workflowID, wj.Name)
		wj.JobID = jobID

		// Job priority: use per-job override, fall back to workflow default.
		priority := w.Priority
		if wj.Priority > 0 {
			priority = wj.Priority
		}

		job := &cpb.Job{
			ID:             jobID,
			Command:        wj.Command,
			Args:           wj.Args,
			Env:            wj.Env,
			TimeoutSeconds: wj.TimeoutSeconds,
			Priority:       priority,
			WorkflowID:     workflowID,
		}

		if err := jobs.Submit(ctx, job); err != nil {
			// If any job creation fails, mark workflow as failed.
			w.Status = cpb.WorkflowStatusFailed
			w.Error = fmt.Sprintf("failed to create job %q: %v", wj.Name, err)
			w.FinishedAt = time.Now()
			_ = s.persister.SaveWorkflow(ctx, w)
			s.mu.Unlock()
			return fmt.Errorf("workflow %s: failed to create job %q: %w", workflowID, wj.Name, err)
		}
	}

	w.Status = cpb.WorkflowStatusRunning
	w.StartedAt = time.Now()
	if err := s.persister.SaveWorkflow(ctx, w); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("WorkflowStore.Start persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Info("workflow started",
		slog.String("workflow_id", workflowID),
		slog.Int("job_count", len(w.Jobs)),
	)

	return nil
}
