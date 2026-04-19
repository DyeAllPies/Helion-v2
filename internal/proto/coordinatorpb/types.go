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

import (
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
)

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
	TotalGpus       uint32 `json:"total_gpus,omitempty"`        // whole-GPU capacity (0 = CPU-only)

	// Labels reported at Register time. The scheduler's node_selector
	// filter matches a job's NodeSelector against this map using
	// exact-equality semantics (no In / NotIn / glob). Frozen after
	// Register — re-registering with a different label set requires
	// either re-issuing the node's certificate or clearing the
	// coordinator's node record.
	Labels map[string]string `json:"labels,omitempty"`
}

// ── ResourceRequest ──────────────────────────────────────────────────────────

// ResourceRequest declares the resources a job requires for scheduling.
// The scheduler reserves these amounts on the target node.
type ResourceRequest struct {
	CpuMillicores uint32 `json:"cpu_millicores,omitempty"` // CPU reservation (default: 100 = 0.1 core)
	MemoryBytes   uint64 `json:"memory_bytes,omitempty"`   // memory reservation (default: 64MB)
	Slots         uint32 `json:"slots,omitempty"`           // slot count (default: 1)
	// GPUs is a whole-GPU reservation (no MIG slicing, no fractional
	// sharing). A job with GPUs>0 is only dispatched to nodes whose
	// Node.TotalGpus is at least as large, and the node-side runtime
	// assigns a comma-separated CUDA_VISIBLE_DEVICES list to the
	// running process.
	GPUs uint32 `json:"gpus,omitempty"`
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
	//
	// Feature 36 + deprecation note: SubmittedBy stays on the wire and
	// in storage for one release as a back-compat alias. The new
	// authoritative ownership field is OwnerPrincipal (below), which is
	// prefix-qualified with a Kind ("user:alice", "operator:alice@ops").
	// feature 37's authz engine reads OwnerPrincipal; the legacy
	// handleGetJob RBAC check continues to read SubmittedBy until that
	// slice lands.
	SubmittedBy string `json:"submitted_by,omitempty"`

	// OwnerPrincipal is the fully-qualified feature-35 principal ID of
	// whoever created this job. Format is "kind:suffix":
	// "user:alice", "operator:alice@ops", "node:gpu-01",
	// "service:dispatcher", "job:wf-42", "anonymous".
	//
	// Set by handleSubmitJob (from principal.FromContext) on new
	// records, and by the workflow Start() path (inherited from
	// the parent workflow's owner) for materialised workflow jobs.
	//
	// IMMUTABLE after creation. Job state transitions, retry
	// attempts, and workflow cancellations preserve this field. An
	// operator whose job is retried by service:retry_loop does NOT
	// lose ownership — the loop acts as the principal performing
	// the action in audit, but the job's owner stays the original
	// submitter.
	//
	// Legacy records (persisted before feature 36 shipped) are
	// backfilled on load: Jobs with a SubmittedBy value synthesise
	// "user:<SubmittedBy>"; Jobs without SubmittedBy get "legacy:".
	// Feature 37's policy engine refuses non-admin actions on
	// "legacy:"-owned resources — the same fail-closed behaviour
	// the pre-feature-36 AUDIT L1 check produced for SubmittedBy==".
	OwnerPrincipal string `json:"owner_principal,omitempty"`

	// Feature 38 — per-resource share grants. Each Share names
	// a grantee (user:*, operator:*, group:*) and the Actions
	// the grantee may perform. Managed via the
	// /admin/resources/job/{id}/share endpoints, mutated only
	// by the resource owner or an admin. Nil / empty means
	// "only owner + admin may act" (unchanged feature-36
	// behaviour). Persisted alongside OwnerPrincipal in the
	// same Badger record; legacy-owned resources (owner ==
	// "legacy:") ignore this list because feature 37's legacy
	// sentinel short-circuits before the share check runs.
	Shares []authz.Share `json:"shares,omitempty"`

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

	// ── Step 2: ML pipeline fields ───────────────────────────────────────
	//
	// WorkingDir is the directory the runtime cd's into before exec. Empty
	// means "let the node mint a per-job temp dir under HELION_WORK_ROOT".
	WorkingDir string `json:"working_dir,omitempty"`

	// Inputs are artifact-store objects the runtime downloads into the
	// working directory before the command runs. The runtime exports
	// HELION_INPUT_<NAME> = absolute path for each entry.
	Inputs []ArtifactBinding `json:"inputs,omitempty"`

	// Outputs are paths under the working directory that the runtime
	// uploads to the artifact store after the command exits 0. The
	// resolved URIs are recorded on the job's terminal event so that
	// downstream workflow jobs and the registry can reference them.
	Outputs []ArtifactBinding `json:"outputs,omitempty"`

	// NodeSelector narrows the set of nodes eligible to run this job.
	// Step 2 stores the selector verbatim; step 4 wires it into the
	// scheduler. Exact-match equality on labels the node reports at
	// registration time.
	NodeSelector map[string]string `json:"node_selector,omitempty"`

	// ResolvedOutputs is populated by the coordinator after a successful
	// run: for each Outputs binding the node's stager produced, we
	// record the assigned URI, size, and SHA-256. Empty for failed runs
	// (the stager skips uploads) and for jobs that declared no outputs.
	// Step 3's workflow artifact-passing resolves `from: job.output`
	// references against this slice.
	ResolvedOutputs []ArtifactOutput `json:"resolved_outputs,omitempty"`

	// ── Feature 17: inference service ───────────────────────────────────
	//
	// Service, when non-nil, marks this job as a long-running inference
	// service. The runtime skips timeout enforcement; the node-side
	// prober polls http://127.0.0.1:<Port><HealthPath> every 5 s and
	// emits ServiceEvent RPCs to the coordinator on readiness
	// transitions. The coordinator records the (node_address, port)
	// mapping on the first `ready` event so clients can look up the
	// upstream URL via GET /api/services/{id}.
	Service *ServiceSpec `json:"service,omitempty"`

	// ── Feature 26: secret env keys ──────────────────────────────────────
	//
	// SecretKeys names env keys whose VALUE must never appear in a
	// response body, slog line, or audit detail. The coordinator keeps
	// the plaintext value in Env (the runtime needs it to dispatch), but
	// every GET/list/dry-run path runs the env through
	// redactSecretEnv(Env, SecretKeys) before marshaling. Audit event
	// details carry the KEY NAMES (so reviewers can see which keys were
	// flagged) but never the values.
	//
	// Old BadgerDB entries without this field deserialise to nil, which
	// yields no redactions (unchanged legacy behaviour).
	SecretKeys []string `json:"secret_keys,omitempty"`
}

