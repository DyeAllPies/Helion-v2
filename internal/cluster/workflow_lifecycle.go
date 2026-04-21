// internal/cluster/workflow_lifecycle.go
//
// WorkflowStore lifecycle operations: EligibleJobs (dependency resolution),
// OnJobCompleted (cascading failure + workflow completion), Cancel.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// EligibleJobs returns the job IDs from the given workflow that are ready for
// dispatch — i.e., all of their upstream dependencies have completed
// successfully (or match the dependency condition).
//
// Jobs already dispatched/running/completed are excluded.
func (s *WorkflowStore) EligibleJobs(workflowID string, jobs *JobStore) []string {
	s.mu.RLock()
	w, ok := s.workflows[workflowID]
	if !ok || w.Status != cpb.WorkflowStatusRunning {
		s.mu.RUnlock()
		return nil
	}

	// Build name → WorkflowJob map.
	wjByName := make(map[string]*cpb.WorkflowJob, len(w.Jobs))
	for i := range w.Jobs {
		wjByName[w.Jobs[i].Name] = &w.Jobs[i]
	}
	s.mu.RUnlock()

	var eligible []string
	for _, wj := range wjByName {
		if wj.JobID == "" {
			continue
		}

		// Check the current state of this job.
		job, err := jobs.Get(wj.JobID)
		if err != nil {
			continue
		}

		// Only pending jobs are candidates for dispatch.
		if job.Status != cpb.JobStatusPending {
			continue
		}

		// Check if all dependencies are satisfied.
		allSatisfied := true
		for _, depName := range wj.DependsOn {
			depWJ, ok := wjByName[depName]
			if !ok || depWJ.JobID == "" {
				allSatisfied = false
				break
			}

			depJob, err := jobs.Get(depWJ.JobID)
			if err != nil {
				allSatisfied = false
				break
			}

			if !isDependencySatisfied(depJob, wj.Condition) {
				allSatisfied = false
				break
			}
		}

		if allSatisfied {
			eligible = append(eligible, wj.JobID)
		}
	}

	return eligible
}

// isDependencySatisfied checks if a dependency job's status satisfies the
// given condition for the downstream job.
func isDependencySatisfied(depJob *cpb.Job, condition cpb.DependencyCondition) bool {
	switch condition {
	case cpb.DependencyOnSuccess:
		return depJob.Status == cpb.JobStatusCompleted
	case cpb.DependencyOnFailure:
		return depJob.Status == cpb.JobStatusFailed || depJob.Status == cpb.JobStatusTimeout
	case cpb.DependencyOnComplete:
		return depJob.Status.IsTerminal()
	default:
		return depJob.Status == cpb.JobStatusCompleted
	}
}

