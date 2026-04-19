// src/app/shared/models/index.ts
// TypeScript mirror of the Go coordinator types.
// Field names match JSON tags from types.go and api_server.go.

// ── Job ───────────────────────────────────────────────────────────────────────

export type JobStatus =
  | 'unknown'
  | 'pending'
  | 'dispatching'
  | 'running'
  | 'completed'
  | 'failed'
  | 'timeout'
  | 'lost'
  | 'retrying'
  | 'scheduled'
  | 'cancelled'
  | 'skipped';

export interface Job {
  id:            string;
  node_id:       string;
  command:       string;
  args:          string[];
  status:        JobStatus;
  priority?:     number;   // 0-100, default 50
  runtime?:      string;   // "go" or "rust"
  created_at:    string;   // ISO 8601
  dispatched_at?: string;
  finished_at?:  string;
  exit_code?:    number;
  error?:        string;
  /**
   * Feature 26: env values for keys listed in `secret_keys` are
   * server-redacted (render as "[REDACTED]") in every GET response.
   * The plaintext is available only via
   * POST /admin/jobs/{id}/reveal-secret (admin-only, audited).
   */
  env?:          Record<string, string>;
  secret_keys?:  string[];
}

export interface JobLogEntry {
  job_id:    string;
  seq:       number;
  data:      string;
  timestamp: string;
}

export interface JobLogsResponse {
  job_id:  string;
  entries: JobLogEntry[];
  total:   number;
}

export interface JobsPage {
  jobs:  Job[];
  total: number;
  page:  number;
  size:  number;
}

/**
 * Client-side mirror of the Go `api.SubmitRequest`. Extended for
 * feature 22 beyond the original three fields so the submission
 * form can exercise the full validator surface.
 *
 * Note on the `env` + `secret_keys` fields: the shipped form
 * (feature 26) is the `Record<string, string>` env map paired with
 * a sibling `secret_keys: string[]` list. The server redacts values
 * whose key is in `secret_keys` on every GET path; operators who
 * need to read one back must POST /admin/jobs/{id}/reveal-secret
 * (admin-only, audited).
 */
export interface SubmitJobRequest {
  id:              string;
  command:         string;
  args?:           string[];
  env?:            Record<string, string>;
  /** Feature 26 — keys whose values must be redacted on any GET path. */
  secret_keys?:    string[];
  timeout_seconds?: number;
  priority?:       number;
  node_selector?:  Record<string, string>;
  working_dir?:    string;
  inputs?:         SubmitArtifactBinding[];
  outputs?:        SubmitArtifactBinding[];
  limits?: {
    memory_bytes?:  number;
    cpu_quota_us?:  number;
    cpu_period_us?: number;
  };
  resources?: {
    gpus?: number;
  };
  service?: {
    port:             number;
    health_path:      string;
    health_initial_ms?: number;
  };
}

export interface SubmitArtifactBinding {
  name:        string;
  uri?:        string;
  from?:       string;   // "<upstream-job>.<output-name>" reference (workflow jobs only)
  local_path?: string;
  sha256?:     string;
}

/**
 * A single env var as used by the submission form. The `secret`
 * flag is now plumbed to the server (feature 26): the form
 * collects the array and splits it into `env` (map) +
 * `secret_keys` (list) when building the SubmitJobRequest payload.
 */
export interface SubmitEnvEntry {
  key:    string;
  value:  string;
  secret: boolean;
}

/**
 * Feature 26 — POST /admin/jobs/{id}/reveal-secret request + response.
 *
 * Admin-only endpoint. Every successful AND every rejected call is
 * audited (secret_revealed / secret_reveal_reject). The `reason`
 * field is mandatory and is written into the audit record — a
 * dashboard that lets a user reveal a secret without entering a
 * reason is a bug (the server will return 400).
 */
export interface RevealSecretRequest {
  key:    string;
  reason: string;
}

export interface RevealSecretResponse {
  job_id:       string;
  key:          string;
  value:        string;
  revealed_at:  string;
  revealed_by:  string;
  audit_notice: string;
}

// ── Node ──────────────────────────────────────────────────────────────────────

export interface Node {
  node_id:         string;
  address:         string;
  healthy:         boolean;
  last_seen:       string;   // ISO 8601
  running_jobs:    number;
  cpu_percent:     number;
  mem_percent:     number;
  registered_at:   string;
  cpu_millicores?: number;   // total CPU capacity (e.g. 4000 = 4 cores)
  total_mem_bytes?: number;  // total memory in bytes
  max_slots?:      number;   // max concurrent jobs
}

// ── Metrics ───────────────────────────────────────────────────────────────────

export interface ClusterMetrics {
  timestamp:      string;
  total_nodes:    number;
  healthy_nodes:  number;
  total_jobs:     number;
  running_jobs:   number;
  pending_jobs:   number;
  completed_jobs: number;
  failed_jobs:    number;
}

