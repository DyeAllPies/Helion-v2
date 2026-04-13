// internal/cluster/dispatch.go
//
// DispatchLoop polls the job store for pending jobs and dispatches them
// to healthy nodes via the scheduler and gRPC Dispatch RPC.

package cluster

import (
	"context"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// NodeDispatcher sends a job to a specific node via gRPC.
// Implemented by the coordinator's gRPC-to-node client layer.
type NodeDispatcher interface {
	// DispatchToNode sends a job to the node at the given address.
	// Returns an error if the node rejects the job or is unreachable.
	DispatchToNode(ctx context.Context, nodeAddr string, job *cpb.Job) error
}

// DispatchLoop periodically polls for pending jobs and dispatches them.
type DispatchLoop struct {
	jobs       *JobStore
	workflows  *WorkflowStore // nil if workflow support is not enabled
	scheduler  *Scheduler
	dispatcher NodeDispatcher
	interval   time.Duration
	log        *slog.Logger
}

// NewDispatchLoop creates a new dispatch loop.
func NewDispatchLoop(
	jobs *JobStore,
	scheduler *Scheduler,
	dispatcher NodeDispatcher,
	interval time.Duration,
	log *slog.Logger,
) *DispatchLoop {
	return &DispatchLoop{
		jobs:       jobs,
		scheduler:  scheduler,
		dispatcher: dispatcher,
		interval:   interval,
		log:        log,
	}
}

// SetWorkflowStore attaches a WorkflowStore to the dispatch loop, enabling
// dependency-aware dispatch for workflow jobs.
func (d *DispatchLoop) SetWorkflowStore(ws *WorkflowStore) {
	d.workflows = ws
}

// Run starts the dispatch loop. It blocks until ctx is cancelled.
func (d *DispatchLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	// Dispatch immediately on startup, then on each tick.
	d.dispatchPending(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.dispatchPending(ctx)
		}
	}
}

// buildEligibleSet returns the set of workflow job IDs that are eligible for
// dispatch (all dependencies satisfied). Standalone jobs (no workflow) are not
// included — they are always eligible and checked separately.
func (d *DispatchLoop) buildEligibleSet() map[string]bool {
	if d.workflows == nil {
		return nil
	}
	eligible := make(map[string]bool)
	for _, wfID := range d.workflows.RunningWorkflowIDs() {
		for _, jobID := range d.workflows.EligibleJobs(wfID, d.jobs) {
			eligible[jobID] = true
		}
	}
	return eligible
}

func (d *DispatchLoop) dispatchPending(ctx context.Context) {
	// Build set of workflow-eligible job IDs so we can skip blocked jobs.
	eligible := d.buildEligibleSet()

	now := time.Now()
	jobs := d.jobs.List()
	for _, job := range jobs {
		if job.Status != cpb.JobStatusPending {
			continue
		}

		// Skip jobs in backoff window (waiting for retry delay to expire).
		if !job.RetryAfter.IsZero() && now.Before(job.RetryAfter) {
			continue
		}

		// If this job belongs to a workflow, check dependency eligibility.
		if job.WorkflowID != "" && !eligible[job.ID] {
			continue
		}

		node, err := d.scheduler.Pick()
		if err != nil {
			// No healthy nodes — stop trying until next tick
			d.log.Debug("dispatch: no healthy nodes, will retry",
				slog.String("job_id", job.ID))
			return
		}

		// Transition pending → scheduled (node picked, RPC not yet sent).
		opts := TransitionOptions{NodeID: node.NodeID}
		if err := d.jobs.Transition(ctx, job.ID, cpb.JobStatusScheduled, opts); err != nil {
			d.log.Warn("dispatch: schedule transition failed",
				slog.String("job_id", job.ID), slog.Any("err", err))
			continue
		}

		// Transition scheduled → dispatching (RPC in flight).
		if err := d.jobs.Transition(ctx, job.ID, cpb.JobStatusDispatching, opts); err != nil {
			d.log.Warn("dispatch: dispatching transition failed",
				slog.String("job_id", job.ID), slog.Any("err", err))
			continue
		}

		// Send to node
		if err := d.dispatcher.DispatchToNode(ctx, node.Address, job); err != nil {
			d.log.Warn("dispatch: send to node failed",
				slog.String("job_id", job.ID),
				slog.String("node_id", node.NodeID),
				slog.Any("err", err))
			// Mark as failed since we already transitioned to dispatching
			_ = d.jobs.Transition(ctx, job.ID, cpb.JobStatusFailed, TransitionOptions{
				ErrMsg: "dispatch failed: " + err.Error(),
			})
			continue
		}

		d.log.Info("job dispatched",
			slog.String("job_id", job.ID),
			slog.String("node_id", node.NodeID),
			slog.String("node_addr", node.Address),
		)
	}
}