// OnJobCompleted is called when any job reaches a terminal state. It checks if
// the job belongs to a workflow and handles:
//   - Cascading skip for downstream jobs on failure (when condition is on_success)
//   - Marking the workflow as completed when all jobs are done
//   - Marking the workflow as failed when a required job fails
func (s *WorkflowStore) OnJobCompleted(ctx context.Context, jobID string, jobStatus cpb.JobStatus, jobs *JobStore) {
	// Find which workflow this job belongs to.
	s.mu.Lock()
	var targetWorkflow *cpb.Workflow
	var targetJobName string

	for _, w := range s.workflows {
		if w.Status != cpb.WorkflowStatusRunning {
			continue
		}
		for _, wj := range w.Jobs {
			if wj.JobID == jobID {
				targetWorkflow = w
				targetJobName = wj.Name
				break
			}
		}
		if targetWorkflow != nil {
			break
		}
	}

	if targetWorkflow == nil {
		s.mu.Unlock()
		return
	}

	workflowID := targetWorkflow.ID

	// Handle cascading failures: if this job failed and downstream jobs depend
	// on it with on_success condition, mark them as failed too.
	if jobStatus == cpb.JobStatusFailed || jobStatus == cpb.JobStatusTimeout || jobStatus == cpb.JobStatusLost {
		descendants := Descendants(targetWorkflow.Jobs, targetJobName)
		for _, descName := range descendants {
			for _, wj := range targetWorkflow.Jobs {
				if wj.Name == descName && wj.JobID != "" {
					// Only cascade-skip jobs whose condition requires success.
					if wj.Condition == cpb.DependencyOnSuccess {
						// Use skipped state for DAG cascade (not lost).
						_ = jobs.Transition(ctx, wj.JobID, cpb.JobStatusSkipped, TransitionOptions{
							ErrMsg: fmt.Sprintf("upstream job %q %s", targetJobName, jobStatus),
						})
					}
				}
			}
		}
	}

	// Check if all workflow jobs are now in a terminal state, and
	// build feature-40 analytics counts in the same pass so we
	// don't hold the job-store mutex twice.
	allTerminal := true
	anyFailed := false
	jobCount := 0
	successCount := 0
	failedCount := 0
	for _, wj := range targetWorkflow.Jobs {
		jobCount++
		if wj.JobID == "" {
			continue
		}
		j, err := jobs.Get(wj.JobID)
		if err != nil {
			allTerminal = false
			continue
		}
		if !j.Status.IsTerminal() {
			allTerminal = false
			break
		}
		switch j.Status {
		case cpb.JobStatusCompleted:
			successCount++
		case cpb.JobStatusFailed, cpb.JobStatusTimeout, cpb.JobStatusLost, cpb.JobStatusSkipped:
			anyFailed = true
			failedCount++
		}
	}

	var emitCompleted, emitFailed bool
	var failedJobName string
	if allTerminal {
		if anyFailed {
			targetWorkflow.Status = cpb.WorkflowStatusFailed
			emitFailed = true
			// Use this job as the failed-job attribution if it was the
			// failing one; otherwise find the first non-successful job.
			if jobStatus == cpb.JobStatusFailed || jobStatus == cpb.JobStatusTimeout || jobStatus == cpb.JobStatusLost {
				failedJobName = targetJobName
			} else {
				for _, wj := range targetWorkflow.Jobs {
					if wj.JobID == "" {
						continue
					}
					j, err := jobs.Get(wj.JobID)
					if err == nil &&
						(j.Status == cpb.JobStatusFailed || j.Status == cpb.JobStatusTimeout || j.Status == cpb.JobStatusLost) {
						failedJobName = wj.Name
						break
					}
				}
			}
		} else {
			targetWorkflow.Status = cpb.WorkflowStatusCompleted
			emitCompleted = true
		}
		targetWorkflow.FinishedAt = time.Now()
		_ = s.persister.SaveWorkflow(ctx, targetWorkflow)

		s.log.Info("workflow finished",
			slog.String("workflow_id", workflowID),
			slog.String("status", targetWorkflow.Status.String()),
			slog.Int("jobs", jobCount),
			slog.Int("success", successCount),
			slog.Int("failed", failedCount),
		)
	}

	// Snapshot event-payload inputs before releasing the lock so
	// the feature-40 constructors see a consistent view. Tags are
	// NOT currently carried on the Workflow type (only per-Job);
	// passing nil here is the stable contract — a future tags-on-
	// workflow feature can just populate the map without touching
	// the event constructor.
	ownerPrincipal := targetWorkflow.OwnerPrincipal

	s.mu.Unlock()

	// Publish after releasing the lock so subscribers never contend with us.
	if s.eventBus != nil {
		if emitCompleted {
			s.eventBus.Publish(events.WorkflowCompletedWithCounts(
				workflowID, ownerPrincipal,
				jobCount, successCount, failedCount,
				nil, // tags (reserved for future workflow-level tags)
			))
		}
		if emitFailed {
			s.eventBus.Publish(events.WorkflowFailedWithCounts(
				workflowID, failedJobName, ownerPrincipal,
				jobCount, successCount, failedCount,
				nil, // tags (reserved for future workflow-level tags)
			))
		}
	}
}

// Cancel transitions a running workflow to cancelled and marks all its
// non-terminal jobs as lost.
func (s *WorkflowStore) Cancel(ctx context.Context, workflowID string, jobs *JobStore) error {
	s.mu.Lock()
	w, ok := s.workflows[workflowID]
	if !ok {
		s.mu.Unlock()
		return ErrWorkflowNotFound
	}
	if w.Status.IsTerminal() {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s is %s", ErrWorkflowAlreadyTerminal, workflowID, w.Status)
	}

	// Cancel all non-terminal jobs.
	for _, wj := range w.Jobs {
		if wj.JobID != "" {
			_ = jobs.MarkLost(ctx, wj.JobID, "workflow cancelled")
		}
	}

	w.Status = cpb.WorkflowStatusCancelled
	w.FinishedAt = time.Now()
	if err := s.persister.SaveWorkflow(ctx, w); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("WorkflowStore.Cancel persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Info("workflow cancelled", slog.String("workflow_id", workflowID))
	return nil
}
