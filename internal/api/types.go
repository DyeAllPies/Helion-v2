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
	GPUs          uint32 `json:"gpus,omitempty"`           // whole-GPU reservation (bounded by maxGPUs)
}

// ArtifactBindingRequest is the JSON shape of an input/output entry on
// SubmitRequest. See cpb.ArtifactBinding for the persisted form.
type ArtifactBindingRequest struct {
	Name      string `json:"name"`                 // required; [A-Z_][A-Z0-9_]*
	URI       string `json:"uri,omitempty"`        // required for plain-job inputs; empty for outputs or when From is set
	From      string `json:"from,omitempty"`       // step-3 workflow input: "<upstream_job>.<output_name>"; mutually exclusive with URI
	LocalPath string `json:"local_path"`           // required; relative, no ".."
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
	Priority       *uint32             `json:"priority,omitempty"` // 0-100, default 50
	RetryPolicy    *RetryPolicyRequest `json:"retry_policy,omitempty"` // optional retry configuration

	// Step 2 — ML pipeline fields.
	WorkingDir   string                   `json:"working_dir,omitempty"`   // optional; empty = per-job tempdir on the node
	Inputs       []ArtifactBindingRequest `json:"inputs,omitempty"`        // artifact-store objects to stage before run
	Outputs      []ArtifactBindingRequest `json:"outputs,omitempty"`       // paths to upload after run
	NodeSelector map[string]string        `json:"node_selector,omitempty"` // exact-match label selector (step 4 wires scheduling)

	// Feature 17 — long-running inference service. When set, the job
	// bypasses timeout enforcement and the node probes its health
	// endpoint after HealthInitialMs. Mutually compatible with Inputs
	// (model bytes staged before serve start).
	Service *ServiceSpecRequest `json:"service,omitempty"`

	// Feature 26 — env keys whose VALUES must never appear in a
	// response, slog line, or audit detail. Every listed key must
	// appear in Env; a flag on a non-existent key is rejected at
	// submit. The coordinator still forwards the plaintext to the
	// node runtime — we need the value to dispatch — but every
	// response-path render runs env through redactSecretEnv first.
	SecretKeys []string `json:"secret_keys,omitempty"`
}

