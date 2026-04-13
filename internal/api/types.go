// internal/api/types.go
//
// Request/response types, context keys, and store/provider interfaces for the
// coordinator HTTP API. Kept in a separate file so handlers can import a
// concise type surface without pulling in server lifecycle code.

package api

import (
	"context"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── context keys ──────────────────────────────────────────────────────────────

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	claimsContextKey contextKey = "claims"
)

// ── request / response types ─────────────────────────────────────────────────

// ResourceLimits is the optional cgroup v2 constraint block in SubmitRequest / JobResponse.
// All fields default to 0 (no limit). Enforced only when using the Rust runtime.
type ResourceLimits struct {
	MemoryBytes uint64 `json:"memory_bytes,omitempty"` // maximum RSS in bytes
	CPUQuotaUS  uint64 `json:"cpu_quota_us,omitempty"` // CPU quota per period in microseconds
	CPUPeriodUS uint64 `json:"cpu_period_us,omitempty"` // period in microseconds (default 100000)
}

// RetryPolicyRequest is the optional retry configuration in SubmitRequest.
type RetryPolicyRequest struct {
	MaxAttempts    uint32 `json:"max_attempts,omitempty"`     // total attempts (default: 1 = no retry)
	Backoff        string `json:"backoff,omitempty"`          // "none", "linear", "exponential" (default: exponential)
	InitialDelayMs uint32 `json:"initial_delay_ms,omitempty"` // base delay in ms (default: 1000)
	MaxDelayMs     uint32 `json:"max_delay_ms,omitempty"`     // cap in ms (default: 60000)
	Jitter         *bool  `json:"jitter,omitempty"`           // add 0-25% jitter (default: true)
}

// ResourceRequestAPI is the optional scheduling reservation in SubmitRequest.
type ResourceRequestAPI struct {
	CpuMillicores uint32 `json:"cpu_millicores,omitempty"` // CPU reservation (default: 100 = 0.1 core)
	MemoryBytes   uint64 `json:"memory_bytes,omitempty"`   // memory reservation (default: 64MB)
	Slots         uint32 `json:"slots,omitempty"`           // slot count (default: 1)
}

// SubmitRequest is the JSON body for POST /jobs.
type SubmitRequest struct {
	ID             string              `json:"id"`              // client-generated; required
	Command        string              `json:"command"`         // required
	Args           []string            `json:"args"`            // optional
	Env            map[string]string   `json:"env,omitempty"`   // optional key-value environment variables
	TimeoutSeconds int64               `json:"timeout_seconds"` // optional; 0 means no limit
	Limits         ResourceLimits      `json:"limits,omitempty"` // optional cgroup v2 resource limits
	Resources      *ResourceRequestAPI `json:"resources,omitempty"` // optional scheduling reservation
	RetryPolicy    *RetryPolicyRequest `json:"retry_policy,omitempty"` // optional retry configuration
}

// JobResponse is the JSON body returned by POST /jobs and GET /jobs/{id}.
type JobResponse struct {
	ID             string            `json:"id"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	Limits         ResourceLimits    `json:"limits,omitempty"`
	Status         string            `json:"status"`
	NodeID         string            `json:"node_id,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	Error          string            `json:"error,omitempty"`
	SubmittedBy    string            `json:"submitted_by,omitempty"` // AUDIT L1
	Attempt        uint32            `json:"attempt,omitempty"`
	RetryAfter     *time.Time        `json:"retry_after,omitempty"`
}

// ErrorResponse is the JSON body for error responses.
type ErrorResponse struct {
	Error string `json:"error"`
}

// NodeListResponse is the response for GET /nodes.
type NodeListResponse struct {
	Nodes []NodeInfo `json:"nodes"`
	Total int        `json:"total"`
}

// NodeInfo contains information about a registered node.
type NodeInfo struct {
	ID            string    `json:"id"`
	Health        string    `json:"health"` // "healthy" | "unhealthy"
	LastSeen      time.Time `json:"last_seen"`
	RunningJobs   int       `json:"running_jobs"`
	Address       string    `json:"address"`
	CpuMillicores uint32   `json:"cpu_millicores,omitempty"`
	TotalMemBytes uint64   `json:"total_mem_bytes,omitempty"`
	MaxSlots      uint32   `json:"max_slots,omitempty"`
}

// JobListResponse is the response for GET /jobs (paginated).
type JobListResponse struct {
	Jobs  []JobResponse `json:"jobs"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// ClusterMetrics is the response for GET /metrics.
type ClusterMetrics struct {
	Nodes struct {
		Total   int `json:"total"`
		Healthy int `json:"healthy"`
	} `json:"nodes"`
	Jobs struct {
		Running   int `json:"running"`
		Pending   int `json:"pending"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
		Total     int `json:"total"`
	} `json:"jobs"`
	Timestamp time.Time `json:"timestamp"`
}

// AuditListResponse is the response for GET /audit.
type AuditListResponse struct {
	Events []audit.Event `json:"events"`
	Total  int           `json:"total"`
	Page   int           `json:"page"`
	Size   int           `json:"size"`
}

// IssueTokenRequest is the body for POST /admin/tokens.
type IssueTokenRequest struct {
	Subject  string `json:"subject"`   // required; e.g. "alice"
	Role     string `json:"role"`      // required; "admin" or "node"
	TTLHours int    `json:"ttl_hours"` // optional; defaults to 8 h; max 720 h (30 days)
}

// IssueTokenResponse is the response for POST /admin/tokens.
type IssueTokenResponse struct {
	Token    string `json:"token"`
	Subject  string `json:"subject"`
	Role     string `json:"role"`
	TTLHours int    `json:"ttl_hours"`
}

// RevokeTokenRequest is the optional body for DELETE /admin/tokens/{jti}.
type RevokeTokenRequest struct{}

// RevokeTokenResponse is the response for DELETE /admin/tokens/{jti}.
type RevokeTokenResponse struct {
	Revoked bool   `json:"revoked"`
	JTI     string `json:"jti"`
}

// RevokeNodeRequest is the request body for POST /admin/nodes/{id}/revoke.
type RevokeNodeRequest struct {
	Reason string `json:"reason"`
}

// RevokeNodeResponse is the response for POST /admin/nodes/{id}/revoke.
type RevokeNodeResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ── Store / provider interfaces ───────────────────────────────────────────────

// JobStoreIface is the narrow interface the HTTP server needs from the JobStore.
type JobStoreIface interface {
	Submit(ctx context.Context, j *cpb.Job) error
	Get(jobID string) (*cpb.Job, error)
	List(ctx context.Context, statusFilter string, page, size int) ([]*cpb.Job, int, error)
	GetJobsByStatus(ctx context.Context, status string) ([]*cpb.Job, error)
	CancelJob(ctx context.Context, jobID, reason string) error
}

// NodeRegistryIface is the interface for node operations.
type NodeRegistryIface interface {
	ListNodes(ctx context.Context) ([]NodeInfo, error)
	GetNodeHealth(nodeID string) (string, time.Time, error)
	GetRunningJobCount(nodeID string) int
	RevokeNode(ctx context.Context, nodeID, reason string) error
}

// MetricsProvider computes cluster metrics.
type MetricsProvider interface {
	GetClusterMetrics(ctx context.Context) (*ClusterMetrics, error)
}

// ReadinessChecker reports whether the coordinator is ready to serve traffic.
// Both conditions must pass for /readyz to return 200:
//   - Ping: BadgerDB is open and can execute transactions
//   - RegistryLen > 0: at least one node has registered
type ReadinessChecker interface {
	Ping() error
	RegistryLen() int
}