// ServiceSpec turns a job into a long-running inference service.
// Both JSON-persisted on the coordinator side and wire-copied into
// pb.ServiceSpec at dispatch time. Zero value of the pointer means
// "this is a normal batch job" — cheap to check.
type ServiceSpec struct {
	Port            uint32 `json:"port"`                         // 1-65535; required
	HealthPath      string `json:"health_path"`                  // e.g. "/healthz"; required
	HealthInitialMS uint32 `json:"health_initial_ms,omitempty"`  // pre-probe grace period
}

// ServiceEndpoint is what GET /api/services/{id} returns. Populated
// by the coordinator on first `ready` event for the job; cleared on
// job completion. Kept in memory only — a coordinator restart starts
// with an empty map, and the next `ready` event re-populates it.
type ServiceEndpoint struct {
	JobID       string    `json:"job_id"`
	NodeID      string    `json:"node_id"`
	NodeAddress string    `json:"node_address"` // "host:port" of the node
	Port        uint32    `json:"port"`         // port on the node the service listens on
	HealthPath  string    `json:"health_path"`
	Ready       bool      `json:"ready"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Feature 36 — the principal ID of the job's submitter. The
	// ServiceRegistry inherits this from the owning Job on first
	// `ready` event so authz (feature 37) can gate
	// GET /api/services/{job_id} on the caller's relationship to
	// the underlying job owner. Empty on legacy endpoints from a
	// coordinator restart where the Job has been removed.
	OwnerPrincipal string `json:"owner_principal,omitempty"`
}

// ArtifactOutput is the coordinator-side mirror of pb.ArtifactOutput —
// a resolved reference to an artifact the node agent uploaded post-run.
type ArtifactOutput struct {
	Name      string `json:"name"`
	URI       string `json:"uri"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}

