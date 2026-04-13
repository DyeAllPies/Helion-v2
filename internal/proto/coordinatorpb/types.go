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
	JobStatusRetrying    JobStatus = 8
	JobStatusScheduled   JobStatus = 9
	JobStatusCancelled   JobStatus = 10
	JobStatusSkipped     JobStatus = 11
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
	case JobStatusRetrying:
		return "retrying"
	case JobStatusScheduled:
		return "scheduled"
	case JobStatusCancelled:
		return "cancelled"
	case JobStatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// IsTerminal returns true for statuses from which a job will never transition.
// Used by crash recovery to skip already-finished jobs.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobStatusCompleted, JobStatusFailed, JobStatusTimeout, JobStatusLost, JobStatusCancelled, JobStatusSkipped:
		return true
	}
	return false
}

// ── Node ──────────────────────────────────────────────────────────────────────

// Node is the coordinator's in-memory and persisted record for a worker node.
// Not a proto message — serialised as JSON by BadgerJSONPersister.
//
// The proto HeartbeatMessage carries NodeId + Timestamp + RunningJobs +
// capacity fields; the registry merges those into this struct on every
// heartbeat received.
type Node struct {
	NodeID       string    `json:"node_id"`
	Address      string    `json:"address"`    // set at Register time; not in heartbeat
	Healthy      bool      `json:"healthy"`    // derived from LastSeen; stored for dashboard reads
	LastSeen     time.Time `json:"last_seen"`
	RunningJobs  int32     `json:"running_jobs"`
	CpuPercent   float64   `json:"cpu_percent"`
	MemPercent   float64   `json:"mem_percent"`
	RegisteredAt time.Time `json:"registered_at"`

	// Resource capacity — reported by the node agent via heartbeat.
	// Used by ResourceAwarePolicy for bin-packing scheduling.
	CpuMillicores   uint32 `json:"cpu_millicores,omitempty"`    // total CPU (e.g. 4000 = 4 cores)
	TotalMemBytes   uint64 `json:"total_mem_bytes,omitempty"`   // total memory
	MaxSlots        uint32 `json:"max_slots,omitempty"`         // max concurrent jobs
}

// ── ResourceRequest ──────────────────────────────────────────────────────────

// ResourceRequest declares the resources a job requires for scheduling.
// The scheduler reserves these amounts on the target node.
type ResourceRequest struct {
	CpuMillicores uint32 `json:"cpu_millicores,omitempty"` // CPU reservation (default: 100 = 0.1 core)
	MemoryBytes   uint64 `json:"memory_bytes,omitempty"`   // memory reservation (default: 64MB)
	Slots         uint32 `json:"slots,omitempty"`           // slot count (default: 1)
}

// DefaultResourceRequest returns the minimum resource request used when
// a job doesn't specify one.
func DefaultResourceRequest() ResourceRequest {
	return ResourceRequest{
		CpuMillicores: 100,
		MemoryBytes:   64 << 20, // 64 MiB
		Slots:         1,
	}
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

	// Runtime records which backend executed the job ("go" or "rust").
	// Set when the node reports the result.
	Runtime string `json:"runtime,omitempty"`

	// WorkflowID links this job to a workflow. Empty for standalone jobs.
	WorkflowID string `json:"workflow_id,omitempty"`

	// Priority controls dispatch ordering. 0 (lowest) to 100 (highest).
	// Default: 50 (normal). Higher-priority jobs are dispatched first.
	Priority uint32 `json:"priority,omitempty"`

	// Resources declares the resource reservation for scheduling.
	// Zero value means use DefaultResourceRequest().
	Resources ResourceRequest `json:"resources,omitempty"`

	// RetryPolicy controls automatic retry behavior on failure/timeout.
	// Zero value (nil) means no retry — job fails immediately (max_attempts=1).
	RetryPolicy *RetryPolicy `json:"retry_policy,omitempty"`

	// Attempt is the current attempt number (1-indexed). First attempt is 1.
	Attempt uint32 `json:"attempt,omitempty"`

	// RetryAfter is the earliest time this job can be retried. Set when a job
	// enters the retrying state. The dispatch loop skips jobs where now < RetryAfter.
	RetryAfter time.Time `json:"retry_after,omitempty"`
}

// ── BackoffStrategy ──────────────────────────────────────────────────────────

// BackoffStrategy controls how retry delay increases between attempts.
type BackoffStrategy int32

const (
	// BackoffNone uses a fixed delay (initial_delay_ms every time).
	BackoffNone BackoffStrategy = 0
	// BackoffLinear increases delay by initial_delay_ms each attempt.
	BackoffLinear BackoffStrategy = 1
	// BackoffExponential doubles the delay each attempt (capped at max_delay_ms).
	BackoffExponential BackoffStrategy = 2
)

