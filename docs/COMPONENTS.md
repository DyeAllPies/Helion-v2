# Helion v2 — Component Design

Detailed internals for each component of the Helion v2 minimal orchestrator.
For the high-level architecture, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 1. Coordinator

The coordinator is the single control-plane process.

**Node registry.** Maintains the authoritative list of known nodes, their certificates,
health status, and current load. Persisted in BadgerDB; each heartbeat updates a TTL-keyed
record under `nodes/`.

**Scheduler.** Selects a target node for each incoming job. Policies are pluggable behind
an interface:
- `roundrobin` — cycles through healthy nodes using `atomic.Int64` (v1 race fixed)
- `least` — picks the node with the fewest running jobs

**Job lifecycle.** Tracks every job through a strict state machine:

```
pending → scheduled → dispatching → running → completed
                                            → failed → retrying → pending (with backoff)
                                            → timeout → retrying → pending (with backoff)
                                            → cancelled
pending → cancelled
pending → skipped (DAG: upstream failed)
any non-terminal → lost (crash recovery)
```

All transitions are persisted atomically and written to the audit log.

**Retry policies.** Jobs can declare a per-job `RetryPolicy` with:
- `max_attempts` — total attempts (1 = no retry, default)
- `backoff` — `none` (fixed), `linear`, or `exponential` (default)
- `initial_delay_ms` / `max_delay_ms` — delay bounds
- `jitter` — 0-25% random noise to prevent thundering herd

When a job fails or times out, `RetryIfEligible` checks the policy. If attempts
remain, the job transitions `failed → retrying → pending` with a `RetryAfter`
timestamp. The dispatch loop skips jobs in backoff window (`now < RetryAfter`).

File layout:
```
retry.go      — ShouldRetry, NextRetryDelay, DefaultRetryPolicy (pure functions)
job_retry.go  — JobStore.RetryIfEligible (state transitions)
```

**Dispatch loop.** Periodically polls the job store for pending jobs and dispatches them
to healthy nodes. Uses the scheduler to pick a target node, transitions the job to
`dispatching`, then sends it via gRPC to the node agent. On dispatch failure the job is
marked `failed`; on success the node takes ownership and reports back via `ReportResult`.
Jobs in backoff window (retry delay not yet expired) are skipped until eligible.

**Certificate Authority.** Issues per-node X.509 certificates on first registration using
ML-DSA (Dilithium-3) in hybrid mode with ECDSA. Acts as the cluster's internal CA. The
signed certificate is returned in the `RegisterResponse` so the node can present it on
its own gRPC server — this allows the coordinator to verify node certs during dispatch.

**REST/WebSocket API.** Serves the Angular dashboard and `helion-run` CLI. All endpoints
except `/healthz`, `/readyz`, and `/metrics` require a valid JWT. Admin-only endpoints
(`/admin/...`) additionally require `role: admin` in the token claims.

**Certificate pinning.** On first registration the coordinator records the SHA-256
fingerprint of the node's DER certificate. Subsequent registrations with a different
certificate are rejected unless the node goes through a full revoke → re-register cycle.

**Stream revocation.** When a node is revoked, its active heartbeat gRPC stream is
closed immediately via a done channel, eliminating the window between revocation and
the next heartbeat timeout.

**Crash recovery.** On startup, reads BadgerDB, identifies non-terminal jobs, waits 15 s
(configurable grace period) for nodes to re-register, then dispatches recovered jobs.

**Workflow / DAG engine.** Supports multi-job workflows with dependency-driven execution.

- **DAG validation.** On submission, validates the job graph using Kahn's algorithm for
  cycle detection. Rejects duplicate names, unknown references, and self-dependencies.
- **Job materialisation.** `WorkflowStore.Start()` creates a real `Job` in the `JobStore`
  for each workflow step (ID = `{workflow_id}/{job_name}`). Root jobs (no `depends_on`)
  enter the pending queue immediately.
- **Dependency gating.** The dispatch loop builds an eligible set each tick by checking
  whether all upstream dependencies have reached a satisfying terminal state. Three
  conditions control eligibility: `on_success` (default), `on_failure`, `on_complete`.
- **Cascading failure.** When a job fails and downstream dependents require `on_success`,
  they are automatically marked `lost` with a descriptive reason.
- **Workflow completion.** When all jobs in a workflow reach a terminal state, the workflow
  is marked `completed` (all succeeded) or `failed` (any failed/timed out/lost).
- **Cancellation.** `DELETE /workflows/{id}` marks all non-terminal jobs as `lost` and
  transitions the workflow to `cancelled`.

File layout:
```
workflow.go           — errors, interfaces, WorkflowStore type
workflow_submit.go    — Submit, Start
workflow_lifecycle.go — EligibleJobs, OnJobCompleted, Cancel
workflow_read.go      — Get, List, RunningWorkflowIDs, Restore
dag.go                — ValidateDAG, TopologicalSort, Descendants, RootJobs
```

---

## 2. Node agent

Each node agent is a long-lived process on a worker host.

- **Self-registration.** Contacts the coordinator via gRPC on startup, presents its
  certificate, and registers. If no certificate exists, initiates the issuance flow.
- **Heartbeat.** Maintains a bidi-streaming gRPC call to the coordinator at a configurable
  interval (default 10 s). The coordinator does not poll — it passively monitors the stream.
- **Job execution.** Receives dispatch RPCs, hands off to the runtime layer, streams log
  chunks back to the coordinator in real time.
- **Local metrics.** Exposes a `/metrics` endpoint in Prometheus text format.

---

## 3. Runtime interface

The runtime is isolated from the agent behind a Go interface:

