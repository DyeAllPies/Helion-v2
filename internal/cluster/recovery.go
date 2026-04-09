// internal/cluster/recovery.go
//
// RecoveryManager implements the coordinator crash-recovery path described in
// §4.2 and §7 Phase 2 of the design document:
//
//   "On startup: load BadgerDB state, identify non-terminal jobs, enqueue into
//   retry pool with 15 s grace period, then dispatch to re-registered nodes."
//
// How it works
// ────────────
//  1. The coordinator calls JobStore.Restore() to load persisted jobs into
//     memory. Non-terminal jobs (pending / dispatching / running) are those
//     that were in-flight at shutdown — the node they were assigned to may or
//     may not rejoin.
//
//  2. RecoveryManager.Run() is called immediately after Restore(). It collects
//     those non-terminal jobs and waits for the configured grace period
//     (default 15 s) before attempting dispatch. This gives node agents time
//     to re-register and send a heartbeat so the Registry has healthy nodes
//     available.
//
//  3. After the grace period each non-terminal job is attempted via the
//     Dispatcher interface. If no healthy node is available the job is marked
//     lost (the caller can surface this). If dispatch succeeds the job
//     transitions pending→dispatching via the JobStore.
//
// Dispatcher interface
// ────────────────────
// RecoveryManager depends on a Dispatcher, not on the gRPC node client
// directly. This keeps the recovery logic testable without a real network:
// tests inject a fake Dispatcher. The production wiring in coordinator main()
// provides a real gRPC-backed implementation.
//
// Relationship to JobStore
// ────────────────────────
// RecoveryManager does NOT own the JobStore — it receives it as a dependency.
// The coordinator creates and owns the JobStore; recovery is one consumer.
//
// Context cancellation
// ────────────────────
// Run() respects ctx cancellation throughout: if the coordinator is asked to
// shut down during the grace period or during dispatch, Run() returns cleanly
// without attempting further work.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// DefaultGracePeriod is the time RecoveryManager waits after startup before
// attempting to dispatch recovered jobs. During this window node agents are
// expected to re-register and send their first heartbeat.
const DefaultGracePeriod = 15 * time.Second

// ── Dispatcher ───────────────────────────────────────────────────────────────

// Dispatcher is the interface RecoveryManager uses to assign a job to a node.
//
// The production implementation dials the chosen node's gRPC NodeService and
// calls Dispatch. Tests inject a synchronous fake.
//
// A Dispatcher implementation must:
//   - Select a healthy node (via the Scheduler or equivalent).
//   - Send the job to that node.
//   - Return the node ID that accepted the job, or an error.
//
// If no healthy node is available the implementation should return
// ErrNoHealthyNodes so the caller can mark the job lost.
type Dispatcher interface {
	Dispatch(ctx context.Context, job *cpb.Job) (nodeID string, err error)
}

// ── RecoveryManager ──────────────────────────────────────────────────────────

// RecoveryManager handles non-terminal jobs found in BadgerDB on coordinator
// startup.
type RecoveryManager struct {
	jobs        *JobStore
	dispatcher  Dispatcher
	gracePeriod time.Duration
	log         *slog.Logger
}

// NewRecoveryManager creates a RecoveryManager.
//
// gracePeriod is how long to wait before attempting dispatch after startup.
// Pass DefaultGracePeriod (15 s) for production; use a short value in tests.
func NewRecoveryManager(jobs *JobStore, d Dispatcher, gracePeriod time.Duration, log *slog.Logger) *RecoveryManager {
	if log == nil {
		log = slog.Default()
	}
	return &RecoveryManager{
		jobs:        jobs,
		dispatcher:  d,
		gracePeriod: gracePeriod,
		log:         log,
	}
}

