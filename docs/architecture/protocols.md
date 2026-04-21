> **Audience:** engineers
> **Scope:** gRPC, REST, WebSocket, and event-bus contracts that shape coordinator↔node and dashboard↔coordinator traffic.
> **Depth:** reference

# Protocols

Protocol Buffers are the single source of truth for all coordinator↔node
communication. Generated Go stubs are checked in; `.proto` files live under
[`../../proto/`](../../proto/).

## 1. gRPC

### `CoordinatorService` (coordinator exposes)

```protobuf
service CoordinatorService {
  // Node registers itself; coordinator issues a signed certificate.
  rpc Register(RegisterRequest) returns (RegisterResponse);

  // Long-lived bidi stream: node sends HeartbeatMessage,
  // coordinator sends NodeCommand (NOOP, SHUTDOWN, …).
  rpc Heartbeat(stream HeartbeatMessage) returns (stream NodeCommand);

  // Node reports job completion (success, failure, or timeout).
  rpc ReportResult(JobResult) returns (Ack);

  // Node streams real-time log chunks to the coordinator.
  rpc StreamLogs(stream LogChunk) returns (Ack);

  // Node reports a service-job readiness transition (feature 17).
  rpc ReportServiceEvent(ServiceEvent) returns (Ack);
}
```

### `NodeService` (node agent exposes)

```protobuf
service NodeService {
  // Coordinator dispatches a job to this node.
  rpc Dispatch(DispatchRequest) returns (DispatchAck);

  // Coordinator requests cancellation of a running job.
  rpc Cancel(CancelRequest) returns (Ack);

  // Coordinator requests a current resource snapshot.
  rpc GetMetrics(Empty) returns (NodeMetrics);
}
```

`DispatchRequest` carries `env` (key-value map), `timeout_seconds`, and a
`ResourceLimits` block (`memory_bytes`, `cpu_quota_us`, `cpu_period_us`)
forwarded by the node agent to the runtime. Resource limits are enforced
only when `HELION_RUNTIME=rust`.

### ML additions

Two proto additions in `proto/node.proto` shipped with features 11–19:

```protobuf
message DispatchRequest {
  // ... existing fields ...
  repeated ArtifactBinding inputs  = 10;   // feature 12
  repeated ArtifactBinding outputs = 11;
  ServiceSpec              service = 12;   // feature 17
  uint32                   gpus    = 13;   // feature 15
  map<string, string>      node_selector = 14; // feature 14
}

message ArtifactBinding {
  string name       = 1;
  string uri        = 2;
  string local_path = 3;
  string sha256     = 4;  // feature 13 — verified-download attestation
}

message ServiceSpec {
  uint32 port              = 1;
  string health_path       = 2;
  uint32 health_initial_ms = 3;
}
```