// ── Audit ─────────────────────────────────────────────────────────────────────
//
// AuditEventType values must match the EventXxx constants in internal/audit/logger.go.
// Any new event type added there must be mirrored here.

export type AuditEventType =
  // Job lifecycle
  | 'job_submit'
  | 'job_state_transition'
  // Node lifecycle
  | 'node_register'
  | 'node_revoke'
  // Security
  | 'security_violation'
  | 'auth_failure'
  | 'rate_limit_hit'
  // Coordinator lifecycle
  | 'coordinator_start'
  | 'coordinator_stop'
  // Catch-all so future event types don't crash the UI
  | (string & Record<never, never>);

/**
 * Feature 35 — principal-kind badges on the audit log.
 *
 * Matches internal/principal/principal.go Kind constants. A
 * catch-all string component keeps the UI resilient to future
 * kinds added server-side.
 */
export type AuditPrincipalKind =
  | 'user'
  | 'operator'
  | 'node'
  | 'service'
  | 'job'
  | 'anonymous'
  | (string & Record<never, never>);

export interface AuditEvent {
  id:         string;
  type:       AuditEventType;
  timestamp:  string;
  /** Legacy bare-string actor (kept for back-compat with pre-feature-35 consumers). */
  actor?:     string;
  /**
   * Feature 35 — fully-qualified Principal ID ("user:alice",
   * "operator:alice@ops", "service:dispatcher", …). Empty/absent
   * on events emitted before feature 35 shipped or by callers
   * that didn't stamp a Principal into context.
   */
  principal?:      string;
  /** Feature 35 — derived Kind for UI badge rendering. */
  principal_kind?: AuditPrincipalKind;
  target_id?: string;
  message:    string;
  metadata?:  Record<string, string>;
}

export interface AuditPage {
  events: AuditEvent[];
  total:  number;
  page:   number;
  size:   number;
}

// ── Workflow ─────────────────────────────────────────────────────────────────

export type WorkflowStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type DependencyCondition =
  | 'on_success'
  | 'on_failure'
  | 'on_complete';

export interface WorkflowJob {
  name:            string;
  command:         string;
  args?:           string[];
  env?:            Record<string, string>;
  timeout_seconds?: number;
  depends_on?:     string[];
  condition:       DependencyCondition;
  job_id?:         string;
  job_status?:     string;
}

export interface Workflow {
  id:          string;
  name:        string;
  status:      WorkflowStatus;
  jobs:        WorkflowJob[];
  created_at:  string;
  started_at?: string;
  finished_at?: string;
  error?:      string;
}

export interface WorkflowsPage {
  workflows: Workflow[];
  total:     number;
  page:      number;
  size:      number;
}

export interface SubmitWorkflowJobRequest {
  name:            string;
  command:         string;
  args?:           string[];
  env?:            Record<string, string>;
  timeout_seconds?: number;
  depends_on?:     string[];
  condition?:      string;
  /**
   * Label selector the scheduler matches against node-reported
   * labels. Mirrors the server-side WorkflowJobRequest field.
   * Feature 21 uses this to pin MNIST's `train` step to the
   * Rust-runtime node.
   */
  node_selector?:  Record<string, string>;
  /**
   * Artifact inputs / outputs. Inputs may carry a `from:
   * "<upstream>.<output>"` reference — the coordinator resolves
   * those at dispatch time (feature 13). This client type keeps
   * the binding shape compact; the server validator checks each
   * field shape.
   */
  inputs?:         SubmitArtifactBinding[];
  outputs?:        SubmitArtifactBinding[];
}

export interface SubmitWorkflowRequest {
  id:   string;
  name: string;
  jobs: SubmitWorkflowJobRequest[];
}

// ── Analytics ────────────────────────────────────────────────────────────────

export interface AnalyticsThroughputRow {
  hour:            string;
  status:          string;
  job_count:       number;
  avg_duration_ms: number;
  p95_duration_ms: number;
}

export interface AnalyticsThroughputResponse {
  from: string;
  to:   string;
  data: AnalyticsThroughputRow[];
}

export interface AnalyticsNodeReliabilityRow {
  node_id:          string;
  address:          string;
  jobs_completed:   number;
  jobs_failed:      number;
  failure_rate_pct: number;
  times_stale:      number;
  times_revoked:    number;
}

export interface AnalyticsRetryRow {
  category:       string;   // "retried" | "first_attempt"
  status:         string;
  job_count:      number;
  avg_duration_ms: number;
}

export interface AnalyticsQueueWaitRow {
  hour:        string;
  avg_wait_ms: number;
  p95_wait_ms: number;
  job_count:   number;
}

export interface AnalyticsQueueWaitResponse {
  from: string;
  to:   string;
  data: AnalyticsQueueWaitRow[];
}