// Run executes the crash-recovery sequence.
//
// It should be called once after JobStore.Restore() has loaded persisted state.
// Run blocks until all recovered jobs have been dispatched (or marked lost),
// or until ctx is cancelled.
//
// Callers typically run this in a goroutine:
//
//	go func() {
//	    if err := rm.Run(ctx); err != nil && ctx.Err() == nil {
//	        log.Error("crash recovery failed", "err", err)
//	    }
//	}()
func (r *RecoveryManager) Run(ctx context.Context) error {
	// Snapshot non-terminal jobs before the grace period so we don't pick up
	// jobs submitted after startup.
	toRecover := r.jobs.NonTerminal()

	if len(toRecover) == 0 {
		r.log.Info("crash recovery: no non-terminal jobs found")
		return nil
	}

	r.log.Info("crash recovery: waiting grace period before dispatch",
		slog.Int("jobs", len(toRecover)),
		slog.Duration("grace_period", r.gracePeriod),
	)

	// Wait out the grace period, honouring ctx cancellation.
	select {
	case <-ctx.Done():
		r.log.Info("crash recovery: cancelled during grace period")
		return nil
	case <-time.After(r.gracePeriod):
	}

	r.log.Info("crash recovery: grace period elapsed, dispatching recovered jobs",
		slog.Int("jobs", len(toRecover)),
	)

	var dispatched, lost int
	for _, job := range toRecover {
		if ctx.Err() != nil {
			r.log.Info("crash recovery: cancelled mid-dispatch",
				slog.Int("remaining", len(toRecover)-dispatched-lost),
			)
			break
		}

		if err := r.recoverOne(ctx, job); err != nil {
			r.log.Error("crash recovery: failed to recover job",
				slog.String("job_id", job.ID),
				slog.Any("err", err),
			)
			lost++
		} else {
			dispatched++
		}
	}

	r.log.Info("crash recovery: complete",
		slog.Int("dispatched", dispatched),
		slog.Int("lost", lost),
	)
	return nil
}

// recoverOne attempts to dispatch a single recovered job.
// If no healthy node is available it marks the job lost.
func (r *RecoveryManager) recoverOne(ctx context.Context, job *cpb.Job) error {
	// Re-fetch from the store — the job might have been updated since the
	// NonTerminal() snapshot (e.g. a late ReportResult from the original node).
	current, err := r.jobs.Get(job.ID)
	if err != nil {
		return fmt.Errorf("re-fetch job %s: %w", job.ID, err)
	}
	if current.Status.IsTerminal() {
		r.log.Info("crash recovery: job already terminal, skipping",
			slog.String("job_id", job.ID),
			slog.String("status", current.Status.String()),
		)
		return nil
	}

	nodeID, err := r.dispatcher.Dispatch(ctx, current)
	if err != nil {
		// No healthy node or dispatch RPC failed — mark lost.
		lostReason := fmt.Sprintf("crash recovery dispatch failed: %v", err)
		if markErr := r.jobs.MarkLost(ctx, job.ID, lostReason); markErr != nil {
			return fmt.Errorf("mark lost after dispatch failure: %w", markErr)
		}
		r.log.Warn("crash recovery: job marked lost",
			slog.String("job_id", job.ID),
			slog.String("reason", lostReason),
		)
		return nil // lost is a handled outcome, not a fatal error
	}

	// Transition: whatever state the job was in → dispatching (fresh start).
	// We go through pending first if the job wasn't already pending, because
	// the transition table requires pending→dispatching.
	// The simplest correct approach: if the job is not pending, reset it to
	// pending via MarkLost+re-submit is wrong (loses ID). Instead we use a
	// direct MarkLost approach only when dispatch actually fails. On success,
	// we accept that the job was already in dispatching/running and simply
	// force it back to dispatching with a new node assignment.
	//
	// Implementation: transition to pending first if needed, then dispatching.
	if current.Status != cpb.JobStatusPending {
		// Force back to pending so the normal pending→dispatching transition works.
		// resetToPending is unexported and lives in job.go — same package.
		if err := r.jobs.resetToPending(ctx, job.ID); err != nil {
			return fmt.Errorf("reset job %s to pending: %w", job.ID, err)
		}
	}

	if err := r.jobs.Transition(ctx, job.ID, cpb.JobStatusDispatching,
		TransitionOptions{NodeID: nodeID}); err != nil {
		return fmt.Errorf("transition job %s to dispatching: %w", job.ID, err)
	}

	r.log.Info("crash recovery: job redispatched",
		slog.String("job_id", job.ID),
		slog.String("node_id", nodeID),
	)
	return nil
}
