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
- `roundrobin` — cycles through healthy nodes using `atomic.Int64`
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

---

## 5. ML subsystems

Features 11–19 added five components on top of the base
orchestrator. Each has a focused responsibility and is wired
into the cluster at coordinator startup by opt-in env vars —
nothing is required for non-ML deployments. See
[ml-pipelines.md](ml-pipelines.md) for the user-facing guide.

### 5.1 Artifact store — `internal/artifacts/`

Object-storage abstraction for ML job bytes. Interface:

```go
type Store interface {
    Put(ctx context.Context, uri string, r io.Reader) error
    Get(ctx context.Context, uri string) (io.ReadCloser, error)
    Stat(ctx context.Context, uri string) (Info, error)
    // GetAndVerify reads into a capped buffer, hashes, returns
    // ErrChecksumMismatch if the digest doesn't match.
    GetAndVerify(ctx context.Context, uri, sha256 string) ([]byte, error)
    GetAndVerifyTo(ctx context.Context, uri, sha256, dst string) error
}
```

Two implementations:

- **`LocalStore`** — filesystem-backed, `file://` URIs. Used for
  local development and tests.
- **`S3Store`** — `minio-go` client against any S3-compatible
  endpoint (AWS, MinIO, Ceph). Enabled via
  `HELION_ARTIFACTS_BACKEND=s3` + `HELION_ARTIFACTS_S3_*` env.

URI shape for node-generated artifacts:
`s3://<bucket>/jobs/<job-id>/<output-local-path>`. The
`jobs/<job-id>/` prefix is load-bearing for feature-13's
cross-job integrity attestation — `attestOutputs` rejects any
URI that doesn't match the reporting job's ID.

### 5.2 Staging — `internal/staging/`

Node-side orchestration of per-job working directories.

```go
func (s *Stager) Prepare(ctx context.Context, job *cpb.Job) (*Prepared, error)
func (s *Stager) Finalize(ctx context.Context, p *Prepared, success bool) ([]ResolvedOutput, error)
```

**Prepare** (before `Runtime.Run`):

1. Create a workdir under `$HELION_WORK_ROOT/<job-id>/` (falls
   back to `$TMPDIR/helion-jobs/` when unset) with mode `0o700`.
2. For each declared input, `GetAndVerify` the URI into the
   workdir at the declared `local_path`. Missing SHA-256 falls
   back to a plain `Get` (plain-URI inputs with no committed
   digest — e.g. `/jobs`-submit direct URIs).
3. Export one `HELION_INPUT_<NAME>` env var per input with the
   absolute workdir path.
4. Export one `HELION_OUTPUT_<NAME>` env var per declared output
   with the expected absolute path.

**Finalize** (after `Runtime.Run`):

- On `success=true` (exit 0 + no `KillReason`), for each
  declared output: stat the file at the expected path, compute
  SHA-256, `Put` to
  `s3://<bucket>/jobs/<job-id>/<local-path>`. Returns a
  `ResolvedOutput` slice the node agent copies into the
  `ReportResult` RPC.
- On `success=false` (failure, timeout, crash), skip uploads.
- Always clean the workdir subtree (including any
  `.helion-stage-*.tmp` files from mid-flight downloads).

Security posture: node agents started without
`HELION_ARTIFACTS_BACKEND` have `stager == nil`; their Dispatch
handler rejects any job declaring Inputs/Outputs/WorkingDir with
a descriptive error rather than running the command silently
without bindings. This blocks "unconfigured node accepts blind
run" as a failure mode.

### 5.3 Workflow artifact resolver — `internal/cluster/workflow_resolve.go`

Pure function called by the dispatch loop just before sending a
job to its assigned node:

```go
func ResolveJobInputs(job *cpb.Job, jobs JobLookup) (*cpb.Job, error)
```

Walks `job.Inputs`; for each input with a non-empty `From`
(`"<upstream_name>.<OUTPUT_NAME>"`, last-dot split so dotted job
names still work), looks up the upstream's `ResolvedOutputs`,
and rewrites the input's URI + SHA-256. Returns a defensive
copy — the persisted Job record retains the original `From`
field so lineage stays auditable across retries.