func (s BackoffStrategy) String() string {
	switch s {
	case BackoffNone:
		return "none"
	case BackoffLinear:
		return "linear"
	case BackoffExponential:
		return "exponential"
	default:
		return "unknown"
	}
}

// ── RetryPolicy ──────────────────────────────────────────────────────────────

// RetryPolicy defines per-job retry behavior on failure or timeout.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts (not retries). Default: 1 (no retry).
	// A value of 3 means: 1 initial attempt + 2 retries.
	MaxAttempts uint32 `json:"max_attempts"`

	// Backoff controls how delay grows between retries.
	Backoff BackoffStrategy `json:"backoff"`

	// InitialDelayMs is the base delay in milliseconds before the first retry.
	// Default: 1000 (1 second).
	InitialDelayMs uint32 `json:"initial_delay_ms"`

	// MaxDelayMs caps the delay regardless of backoff strategy.
	// Default: 60000 (60 seconds).
	MaxDelayMs uint32 `json:"max_delay_ms"`

	// Jitter adds random noise (0-25% of calculated delay) to prevent thundering herd.
	// Default: true.
	Jitter bool `json:"jitter"`
}

// ── WorkflowStatus ───────────────────────────────────────────────────────────

// WorkflowStatus is the lifecycle state of a workflow.
type WorkflowStatus int32

const (
	WorkflowStatusPending   WorkflowStatus = 0
	WorkflowStatusRunning   WorkflowStatus = 1
	WorkflowStatusCompleted WorkflowStatus = 2
	WorkflowStatusFailed    WorkflowStatus = 3
	WorkflowStatusCancelled WorkflowStatus = 4
)

func (s WorkflowStatus) String() string {
	switch s {
	case WorkflowStatusPending:
		return "pending"
	case WorkflowStatusRunning:
		return "running"
	case WorkflowStatusCompleted:
		return "completed"
	case WorkflowStatusFailed:
		return "failed"
	case WorkflowStatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// IsTerminal returns true for statuses from which a workflow will never transition.
func (s WorkflowStatus) IsTerminal() bool {
	switch s {
	case WorkflowStatusCompleted, WorkflowStatusFailed, WorkflowStatusCancelled:
		return true
	}
	return false
}

// ── DependencyCondition ──────────────────────────────────────────────────────

// DependencyCondition controls when a downstream job becomes eligible.
type DependencyCondition int32

const (
	// DependencyOnSuccess runs the downstream job only if the upstream succeeded.
	DependencyOnSuccess DependencyCondition = 0
	// DependencyOnFailure runs the downstream job only if the upstream failed.
	DependencyOnFailure DependencyCondition = 1
	// DependencyOnComplete runs the downstream job regardless of upstream result.
	DependencyOnComplete DependencyCondition = 2
)

func (c DependencyCondition) String() string {
	switch c {
	case DependencyOnSuccess:
		return "on_success"
	case DependencyOnFailure:
		return "on_failure"
	case DependencyOnComplete:
		return "on_complete"
	default:
		return "unknown"
	}
}

// ── WorkflowJob ──────────────────────────────────────────────────────────────

// WorkflowJob defines a single job within a workflow DAG.
type WorkflowJob struct {
	Name           string              `json:"name"`                     // unique within the workflow
	Command        string              `json:"command"`
	Args           []string            `json:"args,omitempty"`
	Env            map[string]string   `json:"env,omitempty"`
	TimeoutSeconds int64               `json:"timeout_seconds,omitempty"`
	Runtime        string              `json:"runtime,omitempty"`
	DependsOn      []string            `json:"depends_on,omitempty"`     // names of upstream jobs
	Condition      DependencyCondition `json:"condition,omitempty"`      // when to run (default: on_success)
	Priority       uint32              `json:"priority,omitempty"`       // overrides workflow priority; 0 = inherit
	JobID          string              `json:"job_id,omitempty"`         // set after job is created in JobStore
}

// ── Workflow ─────────────────────────────────────────────────────────────────

// Workflow is the coordinator's persisted record for a multi-job DAG.
// Serialised as JSON by BadgerJSONPersister under workflows/{id}.
type Workflow struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Priority  uint32         `json:"priority,omitempty"` // default priority for all jobs; 0 = 50
	Jobs      []WorkflowJob  `json:"jobs"`
	Status    WorkflowStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	StartedAt time.Time      `json:"started_at,omitempty"`
	FinishedAt time.Time     `json:"finished_at,omitempty"`
	Error     string         `json:"error,omitempty"`
}
