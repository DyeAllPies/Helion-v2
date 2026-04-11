// internal/proto/coordinatorpb/types.go
//
// Internal types that complement the generated proto stubs in proto/.
//
// The real generated types (RegisterRequest, HeartbeatMessage, JobResult, etc.)
// live in github.com/DyeAllPies/Helion-v2/proto (package proto).
//
// This file defines two things that are NOT in the proto:
//
//   1. Node  — the coordinator's in-memory + persisted record for a worker node.
//              Not a proto message; serialised as JSON via BadgerJSONPersister.
//
//   2. JobStatus — a plain Go enum for job lifecycle state, used by the job
//                  lifecycle and crash-recovery logic.  The proto uses
//                  JobResult.Success bool + ExitCode for outcomes; we need a
//                  richer internal state machine.

package coordinatorpb

import "time"

// ── JobStatus ─────────────────────────────────────────────────────────────────

// JobStatus is the internal lifecycle state of a job.
// The proto uses JobResult.Success/ExitCode for final outcomes; this enum
// covers the full in-flight state machine the coordinator tracks internally.
type JobStatus int32

const (
	JobStatusUnknown     JobStatus = 0
	JobStatusPending     JobStatus = 1
	JobStatusDispatching JobStatus = 2
	JobStatusRunning     JobStatus = 3
	JobStatusCompleted   JobStatus = 4
	JobStatusFailed      JobStatus = 5
	JobStatusTimeout     JobStatus = 6
	JobStatusLost        JobStatus = 7
)

func (s JobStatus) String() string {
	switch s {
	case JobStatusPending:
		return "pending"
	case JobStatusDispatching:
		return "dispatching"
	case JobStatusRunning:
		return "running"
	case JobStatusCompleted:
		return "completed"
	case JobStatusFailed:
		return "failed"
	case JobStatusTimeout:
		return "timeout"
	case JobStatusLost:
		return "lost"
	default:
		return "unknown"
	}
}

// IsTerminal returns true for statuses from which a job will never transition.
// Used by crash recovery to skip already-finished jobs.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobStatusCompleted, JobStatusFailed, JobStatusTimeout, JobStatusLost:
		return true
	}
	return false
}

// ── Node ──────────────────────────────────────────────────────────────────────

// Node is the coordinator's in-memory and persisted record for a worker node.
// Not a proto message — serialised as JSON by BadgerJSONPersister.
//
// The proto HeartbeatMessage carries NodeId + Timestamp + RunningJobs;
// the registry merges those into this struct on every heartbeat received.
type Node struct {
	NodeID       string    `json:"node_id"`
	Address      string    `json:"address"`    // set at Register time; not in heartbeat
	Healthy      bool      `json:"healthy"`    // derived from LastSeen; stored for dashboard reads
	LastSeen     time.Time `json:"last_seen"`
	RunningJobs  int32     `json:"running_jobs"`
	CpuPercent   float64   `json:"cpu_percent"`
	MemPercent   float64   `json:"mem_percent"`
	RegisteredAt time.Time `json:"registered_at"`
}

// ── Job ───────────────────────────────────────────────────────────────────────

// Job is the coordinator's in-memory and persisted record for a scheduled job.
// Not a proto message — serialised as JSON by BadgerJSONPersister.
// ResourceLimits mirrors runtime.ResourceLimits for storage in the Job record.
// Zero values mean "no limit".  Applied by the Rust runtime via cgroup v2.
type ResourceLimits struct {
	MemoryBytes uint64 `json:"memory_bytes,omitempty"` // maximum RSS in bytes
	CPUQuotaUS  uint64 `json:"cpu_quota_us,omitempty"` // CPU quota per period in microseconds
	CPUPeriodUS uint64 `json:"cpu_period_us,omitempty"` // period in microseconds (default 100000)
}

type Job struct {
	ID             string            `json:"id"`
	NodeID         string            `json:"node_id"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	Limits         ResourceLimits    `json:"limits,omitempty"`
	Status         JobStatus         `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
	DispatchedAt   time.Time         `json:"dispatched_at,omitempty"`
	FinishedAt     time.Time         `json:"finished_at,omitempty"`
	ExitCode       int32             `json:"exit_code,omitempty"`
	Error          string            `json:"error,omitempty"`

	// SubmittedBy records the JWT subject of the caller that submitted the
	// job. Set by the API layer when auth is enabled; empty in dev mode.
	//
	// AUDIT L1 (fixed): used by handleGetJob to enforce per-job RBAC —
	// non-admin callers can only read jobs they submitted themselves.
	// Old BadgerDB entries without this field deserialize with an empty
	// string, which yields a 403 for non-admin access (backward-compatible
	// in the safe direction).
	SubmittedBy string `json:"submitted_by,omitempty"`
}