Errors are deliberate: `ErrResolveUpstreamMissing`,
`ErrResolveUpstreamNotCompleted`, `ErrResolveOutputMissing` —
each a specific transition reason the dispatch loop translates
into a `Failed` state with a descriptive message. Emits
`ml.resolve_failed` events to the bus (feature 18 Pipelines
view surfaces these).

### 5.4 Registry — `internal/registry/`

BadgerDB-backed dataset + model metadata store. Shares the
coordinator's DB under `datasets/<name>/<version>` and
`models/<name>/<version>` key prefixes (no separate DB file —
metadata is small and low-traffic compared to jobs).

```go
type Store interface {
    RegisterDataset(ctx, *Dataset) error
    GetDataset(name, version string) (*Dataset, error)
    ListDatasets(ctx, page, size int) ([]*Dataset, int, error)
    DeleteDataset(ctx, name, version string) error
    // ... parallel set for models ...
    LatestModel(name string) (*Model, error)
    ListBySourceJob(ctx, sourceJobID string) ([]*Model, error)
}
```

`Model.SourceJobID` + `Model.SourceDataset` are the lineage
pointers; the registrar is trusted to stamp them correctly
(matching the broader trust model where node-reported outputs
are gated by `attestOutputs` but user-supplied metadata at the
REST boundary rides on the JWT subject).

Validation lives in `registry/validate.go`: k8s-DNS-label
charset for names, broader charset for versions, k8s-label-
shaped bounds on tags (≤32 entries, 63-byte keys, 253-byte
values), `math.IsInf` / `math.IsNaN` rejection on metric floats,
partial lineage pointer rejection.

REST surface in `internal/api/handlers_registry.go`: every
endpoint rides the shared `authMiddleware` + per-subject
`registryQueryAllow` (2 rps burst 30); success emits an audit
event + bus event. Routes register only when
`SetRegistryStore` is called at coordinator startup, so a
non-ML deployment returns 404 from the mux.

### 5.5 Service mode — `internal/nodeserver/service_prober.go` + `internal/cluster/service_registry.go`

Long-running inference jobs with readiness probing.

**Node-side prober.** Launched by `nodeserver.Dispatch` when
`DispatchRequest.service != nil`. Forks the runtime into a
detached goroutine (so the Dispatch RPC can ACK immediately —
the subprocess never returns on its own) and runs
`probeService` alongside it:

```
loop:
  ready = GET http://127.0.0.1:<port><health_path>  (2s timeout)
  if ready != last: ReportServiceEvent(...)
  sleep 5s
  on ctx.Done(): exit (ReportServiceEvent only records live
                       state — "gone" is inferred from the job
                       reaching a terminal state)
```

Edge-triggered: a happy service emits exactly one
`service.ready` across its entire lifetime. The 4 KiB
body-drain cap on the probe response bounds goroutine cost
against a misbehaving `/healthz`.

**Coordinator-side registry.** In-memory
`map[job_id]ServiceEndpoint`; `Upsert` on every
`ReportServiceEvent`, `Delete` on the JobCompletionCallback
that fires when the service job reaches a terminal state.
Backs two endpoints:

- `GET /api/services` — list all currently-tracked services.
- `GET /api/services/{job_id}` — one service's upstream URL.

Not persisted: a coordinator restart starts with an empty map,
and the next node-side probe tick re-populates within ~5 s.
Persisting would risk surfacing stale entries pointing at gone
nodes — worse than a brief empty state.

**Workflow-scoped tokens.** The iris `submit.py` mints a
short-lived `job`-role JWT via `POST /admin/tokens` and injects
it into each job's env — in-workflow scripts that need to call
back into the registry use that scoped token instead of the
operator's root admin. `adminMiddleware` rejects the `job` role
at 403 so a leaked token cannot mint more tokens or revoke
nodes. See [ml-pipelines.md § 9](ml-pipelines.md#9-security-model--token-scoping-and-attestation).