```go
type Runtime interface {
    Run(ctx context.Context, job Job, logWriter io.Writer) error
    Kill(jobID string) error
    Status(jobID string) (JobStatus, error)
}
```

**GoRuntime** (current default) — uses Linux namespaces (UTS, PID, MNT) gated behind a
privilege check. Falls back to a plain subprocess when `HELION_ALLOW_ISOLATION=false`.

**RustRuntime** — communicates with the `helion-runtime` Rust binary over a Unix
domain socket using protobuf-framed messages. Adds cgroup v2 resource limits and
seccomp-bpf syscall filtering. Enabled by setting `HELION_RUNTIME_SOCKET`.

The selector logic:

```
HELION_RUNTIME_SOCKET set + socket reachable  → RustRuntime
otherwise                                      → GoRuntime
```

---

## 4. Analytics pipeline

Package: `internal/analytics/`

The analytics pipeline is an opt-in subsystem that exports event data from the
coordinator into PostgreSQL for historical analysis.

### Sink (`sink.go`)

Subscribes to `"*"` on the event bus and batches events in memory. Flushes to
PostgreSQL every `HELION_ANALYTICS_FLUSH_MS` (default 500) or `HELION_ANALYTICS_BATCH`
(default 100) events, whichever comes first. Each flush:

1. Batch-INSERTs into the `events` fact table (`ON CONFLICT DO NOTHING`).
2. Upserts `job_summary` (one row per job: status, timing, retry count).
3. Upserts `node_summary` (one row per node: registration history, job tallies).

Buffer grows up to `HELION_ANALYTICS_BUFFER` (default 10,000), then drops oldest.
Never blocks the coordinator's hot path. On shutdown, drains remaining events with
a 5-second timeout.

### Migrations (`migrations.go`)

SQL migrations are `go:embed`-ded at compile time. The runner creates a
`schema_migrations` tracking table and applies numbered migrations in order,
each in its own transaction. Rollback support via `.down.sql` files.

### Backfill (`backfill.go`)

Reads all `audit:` entries from BadgerDB, normalises audit event types to bus
topic names (e.g. `job_state_transition` → `job.transition`), and inserts into
PostgreSQL via the same flush path. Idempotent.

Exposed as a one-shot subcommand of the coordinator binary:

```
helion-coordinator analytics backfill [--pg-dsn=...] [--db-path=...]
```

Flags fall back to `HELION_ANALYTICS_DSN` / `HELION_DB_PATH` env vars. Runs
migrations before inserting so the target database may be empty.

**Read-only BadgerDB access.** Opens via
`cluster.NewBadgerJSONPersisterReadOnly` (`WithReadOnly(true).WithBypassLockGuard(true)`).
Any accidental write fails at the BadgerDB layer. Note that BadgerDB's
read-only mode fails to open if the WAL is dirty — i.e. another writer is
currently holding the DB open with pending writes. In practice, run
backfills during maintenance windows or shortly after a clean coordinator
restart.

### REST API (`internal/api/handlers_analytics.go`)

Six read-only endpoints, all authenticated, querying PostgreSQL:

| Endpoint | Description |
|---|---|
| `GET /api/analytics/throughput` | Hourly job counts, avg/p95 duration by status |
| `GET /api/analytics/node-reliability` | Per-node failure rates and health history |
| `GET /api/analytics/retry-effectiveness` | Retried vs first-attempt outcomes |
| `GET /api/analytics/queue-wait` | Avg/p95 pending→running wait per hour |
| `GET /api/analytics/workflow-outcomes` | Workflow success/failure by day |
| `GET /api/analytics/events` | Paginated raw event query with type filter |

Time-range endpoints accept `from` and `to` query parameters (RFC 3339).
Default range: last 7 days.

### Security controls

Every analytics endpoint runs through a shared pre-flight (see
`handlers_analytics.go:analyticsPreflight`) that applies the same protections
as the rest of the authenticated API surface:

| Control | Details |
|---|---|
| JWT auth | `authMiddleware` — 401 if missing/invalid Bearer token |
| Per-subject rate limit | Token bucket: `analyticsQueryRate=2 rps`, `analyticsQueryBurst=30`. Returns **429 Too Many Requests** when exceeded. Bucket keyed on JWT subject. |
| Time-range bounds | `analyticsMaxRange = 365 * 24h`. Rejects inverted, malformed, or oversized ranges with **400 Bad Request**. |
| Pagination bounds | `analyticsMaxLimit = 1000`. `limit` query param is clamped; negative values fall back to default 100. |
| Audit logging | Every successful query is recorded as `analytics.query` in the audit log with `actor`, `endpoint`, `from`, `to`. Rate-limited requests are rejected **before** audit so they are not recorded as successful reads. |
| Error masking | All DB errors return generic `"internal error"`; raw pgx errors logged server-side only. |

The rate-limit and audit behaviour mirrors the existing `tokenIssueAllow`
and `LogJobSubmit` patterns so operators see one consistent surface.

### Connection pooling

The coordinator connects via `*pgxpool.Pool`, **not** `*pgx.Conn`. The pool
is required because the sink writes (in a transaction) and the API handlers
read concurrently — a single pgx connection is not safe for concurrent use
and produced `"conn busy"` errors before the switch.

### Workflow event emission

Feature 09 depends on `workflow.completed` / `workflow.failed` events. These
are published by `WorkflowStore.OnJobCompleted` once all jobs in a workflow
reach terminal state (see [workflow_lifecycle.go](../internal/cluster/workflow_lifecycle.go)).
Wiring happens at coordinator startup via `workflows.SetEventBus(eventBus)`.
Without this wiring the analytics sink would never persist workflow rows to
the `events` table.
