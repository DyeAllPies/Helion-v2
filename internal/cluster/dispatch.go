// internal/cluster/dispatch.go
//
// DispatchLoop polls the job store for pending jobs and dispatches them
// to healthy nodes via the scheduler and gRPC Dispatch RPC.

package cluster

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
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

	// unschedulableLastEmit debounces `job.unschedulable` so a job
	// that can't match its selector doesn't re-emit the event on
	// every tick (~10/sec at the default 100ms interval). Keyed by
	// job ID; values are the timestamp of the most recent emit.
	// Entries are cleared lazily when a job leaves the pending
	// queue (detected by its absence from the next scan).
	unschedulableLastEmit map[string]time.Time
}

// unschedulableEmitCooldown bounds the re-emit rate of the
// job.unschedulable event per job. Long enough that an operator
// alert fires at most every 30s per stuck job, short enough that
// recovery (operator fixes a label) is observable promptly.
const unschedulableEmitCooldown = 30 * time.Second

// maybeEmitUnschedulable publishes a job.unschedulable event for job
// unless the same job emitted one within unschedulableEmitCooldown.
// The debounce state is reset in dispatchPending whenever a job
// successfully picks a node, so recovery is observable.
func (d *DispatchLoop) maybeEmitUnschedulable(job *cpb.Job) {
	now := time.Now()
	if last, ok := d.unschedulableLastEmit[job.ID]; ok && now.Sub(last) < unschedulableEmitCooldown {
		return
	}
	d.unschedulableLastEmit[job.ID] = now
	d.log.Info("dispatch: no node matches job node_selector",
		slog.String("job_id", job.ID),
		slog.Any("selector", job.NodeSelector))
	d.jobs.publishEvent(events.JobUnschedulable(job.ID, job.NodeSelector))
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

		unschedulableLastEmit: make(map[string]time.Time),
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
	jobs := d.jobs.PendingByPriority()
	for _, job := range jobs {

		// Skip jobs in backoff window (waiting for retry delay to expire).
		if !job.RetryAfter.IsZero() && now.Before(job.RetryAfter) {
			continue
		}

		// If this job belongs to a workflow, check dependency eligibility.
		if job.WorkflowID != "" && !eligible[job.ID] {
			continue
		}

		// Step-3 artifact resolution: rewrite any `from:
		// <upstream>.<output>` input references to concrete URIs
		// drawn from the upstream job's ResolvedOutputs. Runs only
		// for workflow jobs carrying at least one From ref — the
		// resolver short-circuits otherwise. A resolution failure
		// means the upstream never produced the named output (or
		// ancestor dependency was skipped); the downstream job
		// cannot run, so transition to Failed with a descriptive
		// error instead of dispatching a half-specified job.
		resolvedJob, rerr := ResolveJobInputs(job, d.jobs)
		if rerr != nil {
			d.log.Warn("dispatch: artifact resolution failed",
				slog.String("job_id", job.ID), slog.Any("err", rerr))
			_ = d.jobs.Transition(ctx, job.ID, cpb.JobStatusFailed, TransitionOptions{
				ErrMsg: "artifact resolution: " + rerr.Error(),
			})
			continue
		}
		// Persist the rewritten Inputs so /api/jobs/{id} shows the
		// concrete URIs the node received. The From field is
		// preserved on each entry; both sides of the lineage stay
		// queryable after dispatch. Only persist when the resolver
		// actually made changes (pointer equality is how the
		// resolver signals "no From refs, no copy").
		if resolvedJob != job {
			if perr := d.jobs.UpdateResolvedInputs(ctx, job.ID, resolvedJob.Inputs); perr != nil {
				d.log.Warn("dispatch: persist resolved inputs failed",
					slog.String("job_id", job.ID), slog.Any("err", perr))
				_ = d.jobs.Transition(ctx, job.ID, cpb.JobStatusFailed, TransitionOptions{
					ErrMsg: "persist resolved inputs: " + perr.Error(),
				})
				continue
			}
			job = resolvedJob
		}

		node, err := d.scheduler.PickForSelector(job.NodeSelector)
		if err != nil {
			switch {
			case errors.Is(err, ErrNoNodeMatchesSelector):
				// Healthy nodes exist but none satisfy the selector.
				// Job stays pending — retrying without operator
				// intervention won't invent labels, but leaving it
				// queued means a newly-registered matching node
				// picks it up automatically. The event is the
				// diagnostic signal operators watch for; debounced
				// so a stuck job doesn't spam the bus.
				d.maybeEmitUnschedulable(job)
				continue
			default:
				// No healthy nodes at all — stop trying until the
				// next tick so we don't burn through every pending
				// job on a transient registry outage.
				d.log.Debug("dispatch: no healthy nodes, will retry",
					slog.String("job_id", job.ID))
				return
			}
		}
		// Picked a node — clear any debounce state so a future
		// unschedulable transition (e.g. the matching node goes
		// unhealthy) re-emits promptly instead of being throttled.
		delete(d.unschedulableLastEmit, job.ID)

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
