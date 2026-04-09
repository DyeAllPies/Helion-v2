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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrJobNotFound is returned when a job ID does not exist in the store.
var ErrJobNotFound = errors.New("job: not found")

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
}

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

// ── Submit ────────────────────────────────────────────────────────────────────

// Submit inserts a new job in the pending state.
//
// The job is persisted before this call returns. An audit event
// "job.submitted" is written asynchronously.
func (s *JobStore) Submit(ctx context.Context, j *cpb.Job) error {
	j.Status = cpb.JobStatusPending
	j.CreatedAt = time.Now()

	s.mu.Lock()
	s.jobs[j.ID] = j
	// Persist inside the lock so the in-memory and on-disk states are always
	// consistent — no reader can observe the new job before it is durable.
	if err := s.persister.SaveJob(ctx, j); err != nil {
		delete(s.jobs, j.ID)
		s.mu.Unlock()
		return fmt.Errorf("JobStore.Submit persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Info("job submitted",
		slog.String("job_id", j.ID),
		slog.String("command", j.Command),
	)

	go func() {
		_ = s.persister.AppendAudit(context.Background(),
			"job.submitted", "coordinator", j.ID,
			fmt.Sprintf("command=%q", j.Command))
	}()

	return nil
}

// ── Transition ────────────────────────────────────────────────────────────────

// Transition moves a job from its current state to the target state.
//
// Rules:
//   - Only transitions listed in allowedTransitions are accepted.
//   - Transitioning a terminal job returns ErrJobAlreadyTerminal.
//   - An unknown job ID returns ErrJobNotFound.
//
// The new state is persisted inside the write lock before returning.
// A "job.transition" audit record is written asynchronously.
func (s *JobStore) Transition(ctx context.Context, jobID string, to cpb.JobStatus, opts TransitionOptions) error {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}

	from := j.Status
	if from.IsTerminal() {
		s.mu.Unlock()
		return fmt.Errorf("%w: job %s is %s", ErrJobAlreadyTerminal, jobID, from)
	}
	if !isAllowed(from, to) {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s → %s not permitted", ErrInvalidTransition, from, to)
	}

	// Apply mutation.
	j.Status = to
	now := time.Now()
	switch to {
	case cpb.JobStatusDispatching:
		j.DispatchedAt = now
		if opts.NodeID != "" {
			j.NodeID = opts.NodeID
		}
	case cpb.JobStatusCompleted, cpb.JobStatusFailed, cpb.JobStatusTimeout:
		j.FinishedAt = now
		j.ExitCode = opts.ExitCode
		if opts.ErrMsg != "" {
			j.Error = opts.ErrMsg
		}
	}

	if err := s.persister.SaveJob(ctx, j); err != nil {
		// Roll back the in-memory mutation on persist failure so callers do
		// not observe a state that is not on disk.
		j.Status = from
		s.mu.Unlock()
		return fmt.Errorf("JobStore.Transition persist: %w", err)
	}

	// Take a snapshot for the audit goroutine before releasing the lock.
	snap := *j
	s.mu.Unlock()

	s.log.Info("job state transition",
		slog.String("job_id", jobID),
		slog.String("from", from.String()),
		slog.String("to", to.String()),
	)

	go func() {
		detail := fmt.Sprintf("from=%s to=%s", from, to)
		if opts.NodeID != "" {
			detail += " node=" + opts.NodeID
		}
		if snap.Error != "" {
			detail += " error=" + snap.Error
		}
		_ = s.persister.AppendAudit(context.Background(),
			"job.transition", "coordinator", jobID, detail)
	}()

	return nil
}

// ── MarkLost ──────────────────────────────────────────────────────────────────

// MarkLost forcibly moves a job to the lost terminal state.
//
// This is the crash-recovery path: on coordinator restart, jobs that were
// in-flight (pending, dispatching, running) and have no node to complete them
// are marked lost. The normal transition table is bypassed — lost is reachable
// from any non-terminal state.
func (s *JobStore) MarkLost(ctx context.Context, jobID string, reason string) error {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if j.Status.IsTerminal() {
		s.mu.Unlock()
		return nil // already terminal — idempotent
	}

	prev := j.Status
	j.Status = cpb.JobStatusLost
	j.FinishedAt = time.Now()
	j.Error = reason

	if err := s.persister.SaveJob(ctx, j); err != nil {
		j.Status = prev
		s.mu.Unlock()
		return fmt.Errorf("JobStore.MarkLost persist: %w", err)
	}
	s.mu.Unlock()

	s.log.Warn("job marked lost",
		slog.String("job_id", jobID),
		slog.String("prev_status", prev.String()),
		slog.String("reason", reason),
	)

	go func() {
		_ = s.persister.AppendAudit(context.Background(),
			"job.lost", "coordinator", jobID,
			fmt.Sprintf("prev=%s reason=%s", prev, reason))
	}()

	return nil
}

