# Helion v2 — Architecture Reference

Helion v2 is a minimal distributed orchestrator. This document is the technical
companion to the [README](README.md). It covers component internals, protocol
contracts, persistence design, the CI/CD pipeline, runtime benchmarks, and the
key decisions behind every major choice.

---

## Table of contents

1. [v1 post-mortem](#1-v1-post-mortem)
2. [Technology decisions](#2-technology-decisions)
3. [Component design](#3-component-design) → [COMPONENTS.md](COMPONENTS.md)
4. [Persistence layer](#4-persistence-layer) → [persistence.md](persistence.md)
5. [Protocol contracts](#5-protocol-contracts)
6. [Angular dashboard design](#6-angular-dashboard-design)
7. [CI/CD pipeline](#7-cicd-pipeline)
8. [Benchmarks](#8-benchmarks--go-vs-rust-runtime) → [PERFORMANCE.md](PERFORMANCE.md)
9. [Known constraints and out-of-scope](#9-known-constraints-and-out-of-scope)
10. [Glossary](#10-glossary)
11. [Key decisions quick reference](#11-key-decisions-quick-reference)
12. [Analytics pipeline](#12-analytics-pipeline)
13. [ML pipeline](#13-ml-pipeline) → [ml-pipelines.md](ml-pipelines.md)

---

## 1. v1 post-mortem

v1 (4th semester) was a genuine success. The following table records what worked, what was
partial, and what was rebuilt from scratch in v2.

| Area | v1 status | Root cause / notes |
|---|---|---|
| Core runtime (process exec + namespaces) | ✓ Working | Correctly gated behind root / `HELION_ALLOW_ISOLATION`. |
| Node registration + heartbeat | ✓ Working | Both push and pull ran simultaneously — redundant but resilient. |
| Round-robin + least-loaded scheduler | ✓ Working | Logic correct; concurrency bug in `lastIndex` write (see below). |
| Job persistence (`cluster.json`) | ✓ Working | Atomic write-then-rename pattern is production-correct. |
| Crash recovery / lost-job detection | ✓ Working | Correctly marks running jobs as `lost` on restart. Reschedule timing was naive (see below). |
| Dashboard (HTML template) | ⚠ Partial | Blocked by `CheckHealth` deadlock in steady state. |
| Docker Compose packaging | ✓ Working | All three Dockerfiles built and composed correctly on Linux. |
| `CheckHealth()` deadlock | ✗ Bug | Held write lock while making blocking HTTP calls to every node. Starved all other goroutines. |
| `lastIndex` write under `RLock` | ✗ Bug | Writes a field while only holding `RLock` — classic TOCTOU race. Fixed with `atomic.Int64`. |
| Double-close on `Heartbeat.stop` | ✗ Bug | `Stop()` called twice (defer + explicit). Closing a closed channel panics. Fixed with `sync.Once`. |
| State round-trip of `*PersistentState` | ✗ Bug | Serialising a pointer to its own path caused silent nil on reload. State excluded from JSON; BadgerDB handles persistence in v2. |
| Timeout layering in `job.go` | ✗ Design | 5 s client timeout + 3 s select timeout: inner always fired first, making the outer meaningless. |
| `recoverLostJobs()` timing | ✗ Bug | Fired before any node had re-registered on startup. Fixed with a 15 s grace period. |
| Security | ✗ Missing | No mTLS, no token auth, no rate limiting. Acceptable for v1; not for v2. |

---

## 2. Technology decisions

### Primary language — Go 1.26

Go is the same language used by Kubernetes, Docker, etcd, Consul, and Prometheus. Its
goroutine model, first-class concurrency primitives, and single-binary output suit network
infrastructure. The gRPC, protobuf, and Kubernetes client library ecosystems in Go are
mature and well-documented.

### Future runtime language — Rust

Memory safety without a GC matters specifically for code that manipulates Linux namespaces,
cgroups, and seccomp policies — the same code `runc` and `youki` are written in. At v2
scale this is scoped to the job executor only. The runtime is isolated behind a clean
protobuf-over-Unix-socket interface; the swap changes no other component.

### Inter-node protocol — gRPC + Protocol Buffers

Replaces the v1 ad-hoc HTTP/JSON protocol. Reasons: strongly-typed contracts enforced at
compile time; bidirectional streaming for logs and heartbeats; native mTLS support;
language-agnostic contracts that make a future Rust node agent a drop-in replacement.

The coordinator's public API (consumed by the dashboard and CLI) remains REST+JSON over
HTTPS for simplicity and browser compatibility.

### Persistence — BadgerDB

BadgerDB is a pure-Go embedded key-value store with ACID transactions and an LSM-tree
design. It requires no external process (unlike etcd or PostgreSQL) and is used in
production by Dgraph. For Helion's access patterns — frequent small writes (job transitions,
heartbeats) and occasional full scans (dashboard load, crash recovery) — it is a good fit.

The business logic accesses storage only through a typed interface. Swapping BadgerDB for
etcd (for multi-coordinator HA) is a one-file change.

### Dashboard — Angular 21

Angular fills the enterprise-framework gap. The
dashboard is not a UI exercise — it consumes real WebSocket streams, renders live metrics,
and handles JWT authentication with automatic session management.

---

## 3. Component design

See [COMPONENTS.md](COMPONENTS.md) for detailed internals on the Coordinator
(registry, scheduler, job lifecycle, dispatch loop, workflow/DAG engine, crash
recovery), Node agent, and Runtime interface (Go + Rust).

---

## 4. Persistence layer

See [persistence.md](persistence.md) for the full rules, key schema, and TTL
conventions. Summary:

- No package outside `persistence/` imports BadgerDB (swap path to etcd).
- All keys built through typed constructors in `keys.go`.
- Prefixes: `nodes/`, `jobs/`, `workflows/`, `certs/`, `audit/`, `tokens/`.

---

## 5. Protocol contracts

Protocol Buffers are the single source of truth for all coordinator↔node communication.
Generated Go stubs are checked into the repository. `.proto` files live in `proto/`.

### CoordinatorService (coordinator exposes)

```protobuf
service CoordinatorService {
  // Node registers itself; coordinator issues a signed certificate.
  rpc Register(RegisterRequest) returns (RegisterResponse);

  // Long-lived bidi stream: node sends HeartbeatMessage,
  // coordinator sends NodeCommand (NOOP, SHUTDOWN, etc.).
  rpc Heartbeat(stream HeartbeatMessage) returns (stream NodeCommand);

  // Node reports job completion (success, failure, or timeout).
  rpc ReportResult(JobResult) returns (Ack);

  // Node streams real-time log chunks to the coordinator.
  rpc StreamLogs(stream LogChunk) returns (Ack);
}
```

### NodeService (node agent exposes)

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

`DispatchRequest` carries `env` (key-value map), `timeout_seconds`, and a `ResourceLimits`
block (`memory_bytes`, `cpu_quota_us`, `cpu_period_us`) forwarded by the node agent to the
runtime. Resource limits are enforced only when `HELION_RUNTIME=rust`.

### REST API — coordinator HTTP endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/healthz` | none | Liveness probe |
| `GET` | `/readyz` | none | Readiness probe with subsystem checks |
| `POST` | `/jobs` | Bearer | Submit job `{id, command, args, env, timeout_seconds, limits, priority, resources, retry_policy}` |
| `GET` | `/jobs` | Bearer | List jobs (paginated, sorted newest-first, filterable by status) |
| `GET` | `/jobs/{id}` | Bearer | Get single job |
| `POST` | `/jobs/{id}/cancel` | Bearer | Cancel a non-terminal job |
| `GET` | `/jobs/{id}/logs` | Bearer | Retrieve stored job stdout/stderr (`?tail=N`) |
| `POST` | `/workflows` | Bearer | Submit workflow DAG `{id, name, priority, jobs}` |
| `GET` | `/workflows` | Bearer | List workflows (paginated, sorted newest-first) |
| `GET` | `/workflows/{id}` | Bearer | Get workflow with job statuses |
| `DELETE` | `/workflows/{id}` | Bearer | Cancel a running workflow |
| `GET` | `/nodes` | Bearer | List registered nodes with capacity |
| `GET` | `/audit` | Bearer | Paginated audit log |
| `GET` | `/metrics` | none (Prometheus) | Prometheus text metrics |
| `POST` | `/admin/nodes/{id}/revoke` | Bearer (admin) | Revoke node registration |
| `POST` | `/admin/tokens` | Bearer (admin) | Issue scoped JWT `{subject, role, ttl_hours}` |
| `DELETE` | `/admin/tokens/{jti}` | Bearer (admin) | Immediately revoke a token by JTI |
| `GET` | `/ws/jobs/{id}/logs` | First-message | WebSocket live log stream |
| `GET` | `/ws/metrics` | First-message | WebSocket live cluster metrics |
| `GET` | `/ws/events` | First-message | WebSocket event stream (subscribe with topic patterns) |
| `POST` | `/api/datasets` | Bearer | Register a dataset `{name, version, uri, size_bytes, sha256, tags}` (feature 16) |
| `GET` | `/api/datasets` | Bearer | List datasets (paginated) |
| `GET` | `/api/datasets/{name}/{version}` | Bearer | Fetch single dataset |
| `DELETE` | `/api/datasets/{name}/{version}` | Bearer | Delete dataset metadata (bytes remain) |
| `POST` | `/api/models` | Bearer | Register a model with lineage `{name, version, uri, framework, source_job_id, source_dataset, metrics}` |
| `GET` | `/api/models` | Bearer | List models (paginated) |
| `GET` | `/api/models/{name}/latest` | Bearer | Fetch most-recently-registered version by `CreatedAt` |
| `GET` | `/api/models/{name}/{version}` | Bearer | Fetch single model |
| `DELETE` | `/api/models/{name}/{version}` | Bearer | Delete model metadata (bytes remain) |
| `GET` | `/api/services` | Bearer | List live inference-service endpoints (feature 17) |
| `GET` | `/api/services/{job_id}` | Bearer | Lookup one service's upstream URL |
| `GET` | `/workflows/{id}/lineage` | Bearer | Workflow DAG joined with job status + ResolvedOutputs + registered models (feature 18 Pipelines view) |

---

## 6. Angular dashboard design

### Component tree

```
AppComponent  (shell: nav sidebar + router outlet)
├── AuthModule
│   └── LoginComponent           # Token entry form
├── NodesModule (lazy)
│   ├── NodeListComponent        # Table with health badges, auto-refresh 10 s
│   └── NodeDetailComponent      # Single node: metrics + job history
├── JobsModule (lazy)
│   ├── JobListComponent         # Paginated, filterable, sortable table
│   └── JobDetailComponent       # Metadata + live log viewer (WebSocket)
├── WorkflowsModule (lazy)
│   ├── WorkflowListComponent    # Paginated workflow table with status badges
│   └── WorkflowDetailComponent  # DAG job cards with statuses, cancel action
├── MetricsModule (lazy)
│   └── ClusterMetricsComponent  # Chart.js time-series, summary cards
└── AuditModule (lazy)
    └── AuditLogComponent        # Paginated audit event table, type filter
```

### Technology choices

| Concern | Choice |
|---|---|
| Framework | Angular 21 (standalone components, signals) |
| Components | Angular Material (table, badge, card, form) |
| Async | RxJS — WebSocket streams as Observables, `HttpClient` with interceptors |
| Charts | ng2-charts / Chart.js |
| Router | Lazy-loaded feature modules; `AuthGuard` on all protected routes |

### Dashboard security

See [SECURITY.md](SECURITY.md#9-dashboard-security) for the full dashboard
security contract (JWT in-memory only, first-message WebSocket auth, CSP).

---

## 7. CI/CD pipeline

### Workflow structure

| Trigger | Job | Steps |
|---|---|---|
| Every push / PR | `build` | `go vet` · `golangci-lint` · `go test -race -count=1 ./...` · coverage gates (internal/ ≥ 85%, cmd/ ≥ 24%) |
| Every push / PR | `test-rust` | `cargo clippy -D warnings` · `cargo llvm-cov` with ≥ 85% coverage gate |
| Every push / PR | `test-dashboard` | `npm ci` · `ng lint` · `ng test --browsers=ChromeHeadless` with coverage thresholds |
| After unit suites pass | `e2e` | Build Docker images · boot cluster · wait for healthy nodes · run Playwright E2E suite · tear down |
| After all suites pass | `snyk` | `snyk test --severity-threshold=high` (Go deps) · `snyk container test` (coordinator image) |
| After all suites pass | `docker` | `docker buildx build` for coordinator and node images (cache to GHA) |

The `e2e` job runs after `build` (Go) and `test-dashboard` (Angular) pass. It builds
coordinator + node Docker images, starts the full cluster via `docker-compose.e2e.yml`,
waits for at least one healthy node, then runs Playwright against the live dashboard.
On failure, CI uploads the Playwright HTML report, traces, and cluster Docker logs as
artifacts for debugging.

### Test categories

- **Unit tests.** Co-located with source (`*_test.go`). Cover scheduler policies, BadgerDB
  helpers, rate limiter, JWT issuance. Always run with `-race`.
- **Integration tests.** In `tests/integration/`. Spin up a coordinator and node agents
  within the test process against real BadgerDB (temp dir, cleaned after).
- **Security tests.** In `tests/integration/security/`. TLS rejection, invalid JWT, revoked
  node certificate, rate limit enforcement, audit log completeness.
- **Angular unit tests.** Karma + Jasmine. Coverage thresholds (85% statements / 60%
  branches / 85% functions / 85% lines) are enforced by
  `scripts/check-dashboard-coverage.sh` — the `@angular-devkit/build-angular:karma`
  builder ignores the `check:` block in `karma.conf.js`, so the script parses the
  generated HTML report and fails the build when a metric drops below its minimum.
- **E2E tests.** In `dashboard/e2e/`. Playwright specs covering the full path from
  coordinator + nodes (gRPC registration, job dispatch) through the Angular dashboard
  (login, nodes, jobs, metrics, audit, analytics). Tests run against a real cluster —
  no mocks.
- **Benchmarks.** In `tests/bench/`. Measure Go vs Rust runtime latency and throughput.
  See §8.

---

## 8. Benchmarks — Go vs Rust runtime

See [PERFORMANCE.md](PERFORMANCE.md) for full benchmark results, reproduction
instructions, cgroup v2 overhead measurements, and seccomp filtering latency.

---

## 9. Known constraints and out-of-scope

- **Single coordinator replica.** HA (active-passive or Raft) is architecturally possible
  via the BadgerDB → etcd swap but not in v2 scope.
- **Workflows are single-coordinator.** DAG execution runs on one coordinator instance.
  HA would require distributed locking for workflow state transitions.
- **No multi-tenancy.** All jobs share a single namespace. Per-tenant RBAC is a v3 concern.
- **No GPU scheduling.** Requires Kubernetes device plugin integration.
- **No MapReduce demo.** Deferred to keep v2 focused on infrastructure correctness.
- **Linux-only isolation.** Cross-compiled binaries run on other OS targets without
  namespace isolation (`HELION_ALLOW_ISOLATION=false`), suitable for local development.
- **Namespace isolation requires root or `CAP_SYS_ADMIN`.** Set in the DaemonSet
  `SecurityContext` in Kubernetes.

---

## 10. Glossary

| Term | Definition |
|---|---|
| **BadgerDB** | Embeddable key-value store in Go. LSM-tree design, ACID transactions, optional per-key TTL. No external process required. |
| **cgroup v2** | Linux control group v2. Used by the Rust runtime to enforce per-job CPU and memory limits. |
| **Cloud-agnostic** | Deployable to any conformant Kubernetes cluster without code changes. Differences between cloud providers are in Helm values files. |
| **ML-DSA (Dilithium)** | NIST FIPS 204. Lattice-based digital signature algorithm. Used to sign node certificates. Resistant to quantum attacks. |
| **ML-KEM (Kyber)** | NIST FIPS 203. Lattice-based key encapsulation mechanism. Used in hybrid TLS key exchange. Resistant to quantum attacks. |
| **DaemonSet** | Kubernetes workload type that ensures one pod runs on every (or selected) node. Used for Helion node agents. |
| **gRPC** | Google RPC framework using HTTP/2 and Protocol Buffers. Supports streaming, cancellation, and mTLS natively. |
| **Harvest-now-decrypt-later** | Attack where an adversary records encrypted traffic today to decrypt once a quantum computer is available. |
| **Helm** | Package manager for Kubernetes. A chart is a parameterised collection of manifests. |
| **Hybrid TLS** | TLS that negotiates both a classical (X25519) and post-quantum (ML-KEM) key exchange simultaneously. Breaking the session requires breaking both. |
| **JTI (JWT ID)** | Unique identifier in a JWT. Storing it in BadgerDB with a TTL enables sub-second revocation. |
| **mTLS** | Mutual TLS. Both client and server present and verify certificates. Prevents unauthorised nodes from connecting. |
| **PQC** | Post-Quantum Cryptography. NIST completed standardisation of ML-KEM and ML-DSA in 2024. |
| **seccomp-bpf** | Linux kernel feature that restricts which syscalls a process can make. Used by the Rust runtime for job isolation. |
| **Artifact store** | Object-storage abstraction for ML job bytes. S3-compatible (MinIO in dev); `file://` fallback for local testing. Addressed by `s3://<bucket>/jobs/<job-id>/<path>`. See [ml-pipelines.md](ml-pipelines.md). |
| **Stager** | Node-side component that prepares a per-job working directory: downloads declared inputs before `Run()`, uploads declared outputs after exit 0. Drives the `HELION_INPUT_*` / `HELION_OUTPUT_*` env vars. |
| **`from:` reference** | Workflow YAML syntax for "rewrite this input's URI to the upstream job's resolved output URI at dispatch time." Splits at last dot so workflow job names with dots still work. |
| **ResolvedOutputs** | Per-job record of the `(name, uri, sha256, size)` tuples the coordinator persists once the stager uploads on exit 0. Downstream `from:` references read from here. Attested via scheme + prefix + suffix + declared-name checks. |
| **Dataset / model registry** | Metadata store for named, versioned ML artifacts. Lineage-bearing (model → source job → source dataset) and metric-bearing. Bytes stay in the artifact store; registry holds pointers. |
| **Service job** | Long-running job with a `service: {port, health_path}` block. Runtime skips timeout enforcement; node runs a readiness prober and reports ready/unhealthy transitions. Exposed via `GET /api/services/{id}`. |
| **CUDA_VISIBLE_DEVICES** | Env var set by the runtime on GPU jobs (list of claimed device indices) and on CPU jobs running on GPU-equipped nodes (empty string, hides all devices from the process). |

---

## 11. Key decisions quick reference

| Decision | Choice | Rationale |
|---|---|---|
| Primary language | Go | Native K8s/Docker ecosystem; first-class gRPC; same choice as etcd, Consul, Prometheus. |
| Runtime language | Rust | Memory safety matters for namespace/cgroup/seccomp code; isolated behind a clean Unix socket interface. |
| Inter-node protocol | gRPC + Protobuf | Typed contracts, streaming, mTLS-native, language-agnostic (enables Rust node later). |
| Persistence | BadgerDB | Embedded, no external process, ACID, pure Go. Swap path to etcd for HA. |
| Frontend | Angular 21 | Enterprise framework with real WebSocket + auth complexity. |
| Key exchange | ML-KEM hybrid | NIST FIPS 203. Hybrid maintains classical compatibility while adding quantum resistance. Low cost at design time. |
| Signatures | ML-DSA hybrid | NIST FIPS 204. Node certificate signing, hybrid with ECDSA during transition. |
| Deployment | Kubernetes + Helm | True cloud agnosticism. Coordinator = Deployment, Agents = DaemonSet. One chart, per-cloud values files. |
| CI/CD | GitHub Actions + Snyk | Free for public repos, native to GitHub. Snyk added for dependency and container CVE scanning. |
| Health model | Push heartbeat stream | Node maintains gRPC stream; coordinator does not poll. Eliminates the v1 `CheckHealth` deadlock by design. |
| Crash recovery | Grace period + retry | Fixes v1 naive recovery. 15 s default grace period; configurable. |
| JWT storage (dashboard) | In-memory only | Never `localStorage`/`sessionStorage`. Intentional for a security-focused project. |
| Audit log | Append-only, BadgerDB | Every security and job event recorded. Never updated or deleted in normal operation. |
| Analytics store | PostgreSQL (opt-in) | Triple-tier storage: BadgerDB for operational hot path, object store for ML artifact bytes, PostgreSQL for historical analytics. Opt-in via `HELION_ANALYTICS_DSN`. |
| ML artifact store | S3-compatible, `file://` fallback | Separate from operational metadata: terabytes of training bytes don't belong in BadgerDB. Addressed by `s3://<bucket>/jobs/<job-id>/<path>`; integrity via SHA-256 propagated end-to-end. See [ml-pipelines.md](ml-pipelines.md). |
| ML artifact passing | `from:` references resolved at dispatch | Data flow between workflow jobs is declarative: `from: upstream.OUTPUT` rewrites to the concrete URI once the upstream reports `ResolvedOutputs`. Resolver reads only the coordinator's persisted attested record — compromised node cannot inject foreign URIs. |
| Inference serving | In-process service jobs with readiness probes | No separate serving control plane. `service: {port, health_path}` on any job turns it into a long-running subprocess; node runs an edge-triggered probe loop and surfaces the upstream URL via `/api/services/{id}`. Routing / LB / autoscaling out of scope. |

---

## 12. Analytics pipeline

The analytics pipeline exports event data from the operational system into a PostgreSQL
database for historical querying and dashboard visualisation.

### Dual-database design

BadgerDB remains the **operational store** — low-latency reads/writes for dispatch,
heartbeats, and state transitions. PostgreSQL is the **analytical store** — append-only
event facts, populated asynchronously, queried by the analytics dashboard.

```
Coordinator ──▶ Event Bus ──▶ Analytics Sink ──▶ PostgreSQL
  (BadgerDB)     (in-memory)   (batch writer)     (analytics)
```

### Data flow

1. Every state transition emits an event on the in-memory bus (10 topic types).
2. The analytics `Sink` subscribes to `"*"` and buffers events in memory.
3. Every 500 ms or 100 events (configurable), the sink flushes to PostgreSQL:
   - Batch INSERT into the `events` fact table (idempotent via `ON CONFLICT`).
   - Upsert `job_summary` and `node_summary` tables for fast dashboard queries.
4. The `/api/analytics/*` REST endpoints query PostgreSQL and return JSON.
5. The Angular analytics dashboard at `/analytics` visualises the results.

### Opt-in activation

Set `HELION_ANALYTICS_DSN` to a PostgreSQL connection string. When unset, nothing
happens — no PostgreSQL dependency, zero overhead. Schema migrations run automatically
on startup.

### Backfill

The `analytics.Backfill()` function reads the existing BadgerDB audit trail and
inserts historical events into PostgreSQL, so analytics coverage extends back before
the sink was deployed. Idempotent — safe to run multiple times.

---

## 13. ML pipeline

Helion's ML slice (features 11–19) turns the base orchestrator
into one that runs a training → registry → serve pipeline
end-to-end without a separate ML control plane. See
[ml-pipelines.md](ml-pipelines.md) for the user-facing guide;
this section is the architecture brief.

### Three-tier storage

The operational dual-store (BadgerDB + optional PostgreSQL)
extends to three tiers when ML is in use:

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

Each tier has a distinct access pattern:

- **BadgerDB (operational metadata).** Small, frequent writes
  (job transitions, heartbeats, registry records). Authoritative
  for `ResolvedOutputs` — the attested `(name, uri, sha256,
  size)` tuples downstream jobs reference.
- **Object store (ML artifact bytes).** Large, infrequent writes
  (one upload per job output on exit 0). Addressed by
  `s3://<bucket>/jobs/<job-id>/<path>`. Never reached by the
  coordinator — the node-side Stager is the only writer. Integrity
  via SHA-256 propagated with every URI.
- **PostgreSQL (analytics).** Append-only historical facts for
  the analytics dashboard. Opt-in via `HELION_ANALYTICS_DSN`.

BadgerDB is **never written** by ML hot paths that move bytes;
the object store is **never read** by the dashboard. Each tier's
unavailability degrades a different capability:

| Tier down | Effect |
|---|---|
| BadgerDB | Coordinator unusable — scheduling + registry + auth all blocked |
| Object store | New ML jobs fail at Stager.Finalize (upload) or dispatch (download); operational control plane unaffected |
| PostgreSQL | Analytics dashboard loses historical data; operational state intact |

### Component map

See [COMPONENTS.md § 5](COMPONENTS.md#5-ml-subsystems) for
internals. Summary:

- **`internal/artifacts/`** — `Store` interface with `LocalStore`
  + `S3Store` implementations. `GetAndVerify` reads a URI into a
  capped buffer, hashes it, and returns `ErrChecksumMismatch` on
  digest mismatch.
- **`internal/staging/`** — node-side `Stager` that prepares
  per-job workdirs (downloads inputs, mints `HELION_INPUT_*`)
  and finalises on exit 0 (uploads outputs, emits resolved URIs
  back to the node agent for `ReportResult`).
- **`internal/cluster/workflow_resolve.go`** — dispatch-time
  resolver that rewrites `from:` references to the upstream's
  attested URI + SHA-256.
- **`internal/registry/`** — BadgerDB-backed dataset + model
  registry. Shares the coordinator's DB under `datasets/<name>/
  <version>` and `models/<name>/<version>` prefixes.
- **`internal/cluster/service_registry.go`** — in-memory map of
  live inference-service endpoints. Populated by
  `ReportServiceEvent`, cleared on JobCompletionCallback.
  Deliberately not persisted — a stale entry from a previous
  coordinator run would point at a gone node:port; next probe
  tick re-populates.
- **`internal/nodeserver/service_prober.go`** — long-running
  goroutine alongside a service job's subprocess. Polls
  `127.0.0.1:<port><health_path>` every 5 s, emits
  edge-triggered ready/unhealthy events (never one per tick).

### Wire contracts added

Two proto additions in `proto/node.proto`:

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

One new `ReportServiceEvent` RPC on `CoordinatorService` (feature
17) that the node-side prober calls on ready/unhealthy
transitions.

### Trust boundary

The coordinator is the trust boundary for cross-job data flow.
Nodes never talk to each other; every artifact URI that reaches
a downstream job's inputs has been:

1. Reported by the upstream node via `ReportResult`.
2. Filtered through `attestOutputs` (scheme + `jobs/<job-id>/`
   prefix + `local_path` suffix + declared-name match).
3. Persisted on the upstream's Job record.
4. Read back by the resolver at the downstream's dispatch time.

A compromised node cannot inject cross-job URIs, foreign schemes,
renamed outputs, or undeclared output names. Integrity of the
*bytes* behind the URI is enforced by the downstream's Stager
via `artifacts.GetAndVerify` — the SHA-256 travels with the URI,
so store-side MITM, leaked S3 credentials, and bit rot are all
caught before the downstream process sees the file. See
[ml-pipelines.md § 9](ml-pipelines.md#9-security-model--token-scoping-and-attestation)
for the full model and [SECURITY.md § 1](SECURITY.md#1-threat-model)
for the threat table.
