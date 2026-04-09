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
  | 'lost';

export interface Job {
  id:            string;
  node_id:       string;
  command:       string;
  args:          string[];
  status:        JobStatus;
  created_at:    string;   // ISO 8601
  dispatched_at?: string;
  finished_at?:  string;
  exit_code?:    number;
  error?:        string;
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
  node_id:       string;
  address:       string;
  healthy:       boolean;
  last_seen:     string;   // ISO 8601
  running_jobs:  number;
  cpu_percent:   number;
  mem_percent:   number;
  registered_at: string;
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

export type AuditEventType =
  | 'job_submitted'
  | 'job_dispatched'
  | 'job_completed'
  | 'job_failed'
  | 'node_registered'
  | 'node_unhealthy'
  | 'auth_success'
  | 'auth_failure'
  | 'token_issued';

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