// ── Reads ─────────────────────────────────────────────────────────────────────

// Get returns a snapshot of the job with the given ID.
// Returns ErrJobNotFound if the ID is unknown.
func (s *JobStore) Get(jobID string) (*cpb.Job, error) {
	s.mu.RLock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrJobNotFound
	}
	snap := *j // copy while holding the lock — prevents races with Transition
	s.mu.RUnlock()
	return &snap, nil
}

// List returns snapshots of all jobs currently in the store.
func (s *JobStore) List() []*cpb.Job {
	s.mu.RLock()
	out := make([]*cpb.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		snap := *j
		out = append(out, &snap)
	}
	s.mu.RUnlock()
	return out
}

// NonTerminal returns all jobs that have not yet reached a terminal state.
// Used by crash recovery to build the retry queue.
func (s *JobStore) NonTerminal() []*cpb.Job {
	s.mu.RLock()
	var out []*cpb.Job
	for _, j := range s.jobs {
		if !j.Status.IsTerminal() {
			snap := *j
			out = append(out, &snap)
		}
	}
	s.mu.RUnlock()
	return out
}

// ── Restore ───────────────────────────────────────────────────────────────────

// Restore loads persisted jobs into memory on startup.
//
// Called once during coordinator boot, before any RPCs are served.
// Jobs that were non-terminal at shutdown are loaded in their persisted state;
// the caller (crash recovery) then decides whether to requeue or mark lost.
func (s *JobStore) Restore(ctx context.Context) error {
	jobs, err := s.persister.LoadAllJobs(ctx)
	if err != nil {
		return fmt.Errorf("JobStore.Restore: %w", err)
	}

	s.mu.Lock()
	for _, j := range jobs {
		s.jobs[j.ID] = j
	}
	s.mu.Unlock()

	s.log.Info("job store restored from persistence",
		slog.Int("total", len(jobs)),
	)
	return nil
}

// ── MemJobPersister (tests) ───────────────────────────────────────────────────

// MemJobPersister is an in-memory JobPersister for tests.
// All operations are safe for concurrent use.
type MemJobPersister struct {
	mu     sync.Mutex
	Jobs   map[string]*cpb.Job
	Audits []string
}

// NewMemJobPersister returns an initialised MemJobPersister.
func NewMemJobPersister() *MemJobPersister {
	return &MemJobPersister{Jobs: make(map[string]*cpb.Job)}
}

func (m *MemJobPersister) SaveJob(_ context.Context, j *cpb.Job) error {
	cp := *j
	m.mu.Lock()
	m.Jobs[j.ID] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemJobPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error) {
	m.mu.Lock()
	out := make([]*cpb.Job, 0, len(m.Jobs))
	for _, j := range m.Jobs {
		cp := *j
		out = append(out, &cp)
	}
	m.mu.Unlock()
	return out, nil
}

func (m *MemJobPersister) AppendAudit(_ context.Context, eventType, _, target, detail string) error {
	m.mu.Lock()
	m.Audits = append(m.Audits, fmt.Sprintf("%s target=%s %s", eventType, target, detail))
	m.mu.Unlock()
	return nil
}

// AllJobs returns a point-in-time copy of all persisted jobs (for assertions).
func (m *MemJobPersister) AllJobs() map[string]*cpb.Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*cpb.Job, len(m.Jobs))
	for k, v := range m.Jobs {
		cp := *v
		out[k] = &cp
	}
	return out
}

// ── resetToPending ────────────────────────────────────────────────────────────

// resetToPending forcibly sets a non-terminal job's status back to pending so
// that the normal pending→dispatching transition can be applied.
//
// This is used exclusively by RecoveryManager to re-enter the dispatch pipeline
// for a job that was in-flight (dispatching or running) at coordinator shutdown.
// It is NOT a normal lifecycle transition — it bypasses the transition table
// intentionally and is therefore unexported.
func (s *JobStore) resetToPending(ctx context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}
	if j.Status == cpb.JobStatusPending {
		return nil // already pending, nothing to do
	}
	if j.Status.IsTerminal() {
		return nil // terminal jobs are not re-entered
	}

	j.Status = cpb.JobStatusPending
	if err := s.persister.SaveJob(ctx, j); err != nil {
		j.Status = cpb.JobStatusDispatching // best-effort rollback
		return fmt.Errorf("resetToPending persist: %w", err)
	}
	return nil
}
