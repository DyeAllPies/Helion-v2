// internal/cluster/job.go
//
// Job lifecycle — full state machine implementation.
//
// State machine
// ─────────────
//   pending → dispatching → running → completed
//                                   → failed
//                                   → timeout
//             dispatching → failed   (dispatch RPC error)
//   any non-terminal → lost         (crash recovery)
//
// Concurrency
// ───────────
// JobStore is the in-memory index of jobs, protected by a sync.RWMutex.
// Every state transition goes through Transition(), which:
//   1. Holds the write lock while validating and applying the change.
//   2. Persists atomically inside the BadgerDB transaction.
//   3. Appends an audit record (async — does not block the transition).
//
// The lock is held only while updating in-memory state and calling the
// persister's SaveJob.  The audit append is fired in a goroutine so that
// a slow audit write never stalls a dispatch path.
//
// BadgerDB transaction semantics
// ──────────────────────────────
// SaveJob wraps its write in a single BadgerDB read-write transaction.
// BadgerDB guarantees that the transaction either commits fully or not at all.
// This satisfies the design requirement: "all transitions persisted atomically".
//
// Valid transitions
// ─────────────────
//   From          To (allowed)
//   ─────────     ─────────────────────────────────────────────
//   pending       dispatching
//   dispatching   running, failed
//   running       completed, failed, timeout
//   any           lost  (crash recovery only; skips normal validation)
//
// Attempting any other transition returns ErrInvalidTransition.
//
// File layout
// ───────────
//   job.go               — errors, interfaces, JobStore type, transition table
//   job_submit.go        — Submit
//   job_transition.go    — Transition, MarkLost, resetToPending
//   job_read.go          — Get, List, NonTerminal, Restore, counters
//   mem_job_persister.go — MemJobPersister (test helper)

package cluster

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrJobNotFound is returned when a job ID does not exist in the store.
var ErrJobNotFound = errors.New("job: not found")

// ErrJobExists is returned when Submit is called with an ID that is already
// present in the store. Callers should surface this as HTTP 409 Conflict.
//
// AUDIT M8 (fixed): previously Submit would silently overwrite an existing job
// or return an opaque 500 error. Now it returns ErrJobExists so the API layer
// can return a deterministic 409 response and callers can implement idempotent
// retry logic using stable IDs.
var ErrJobExists = errors.New("job: id already exists")

// ErrInvalidTransition is returned when a transition is not allowed from the
// job's current state.
var ErrInvalidTransition = errors.New("job: invalid state transition")

// ErrJobAlreadyTerminal is returned when a transition is attempted on a job
// that has already reached a terminal state.
var ErrJobAlreadyTerminal = errors.New("job: already in terminal state")

// ── JobPersister ──────────────────────────────────────────────────────────────

// JobPersister is the narrow storage interface used by JobStore.
// Tests inject MemJobPersister; production uses BadgerJobPersister.
type JobPersister interface {
	SaveJob(ctx context.Context, j *cpb.Job) error
	LoadAllJobs(ctx context.Context) ([]*cpb.Job, error)
	AppendAudit(ctx context.Context, eventType, actor, target, detail string) error
}

// ── valid transition table ────────────────────────────────────────────────────

// allowedTransitions lists the valid (from → to) pairs for normal transitions.
// The "lost" terminal is applied directly by MarkLost and bypasses this table.
var allowedTransitions = map[cpb.JobStatus][]cpb.JobStatus{
	cpb.JobStatusPending:     {cpb.JobStatusDispatching},
	cpb.JobStatusDispatching: {cpb.JobStatusRunning, cpb.JobStatusFailed},
	cpb.JobStatusRunning:     {cpb.JobStatusCompleted, cpb.JobStatusFailed, cpb.JobStatusTimeout},
}

func isAllowed(from, to cpb.JobStatus) bool {
	for _, allowed := range allowedTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ── TransitionOptions ─────────────────────────────────────────────────────────

// TransitionOptions carries optional metadata for a state transition.
// Zero value is valid for any transition.
type TransitionOptions struct {
	NodeID   string // set when dispatching to record which node received the job
	ExitCode int32  // set on completed / failed
	ErrMsg   string // human-readable error, set on failed / timeout / lost
}

// ── JobStore ──────────────────────────────────────────────────────────────────

// JobStore is the coordinator's in-memory index of all jobs.
// It is the single source of truth for job state during coordinator uptime.
// State is durable because every mutation is persisted before returning.
type JobStore struct {
	mu        sync.RWMutex
	jobs      map[string]*cpb.Job // keyed by job ID
	persister JobPersister
	log       *slog.Logger

	// auditWG tracks fire-and-forget background goroutines so Close can
	// wait for them during graceful shutdown. See AUDIT 2026-04-11/M1
	// (writes ran under context.Background() with no timeout and no
	// shutdown join).
	auditWG sync.WaitGroup
}

// auditWriteTimeout caps each fire-and-forget audit write so a stalled
// persister cannot leak goroutines or hold up shutdown.
const auditWriteTimeout = 5 * time.Second

// NewJobStore creates a JobStore backed by the given persister.
func NewJobStore(p JobPersister, log *slog.Logger) *JobStore {
	if log == nil {
		log = slog.Default()
	}
	return &JobStore{
		jobs:      make(map[string]*cpb.Job),
		persister: p,
		log:       log,
	}
}

// appendAuditAsync runs AppendAudit in a detached goroutine with a bounded
// timeout and logs at warn on failure. Tracked by auditWG so Close can join.
func (s *JobStore) appendAuditAsync(eventType, actor, target, detail string) {
	s.auditWG.Add(1)
	go func() {
		defer s.auditWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), auditWriteTimeout)
		defer cancel()
		if err := s.persister.AppendAudit(ctx, eventType, actor, target, detail); err != nil {
			s.log.Warn("audit write failed",
				slog.String("event", eventType),
				slog.String("target", target),
				slog.Any("err", err))
		}
	}()
}

// Close waits for in-flight audit writes to drain, bounded by timeout.
// Safe to call once; subsequent calls are no-ops.
func (s *JobStore) Close(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.auditWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		s.log.Warn("JobStore.Close: audit drain timed out", slog.Duration("timeout", timeout))
	}
}