One new `ReportServiceEvent` RPC on `CoordinatorService` that the
node-side prober calls on ready/unhealthy transitions
(see [components.md § 5.5](components.md#55-service-mode---internalnodeserverservice_proberg--internalclusterservice_registryg)).

## 2. REST

Every submit/register endpoint below accepts the `?dry_run=true` query
parameter (feature 24). A dry-run request runs through the full
middleware chain (auth → rate limit → body cap → validators), then
returns `200 OK` with `{"dry_run": true, ...}` instead of performing any
durable write, bus publish, or dispatch. Dry-run emits a distinct audit
event (`job_dry_run`, `workflow_dry_run`, `dataset.dry_run`,
`model.dry_run`) so reviewers can filter probes from real submissions.
Accepted values: `1`/`true`/`yes` (truthy), `0`/`false`/`no`/empty
(falsy); any other value returns `400` so a typo never silently becomes
a real submission.

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/healthz` | none | Liveness probe |
| `GET` | `/readyz` | none | Readiness probe with subsystem checks |
| `POST` | `/jobs` | Bearer | Submit job `{id, command, args, env, timeout_seconds, limits, priority, resources, retry_policy}` (`?dry_run=true`) |
| `GET` | `/jobs` | Bearer | List jobs (paginated, sorted newest-first, filterable by status) |
| `GET` | `/jobs/{id}` | Bearer | Get single job |
| `POST` | `/jobs/{id}/cancel` | Bearer | Cancel a non-terminal job |
| `GET` | `/jobs/{id}/logs` | Bearer | Retrieve stored job stdout/stderr (`?tail=N`) |
| `POST` | `/workflows` | Bearer | Submit workflow DAG `{id, name, priority, jobs}` (`?dry_run=true`) |
| `GET` | `/workflows` | Bearer | List workflows (paginated) |
| `GET` | `/workflows/{id}` | Bearer | Get workflow with job statuses |
| `DELETE` | `/workflows/{id}` | Bearer | Cancel a running workflow |
| `GET` | `/workflows/{id}/lineage` | Bearer | Feature 18 — DAG + job status + ResolvedOutputs + registered models |
| `GET` | `/nodes` | Bearer | List registered nodes with capacity |
| `GET` | `/audit` | Bearer | Paginated audit log |
| `GET` | `/metrics` | none (Prometheus) | Prometheus text metrics |
| `POST` | `/admin/nodes/{id}/revoke` | Bearer (admin) | Revoke node registration |
| `POST` | `/admin/tokens` | Bearer (admin) | Issue scoped JWT `{subject, role, ttl_hours, bind_to_cert_cn?}` |
| `DELETE` | `/admin/tokens/{jti}` | Bearer (admin) | Immediately revoke a token by JTI |
| `POST` | `/admin/jobs/{id}/reveal-secret` | Bearer (admin) | Feature 26 — read back a declared secret env value `{key, reason}`. Reason is mandatory and audited; every reject is audited too. Rate-limited 1/5s. |
| `POST` | `/admin/operator-certs` | Bearer (admin) | Feature 27 — mint a PKCS#12 client-cert bundle `{common_name, ttl_days, p12_password}`. Returns cert + key + P12 once. Rate-limited 1/10s. |
| `POST` | `/admin/operator-certs/{serial}/revoke` | Bearer (admin) | Feature 31 — revoke an operator cert. |
| `GET` | `/admin/operator-certs/revocations` | Bearer (admin) | Feature 31 — list revocations. |
| `GET` | `/admin/ca/crl` | Bearer (admin) | Feature 31 — PEM-encoded X.509 CRL. |
| `POST` | `/admin/webauthn/register-begin` | Bearer (admin) | Feature 34 — start credential registration. |
| `POST` | `/admin/webauthn/register-finish` | Bearer (admin) | Feature 34 — persist attested credential. |
| `POST` | `/admin/webauthn/login-begin` | Bearer (admin) | Feature 34 — start assertion ceremony. |
| `POST` | `/admin/webauthn/login-finish` | Bearer (admin) | Feature 34 — mint WebAuthn-backed JWT. |
| `GET` | `/admin/webauthn/credentials` | Bearer (admin) | Feature 34 — list registered credentials. |
| `DELETE` | `/admin/webauthn/credentials/{id}` | Bearer (admin) | Feature 34 — revoke a credential. |
| `POST` | `/admin/groups` | Bearer (admin) | Feature 38 — create a group. |
| `GET` | `/admin/groups[/{name}]` | Bearer (admin) | List / fetch. |
| `DELETE` | `/admin/groups/{name}` | Bearer (admin) | Delete. |
| `POST` | `/admin/groups/{name}/members` | Bearer (admin) | Add member `{principal_id}`. |
| `DELETE` | `/admin/groups/{name}/members/{id}` | Bearer (admin) | Remove member. |
| `POST` | `/admin/resources/{kind}/share` | Owner / admin | Feature 38 — add share. |
| `GET` | `/admin/resources/{kind}/shares` | Owner / admin | List shares. |
| `DELETE` | `/admin/resources/{kind}/share` | Owner / admin | Revoke share. |
| `POST` | `/admin/secretstore/rotate` | Bearer (admin) | Feature 30 — rewrap envelopes under the active KEK version. |
| `GET` | `/admin/secretstore/status` | Bearer (admin) | Feature 30 — loaded KEK versions + active version. |
| `POST` | `/api/datasets` | Bearer | Register a dataset `{name, version, uri, size_bytes, sha256, tags}` (`?dry_run=true`) |
| `GET` | `/api/datasets` | Bearer | List datasets (paginated) |
| `GET` | `/api/datasets/{name}/{version}` | Bearer | Fetch single dataset |
| `DELETE` | `/api/datasets/{name}/{version}` | Bearer | Delete dataset metadata (bytes remain) |
| `POST` | `/api/models` | Bearer | Register a model with lineage (`?dry_run=true`) |
| `GET` | `/api/models` | Bearer | List models (paginated) |
| `GET` | `/api/models/{name}/latest` | Bearer | Most-recently-registered version by `CreatedAt` |
| `GET` | `/api/models/{name}/{version}` | Bearer | Fetch single model |
| `DELETE` | `/api/models/{name}/{version}` | Bearer | Delete model metadata |
| `GET` | `/api/services` | Bearer | Feature 17 — list live inference-service endpoints |
| `GET` | `/api/services/{job_id}` | Bearer | Lookup one service's upstream URL |

### Analytics endpoints (feature 09 + 28)

All behind `authMiddleware + analyticsPreflight` (5 rps / 60 burst
per-subject; see
[security/auth.md § 2](../security/auth.md#2-rate-limiting)). Every
successful query audits as `analytics.query`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/analytics/throughput` | Hourly job counts, avg + p95 duration by status |
| `GET` | `/api/analytics/node-reliability` | Per-node failure rates and health history |
| `GET` | `/api/analytics/retry-effectiveness` | Retried vs first-attempt outcomes |
| `GET` | `/api/analytics/queue-wait` | Avg / p95 pending → running wait per hour |
| `GET` | `/api/analytics/workflow-outcomes` | Workflow success / failure by day |
| `GET` | `/api/analytics/events` | Paginated raw event query with type filter |
| `GET` | `/api/analytics/submission-history` | Feature 28 — per-operator submission history (accepted, rejected, dry-run) |
| `GET` | `/api/analytics/auth-events` | Feature 28 — logins, token mints, auth failures, rate-limits |
| `GET` | `/api/analytics/unschedulable` | Feature 28 — selector-driven capacity gap |
| `GET` | `/api/analytics/registry-growth` | Feature 28 — dataset + model register/delete counts by day |
| `GET` | `/api/analytics/service-probe` | Feature 28 — service readiness transitions (ready ↔ unhealthy ↔ gone) |
| `GET` | `/api/analytics/artifact-throughput` | Feature 28 — upload + download bytes over time |
| `GET` | `/api/analytics/job-logs` | Feature 28 — PG-backed job log lines (operational-window retention) |
| `GET` | `/api/analytics/ml-runs` | Feature 40 — workflow_outcomes rollup (winner/runner-up metrics) |

## 3. WebSocket

Three read-only WebSocket endpoints on the coordinator. Each
authenticates via the **first-message pattern**: after `onopen`, the
first frame carries the Bearer token; subsequent frames carry topic or
filter subscriptions. Tokens NEVER travel as URL query parameters —
keeps them out of server logs, browser history, and `Referer` headers.

| Path | Purpose |
|---|---|
| `GET /ws/jobs/{id}/logs` | Live log stream for one job |
| `GET /ws/metrics` | Live cluster metrics |
| `GET /ws/events` | Event stream (client subscribes with topic patterns) |

## 4. Event bus (internal)

`internal/events.Bus` is a process-local in-memory pub/sub. Every
state transition, registry mutation, log chunk, security event, and
service-readiness transition emits a typed event. The bus is the join
point between:

1. The WebSocket `/ws/events` stream.
2. The analytics `Sink` (feature 09) — batched writer to PostgreSQL.
3. The log-reconciler (feature 28) — drives BadgerDB-to-PG confirmation.

Topics use dot-notation (`job.state_transition`, `workflow.completed`,
`ml.resolve_failed`, `service.ready`, `authz.deny`, …). Subscribers
match via prefix patterns (`"*"`, `"job.*"`, `"ml.resolve_failed"`).

Per-event publish is synchronous (callers block on a bounded channel
send); subscribers process asynchronously. Backpressure beyond the
bound drops events with a counter incremented on the `events_dropped`
metric — the coordinator's hot path is never blocked by a slow
subscriber.

## 5. Three-tier storage (ML pipeline)

The operational dual-store (BadgerDB + optional PostgreSQL) extends
to three tiers when ML is in use:

```
                      ┌─────────────────┐
  operational metadata│    BadgerDB     │  <── Stager.Finalize attests
       + ResolvedOutputs│  (coordinator)  │      the URI here
                      └────────┬────────┘
                               │ reads
                   ┌───────────▼────────────┐
                   │   Node Stager (S3)     │  <── downloads / uploads
                   │   ┌──────────────────┐ │      artifact bytes
                   │   │  Object store    │ │
 ML artifact bytes │   │  (S3 / MinIO)    │ │
                   │   │  file:// fallback│ │
                   │   └──────────────────┘ │
                   └────────────────────────┘

  historical events  ┌─────────────────┐
                     │   PostgreSQL    │  <── analytics sink, opt-in
                     │   (analytics)   │
                     └─────────────────┘
```

Tier responsibilities:

| Tier | Access pattern | Backing |
|---|---|---|
| BadgerDB | Small, frequent writes (job transitions, heartbeats, registry). Authoritative for `ResolvedOutputs`. | [persistence.md](persistence.md). |
| Object store | Large, infrequent writes (one upload per job output on exit 0). Addressed by `s3://<bucket>/jobs/<job-id>/<path>`. Never reached by the coordinator — the node-side Stager is the only writer. | [components.md § 5.1](components.md#51-artifact-store---internalartifacts). |
| PostgreSQL | Append-only historical facts. Opt-in via `HELION_ANALYTICS_DSN`. | [components.md § 4](components.md#4-analytics-pipeline). |

Tier-unavailability effect:

| Tier down | Effect |
|---|---|
| BadgerDB | Coordinator unusable — scheduling + registry + auth all blocked |
| Object store | New ML jobs fail at `Stager.Finalize` (upload) or dispatch (download); operational control plane unaffected |
| PostgreSQL | Analytics dashboard loses historical data; operational state intact |

Cross-job data flow is mediated by the coordinator as trust boundary
— nodes never talk to each other. Every artifact URI that reaches a
downstream job's inputs has been reported by the upstream node via
`ReportResult`, filtered through `attestOutputs`, persisted on the
upstream's Job record, and read back by the resolver at dispatch time.
A compromised node cannot inject cross-job URIs, foreign schemes,
renamed outputs, or undeclared output names. Integrity of the bytes
is enforced by the downstream's Stager via `artifacts.GetAndVerify`;
the SHA-256 travels with the URI, so store-side MITM, leaked S3
credentials, and bit rot are all caught before the downstream process
sees the file. See
[guides/ml-pipelines.md § 9](../guides/ml-pipelines.md#9-security-model--token-scoping-and-attestation)
and [security/README.md § Data plane](../security/README.md#data-plane-audit-secrets-logs-analytics-see-data-planemd).
