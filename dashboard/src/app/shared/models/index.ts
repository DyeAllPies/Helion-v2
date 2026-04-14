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

export interface SubmitJobRequest {
  id:      string;
  command: string;
  args?:   string[];
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

export interface AuditEvent {
  id:         string;
  type:       AuditEventType;
  timestamp:  string;
  actor?:     string;
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