export interface AnalyticsWorkflowOutcomeRow {
  event_type: string;
  day:        string;
  count:      number;
}

export interface AnalyticsWorkflowOutcomesResponse {
  from: string;
  to:   string;
  data: AnalyticsWorkflowOutcomeRow[];
}

// ── Feature 28 — unified analytics sink ─────────────────────────────────────

export interface SubmissionHistoryRow {
  id:             string;
  submitted_at:   string;
  actor:          string;
  operator_cn?:   string;
  source:         string;   // 'dashboard' | 'cli' | 'ci' | 'unknown'
  kind:           string;   // 'job' | 'workflow'
  resource_id:    string;
  dry_run:        boolean;
  accepted:       boolean;
  reject_reason?: string;
  user_agent?:    string;
}

export interface SubmissionHistoryResponse {
  rows:         SubmissionHistoryRow[];
  total:        number;
  next_cursor?: string;
}

export interface AuthEventRow {
  occurred_at: string;
  event_type:  string;   // 'login' | 'token_mint' | 'auth_fail' | 'rate_limit'
  actor?:      string;
  remote_ip?:  string;
  user_agent?: string;
  reason?:     string;
}

export interface AuthEventsResponse {
  rows:  AuthEventRow[];
  total: number;
}

// ── ML Registry (features 16 + 17) ───────────────────────────────────────────

export interface Dataset {
  name:         string;
  version:      string;
  uri:          string;
  size_bytes?:  number;
  sha256?:      string;
  tags?:        Record<string, string>;
  created_at:   string;
  created_by:   string;
}

export interface DatasetListResponse {
  datasets: Dataset[];
  total:    number;
  page:     number;
  size:     number;
}

export interface DatasetRegisterRequest {
  name:         string;
  version:      string;
  uri:          string;
  size_bytes?:  number;
  sha256?:      string;
  tags?:        Record<string, string>;
}

export interface DatasetRef {
  name:    string;
  version: string;
}

export interface MLModel {
  name:           string;
  version:        string;
  uri:            string;
  framework?:     string;
  source_job_id?: string;
  source_dataset?: DatasetRef;
  metrics?:       Record<string, number>;
  size_bytes?:    number;
  sha256?:        string;
  tags?:          Record<string, string>;
  created_at:     string;
  created_by:     string;
}

export interface ModelListResponse {
  models: MLModel[];
  total:  number;
  page:   number;
  size:   number;
}

export interface ModelRegisterRequest {
  name:           string;
  version:        string;
  uri:            string;
  framework?:     string;
  source_job_id?: string;
  source_dataset?: DatasetRef;
  metrics?:       Record<string, number>;
  size_bytes?:    number;
  sha256?:        string;
  tags?:          Record<string, string>;
}

export interface ServiceEndpoint {
  job_id:       string;
  node_id:      string;
  node_address: string;
  port:         number;
  health_path:  string;
  ready:        boolean;
  upstream_url: string;
  updated_at:   string;
}

export interface ServiceListResponse {
  services: ServiceEndpoint[];
  total:    number;
}

// ── Workflow lineage (feature 18 DAG view) ───────────────────────────────────

export interface LineageOutput {
  name:    string;
  uri:     string;
  size?:   number;
  sha256?: string;
}

export interface LineageModelRef {
  name:    string;
  version: string;
}

export interface LineageJob {
  name:             string;
  job_id?:          string;   // generated; empty before workflow Start()
  status:           string;   // "pending" | "running" | "completed" | …
  command?:         string;
  depends_on?:      string[];
  outputs?:         LineageOutput[];
  models_produced?: LineageModelRef[];
  // Populated once the job is dispatched. `omitempty` on the Go
  // side means all three fields are absent on pending jobs.
  node_id?:         string;
  dispatched_at?:   string;   // RFC-3339 UTC
  finished_at?:     string;   // RFC-3339 UTC
}

export interface ArtifactEdge {
  from_job:    string;
  from_output: string;
  to_job:      string;
  to_input:    string;
}

export interface WorkflowLineage {
  workflow_id:    string;
  name:           string;
  status:         string;
  jobs:           LineageJob[];
  artifact_edges: ArtifactEdge[];
}

// ── Auth ──────────────────────────────────────────────────────────────────────

export interface LoginRequest {
  token: string;   // root bearer token from coordinator stdout
}

export interface LoginResult {
  valid:     boolean;
  expiresAt: number;  // Unix ms — decoded from JWT
}

// ── WebSocket frames ─────────────────────────────────────────────────────────

export interface LogChunk {
  job_id:    string;
  sequence:  number;
  text:      string;
  timestamp: string;
}

export type MetricsFrame = ClusterMetrics;

export interface EventFrame {
  id:        string;
  type:      string;
  timestamp: string;
  data?:     Record<string, unknown>;
}