// ArtifactBinding attaches an artifact-store object to a job as either
// an input (staged before run) or an output (uploaded after success).
// URIs are opaque; see internal/artifacts for the schemes understood
// by the coordinator's configured store.
type ArtifactBinding struct {
	// Name is surfaced to the job as HELION_INPUT_<NAME> / HELION_OUTPUT_<NAME>.
	// Must be a valid shell identifier ([A-Z_][A-Z0-9_]*), enforced at
	// submit time so later code can trust it.
	Name string `json:"name"`

	// URI points to the artifact in the store. Required for plain-job
	// inputs; assigned by the runtime after upload for outputs.
	// For workflow-job inputs, mutually exclusive with From: either
	// supply a concrete URI now or a From reference to resolve later.
	URI string `json:"uri,omitempty"`

	// From is a workflow-only reference of the form
	// "<upstream_job_name>.<output_name>". The coordinator rewrites
	// this into a concrete URI at dispatch time, drawing the value
	// from the upstream job's ResolvedOutputs record. Ignored on
	// plain-job submits (the validator rejects From there).
	From string `json:"from,omitempty"`

	// LocalPath is the job-relative path. Rejected at submit time if
	// absolute or containing ".." segments so it cannot escape the
	// working directory.
	LocalPath string `json:"local_path"`

	// SHA256 is the expected lower-hex digest of the resolved input
	// bytes. Populated by the coordinator at From-resolve time from
	// the upstream's ResolvedOutput record; empty on plain-URI
	// inputs where no upstream committed a digest. When present, the
	// node's stager verifies the download against this digest via
	// artifacts.GetAndVerify and fails the job on mismatch — catches
	// store-side tamper, bit rot, and TLS-layer corruption that
	// slipped past the hybrid PQ channel's MAC.
	SHA256 string `json:"sha256,omitempty"`
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

	// Step 2 — ML pipeline fields. Propagated verbatim onto the
	// materialised Job at Start() time. Step 3 rewrites Inputs[i].URI
	// references of the form "from: <upstream>.<name>" once the
	// upstream job produces a ResolvedOutput of that name.
	WorkingDir   string            `json:"working_dir,omitempty"`
	Inputs       []ArtifactBinding `json:"inputs,omitempty"`
	Outputs      []ArtifactBinding `json:"outputs,omitempty"`
	NodeSelector map[string]string `json:"node_selector,omitempty"`

	// Feature 26 — per-child secret env keys. Propagated onto the
	// materialised Job at workflow Start() so response redaction +
	// reveal-secret authorisation carry through the same as a
	// standalone submit.
	SecretKeys []string `json:"secret_keys,omitempty"`
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

	// Feature 36 — fully-qualified feature-35 principal ID of the
	// workflow's owner. Set by handleSubmitWorkflow on create.
	// Propagated to every materialised child job at Start().
	// Immutable after creation; Cancel / transition paths preserve.
	// Legacy records (pre-feature-36) load as "legacy:" and are
	// treated by feature 37's policy as admin-only-readable.
	OwnerPrincipal string `json:"owner_principal,omitempty"`

	// Feature 38 — share grants. See Job.Shares for the full
	// contract. Workflow-level shares do NOT cascade to child
	// jobs — each materialised Job carries its own inherited
	// list at Start() time (empty by default) so revoking a
	// workflow share does not silently strip access on jobs
	// that had been given their own shares after start.
	Shares []authz.Share `json:"shares,omitempty"`
}
