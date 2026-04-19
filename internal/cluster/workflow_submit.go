// internal/cluster/workflow_submit.go
//
// WorkflowStore.Submit and Start — workflow creation and job materialisation.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
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
			// Step 2 ML pipeline fields — carried verbatim from the
			// workflow template onto each materialised Job so the
			// dispatcher + node-side stager see the same bindings a
			// standalone submit would produce. Step 3 will resolve
			// `from: <upstream>.<name>` references against earlier
			// jobs' ResolvedOutputs just before Submit is called.
			WorkingDir:   wj.WorkingDir,
			Inputs:       wj.Inputs,
			Outputs:      wj.Outputs,
			NodeSelector: wj.NodeSelector,
			// Feature 26 — propagate secret-key flags through to the
			// materialised Job so GET /jobs/{wf-id}/{name} redacts the
			// same values the workflow submit declared.
			SecretKeys: wj.SecretKeys,
			// Feature 36 — inherit the workflow's owner onto every
			// child job. Feature 37's authz engine reads
			// Job.OwnerPrincipal, so per-child ownership must match
			// the parent workflow's owner or a legitimate submitter
			// would lose access to their own workflow's children.
			// Note: this runs in the JobStore.Submit path called
			// from WorkflowStore.Start — the acting principal for
			// audit purposes is service:workflow_runner, but the
			// resource owner stays the workflow's submitter.
			OwnerPrincipal: w.OwnerPrincipal,
			// SubmittedBy also inherits for back-compat with the
			// AUDIT L1 pre-feature-37 RBAC check. Legacy workflows
			// (before feature 36) have empty OwnerPrincipal and
			// empty SubmittedBy; the backfill on load synthesises a
			// sensible value for both.
			SubmittedBy: principal.SubjectFromID(w.OwnerPrincipal),
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