// ServiceSpecRequest is the JSON shape of the optional Service block
// on SubmitRequest. Mirrors cpb.ServiceSpec but lives in the api
// package so a future protocol tweak doesn't leak into the public
// HTTP contract.
type ServiceSpecRequest struct {
	Port            uint32 `json:"port"`                         // 1-65535; required
	HealthPath      string `json:"health_path"`                  // must start with "/"
	HealthInitialMs uint32 `json:"health_initial_ms,omitempty"`  // grace before first probe
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
	// Feature 42 — exposed so per-job run-intervals are computable
	// from REST without scraping the analytics event stream. Set
	// by the state machine on Scheduled → Dispatching; stays empty
	// for jobs that never got picked up (Pending → Failed via
	// unschedulable, etc.).
	DispatchedAt   *time.Time        `json:"dispatched_at,omitempty"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	Error          string            `json:"error,omitempty"`
	SubmittedBy    string            `json:"submitted_by,omitempty"` // AUDIT L1 — legacy; kept for one release (feature 36)
	// Feature 36 — fully-qualified feature-35 principal ID of the
	// job's owner ("user:alice", "operator:alice@ops",
	// "service:workflow_runner", or the "legacy:" sentinel for
	// pre-feature-36 records). Clients should prefer this over
	// SubmittedBy for ownership checks.
	OwnerPrincipal string            `json:"owner_principal,omitempty"`
	Priority       uint32            `json:"priority,omitempty"`
	Attempt        uint32            `json:"attempt,omitempty"`
	RetryAfter     *time.Time        `json:"retry_after,omitempty"`
	Service        *ServiceSpecRequest `json:"service,omitempty"`

	// Feature 26 — declared secret keys. Echoed back so a client can
	// render a "secret" badge next to the redacted value. Values in
	// Env for keys listed here are replaced with "[REDACTED]" by
	// jobToResponse before marshaling; operators who need the real
	// value must call POST /admin/jobs/{id}/reveal-secret.
	SecretKeys []string `json:"secret_keys,omitempty"`
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

	// Feature 33 — optional binding to a specific operator
	// cert CN. When set, authMiddleware will refuse every
	// request that presents this token unless the request
	// arrived with a verified client cert whose CN matches.
	// A cert-less request also fails — so a bound token is
	// useless outside the operator's browser. Empty = legacy
	// unbound behaviour.
	BindToCertCN string `json:"bind_to_cert_cn,omitempty"`
}

// IssueTokenResponse is the response for POST /admin/tokens.
type IssueTokenResponse struct {
	Token    string `json:"token"`
	Subject  string `json:"subject"`
	Role     string `json:"role"`
	TTLHours int    `json:"ttl_hours"`

	// Feature 33 — echoes the CN binding (or omitted when
	// the token was minted without one). Lets the dashboard
	// render a "bound to alice@ops" badge without parsing
	// the JWT payload.
	BoundToCertCN string `json:"bound_to_cert_cn,omitempty"`
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

// ── Feature 27 — browser mTLS operator certs ───────────────────────────────

// IssueOperatorCertRequest is the body for POST /admin/operator-certs.
//
// Admin-only. Generates a new ECDSA P-256 client certificate (EKU =
// ClientAuth only) signed by the coordinator's CA, plus a matching
// PKCS#12 bundle the operator imports into their browser. Every
// issuance emits an `operator_cert_issued` audit event carrying the
// CN + fingerprint + requesting admin's subject; read-back is
// one-shot (no GET endpoint). Rate-limited per admin subject.
type IssueOperatorCertRequest struct {
	// CommonName is the operator's human identifier. Used verbatim as
	// the cert Subject CN and — if feature 27's clientCertMiddleware
	// is enabled — as the `operator_cn` field on future audit entries
	// for requests arriving with this cert. Required; non-empty; ≤256 bytes;
	// no NUL / '='.
	CommonName string `json:"common_name"`
	// TTLDays sets the certificate lifetime in days. Defaults to 90
	// when 0. Capped server-side at the CA's remaining lifetime.
	TTLDays int `json:"ttl_days,omitempty"`
	// P12Password is the password that protects the returned PKCS#12
	// bundle. Required, non-empty, ≥8 chars. The operator needs this
	// password at browser import time. Server does not store it.
	P12Password string `json:"p12_password"`
}

// IssueOperatorCertResponse is the response body for POST
// /admin/operator-certs. Carries BOTH the raw PEM forms (for ops
// who pipe the cert into curl / command-line tools) AND a
// base64-encoded PKCS#12 bundle for browser import.
//
// Returned ONCE. The server does not retain the private key
// anywhere — if the operator loses the response, they must
// request a fresh issuance, which will mint a NEW cert with a
// NEW serial.
type IssueOperatorCertResponse struct {
	CommonName   string    `json:"common_name"`
	SerialHex    string    `json:"serial_hex"`    // operator-facing serial; can paste into revocation request
	FingerprintHex string  `json:"fingerprint_hex"` // SHA-256 of the cert DER; matches what browsers display
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	CertPEM      string    `json:"cert_pem"`
	KeyPEM       string    `json:"key_pem"`
	// P12Base64 is base64(PKCS#12-DER). Password from the request
	// is required to decrypt; server does not include it in the
	// response.
	P12Base64   string `json:"p12_base64"`
	AuditNotice string `json:"audit_notice"`
}

// ── Store / provider interfaces ───────────────────────────────────────────────

// JobStoreIface is the narrow interface the HTTP server needs from the JobStore.
type JobStoreIface interface {
	Submit(ctx context.Context, j *cpb.Job) error
	Get(jobID string) (*cpb.Job, error)
	List(ctx context.Context, statusFilter string, page, size int) ([]*cpb.Job, int, error)
	// Feature 37 — ListAll returns every job that matches the
	// status filter (empty filter = every job) WITHOUT
	// pagination. The list endpoint loads the full matching set,
	// filters per-row via authz.Allow(ActionRead), and paginates
	// the permitted subset. An alternative scope-push-down
	// (where the store filters by owner) is deferred per the
	// feature 37 spec.
	ListAll(ctx context.Context, statusFilter string) ([]*cpb.Job, error)
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
