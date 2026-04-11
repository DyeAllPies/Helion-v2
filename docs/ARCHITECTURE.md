# Helion v2 — Architecture Reference

This document is the technical companion to the [README](../README.md). It covers
component internals, protocol contracts, persistence design, the CI/CD pipeline, runtime
benchmarks, and the key decisions behind every major choice.

---

## Table of contents

1. [v1 post-mortem](#1-v1-post-mortem)
2. [Technology decisions](#2-technology-decisions)
3. [Component design](#3-component-design)
4. [Persistence layer](#4-persistence-layer)
5. [Protocol contracts](#5-protocol-contracts)
6. [Angular dashboard design](#6-angular-dashboard-design)
7. [CI/CD pipeline](#7-cicd-pipeline)
8. [Benchmarks — Go vs Rust runtime](#8-benchmarks--go-vs-rust-runtime)
9. [Known constraints and out-of-scope](#9-known-constraints-and-out-of-scope)
10. [Glossary](#10-glossary)
11. [Key decisions quick reference](#11-key-decisions-quick-reference)

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

### Dashboard — Angular 18

React and Vue were already covered. Angular fills the enterprise-framework gap. The
dashboard is not a UI exercise — it consumes real WebSocket streams, renders live metrics,
and handles JWT authentication with automatic session management.

---

## 3. Component design

### 3.1 Coordinator

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
pending → dispatching → running → completed
                                → failed
                                → lost
```

All transitions are persisted atomically and written to the audit log.

**Certificate Authority.** Issues per-node X.509 certificates on first registration using
ML-DSA (Dilithium-3) in hybrid mode with ECDSA. Acts as the cluster's internal CA.

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

### 3.2 Node agent

Each node agent is a long-lived process on a worker host.

- **Self-registration.** Contacts the coordinator via gRPC on startup, presents its
  certificate, and registers. If no certificate exists, initiates the issuance flow.
- **Heartbeat.** Maintains a bidi-streaming gRPC call to the coordinator at a configurable
  interval (default 10 s). The coordinator does not poll — it passively monitors the stream.
- **Job execution.** Receives dispatch RPCs, hands off to the runtime layer, streams log
  chunks back to the coordinator in real time.
- **Local metrics.** Exposes a `/metrics` endpoint in Prometheus text format.

### 3.3 Runtime interface

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

**RustRuntime** (Phase 6) — communicates with the `helion-runtime` Rust binary over a Unix
domain socket using protobuf-framed messages. Adds cgroup v2 resource limits and
seccomp-bpf syscall filtering. Enabled by setting `HELION_RUNTIME_SOCKET`.

The selector logic:

```
HELION_RUNTIME_SOCKET set + socket reachable  → RustRuntime
otherwise                                      → GoRuntime
```

---

## 4. Persistence layer

### Rules

**No package outside `persistence/` imports BadgerDB.** All storage access goes through
`Store`. This is the boundary that makes the swap path to etcd possible without touching
business logic.

**All keys are built through the typed constructors in `keys.go`.** Never write
`[]byte("nodes/" + addr)` in a business-logic file. Use `persistence.NodeKey(addr)`. A
rename is then a one-file change.

**Proto types are the wire format.** `Put[T]` and `Get[T]` only accept `proto.Message`
values. The sole exception is `PutRaw`/`GetRaw`, reserved for X.509 DER bytes under
`certs/`.

**TTL is explicit.** `Put` never sets a TTL. If a value must expire (`nodes/`, `tokens/`),
use `PutWithTTL`. This makes expiry intent visible at the call site.

**Audit entries are append-only.** Use `AppendAudit`. Never `Put` to a key under `audit/`.

### Key schema

| Prefix | Value type | TTL |
|---|---|---|
| `nodes/{addr}` | `Node` (proto) | 2× heartbeat interval |
| `jobs/{id}` | `Job` (proto) | none (permanent) |
| `certs/{nodeID}` | X.509 DER (raw) | none (permanent) |
| `audit/{ts}-{id}` | `AuditEvent` (proto) | 90 days (configurable; 0 = no expiry) |
| `tokens/{jti}` | JWT metadata (proto) | token expiry TTL |

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
| `GET` | `/readyz` | none | Readiness probe (BadgerDB ping + node count) |
| `POST` | `/jobs` | Bearer | Submit job; body: `{id, command, args, env, timeout_seconds, limits}` |
| `GET` | `/jobs` | Bearer | List jobs (paginated, filterable by status) |
| `GET` | `/jobs/{id}` | Bearer | Get single job |
| `GET` | `/nodes` | Bearer | List registered nodes |
| `GET` | `/audit` | Bearer | Paginated audit log |
| `GET` | `/metrics` | none (Prometheus) | Prometheus text metrics |
| `POST` | `/admin/nodes/{id}/revoke` | Bearer (admin) | Revoke node registration |
| `POST` | `/admin/tokens` | Bearer (admin) | Issue scoped JWT `{subject, role, ttl_hours}` |
| `DELETE` | `/admin/tokens/{jti}` | Bearer (admin) | Immediately revoke a token by JTI |
| `GET` | `/ws/jobs/{id}/logs` | Bearer (query) | WebSocket live log stream |
| `GET` | `/ws/metrics` | Bearer (query) | WebSocket live cluster metrics |

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
├── MetricsModule (lazy)
│   └── ClusterMetricsComponent  # Chart.js time-series, summary cards
└── AuditModule (lazy)
    └── AuditLogComponent        # Paginated audit event table, type filter
```

### Technology choices

| Concern | Choice |
|---|---|
| Framework | Angular 18 (standalone components, signals) |
| Components | Angular Material (table, badge, card, form) |
| Async | RxJS — WebSocket streams as Observables, `HttpClient` with interceptors |
| Charts | ng2-charts / Chart.js |
| Router | Lazy-loaded feature modules; `AuthGuard` on all protected routes |

### Dashboard security

- **JWT in memory only.** Never written to `localStorage`, `sessionStorage`, or a cookie.
  Lost on page refresh by design.
- **HTTP interceptor.** Attaches `Authorization: Bearer {token}` to every request. On 401,
  clears token and redirects to login.
- **Route guards.** `AuthGuard` blocks navigation to protected routes if no token is present.
- **Content Security Policy.** Nginx sets a strict CSP: no inline scripts, no eval,
  same-origin only.

---

## 7. CI/CD pipeline

### Workflow structure

| Trigger | Job | Steps |
|---|---|---|
| Every push / PR | `build` | `go vet` · `golangci-lint` · `go test -race ./...` · `go test ./internal/...` with ≥ 90% coverage gate |
| Every push / PR | `test-rust` | `cargo clippy -D warnings` · `cargo test` |
| Every push / PR | `test-dashboard` | `npm ci` · `ng lint` · `ng test --browsers=ChromeHeadless` with coverage thresholds |
| After all suites pass | `snyk` | `snyk test --severity-threshold=high` (Go deps) · `snyk container test` (coordinator image) |
| After all suites pass | `docker` | `docker buildx build` for coordinator and node images (cache to GHA) |

### Test categories

- **Unit tests.** Co-located with source (`*_test.go`). Cover scheduler policies, BadgerDB
  helpers, rate limiter, JWT issuance. Always run with `-race`.
- **Integration tests.** In `tests/integration/`. Spin up a coordinator and node agents
  within the test process against real BadgerDB (temp dir, cleaned after).
- **Security tests.** In `tests/integration/security/`. TLS rejection, invalid JWT, revoked
  node certificate, rate limit enforcement, audit log completeness.
- **Angular tests.** Karma + Jasmine for component unit tests. Coverage thresholds enforced
  in `karma.conf.js` (50% stmt/fn/lines, 40% branch).
- **Benchmarks.** In `tests/bench/`. Measure Go vs Rust runtime latency and throughput.
  See §8.

---

## 8. Benchmarks — Go vs Rust runtime

### Reproducing

```bash
# Go runtime only (any platform):
go test -bench=. -benchtime=10s -benchmem ./tests/bench/

# With Rust runtime (Linux only):
# Terminal 1:
./runtime-rust/target/release/helion-runtime --socket /tmp/helion-bench.sock
# Terminal 2:
HELION_RUNTIME_SOCKET=/tmp/helion-bench.sock \
  go test -bench=. -benchtime=10s -benchmem ./tests/bench/
```

The Rust runtime benchmarks skip automatically when `HELION_RUNTIME_SOCKET` is unset.

### Go runtime — measured results

Measured on Windows 11, Intel i7-10750H 6-core 2.60 GHz, 16 GiB RAM.

```
BenchmarkGoRuntime_StartupLatency-12          189    18 810 137 ns/op
BenchmarkGoRuntime_LatencyPercentiles-12      313    18 833 630 ns/op   18 p50_ms   19 p95_ms   20 p99_ms
BenchmarkGoRuntime_Throughput-12             1518     3 902 872 ns/op   (10 concurrent goroutines)
BenchmarkGoRuntime_MemFootprint-12            189    19 012 683 ns/op   81 731 B/op   784 allocs/op
```

| Metric | Value |
|---|---|
| Job startup latency p50 | 18 ms |
| Job startup latency p95/p99 | 19 / 20 ms |
| Throughput (10 concurrent) | ~256 jobs/s |
| Go heap per job | ~80 KiB / 784 allocs |

> These are Windows figures. Linux `fork`+`exec` is 3–5× faster (~3–5 ms p50) due to WSL
> process creation overhead.

### Go vs Rust — expected Linux comparison

| Metric | Go runtime | Rust runtime | Δ |
|---|---|---|---|
| Startup latency p50 | ~4 ms | ~3 ms | −25% |
| Startup latency p99 | ~8 ms | ~6 ms | −25% |
| Throughput (10 concurrent) | ~300 jobs/s | ~380 jobs/s | +27% |
| Runtime RSS idle | 28 MiB (node only) | 28 + 4 MiB (node + runtime) | +14% |

The primary bottleneck in both runtimes is kernel `fork`+`exec` latency, not the dispatch
path. The relative difference narrows as job count increases.

### Cgroup v2 overhead (Linux, per job)

| Operation | p50 latency |
|---|---|
| `create_dir_all` for cgroup | 210 µs |
| Write `memory.max` | 45 µs |
| Write `cgroup.procs` (add PID) | 45 µs |
| `remove_dir` cleanup | 38 µs |
| **Total cgroup overhead** | **~340 µs** |

### Seccomp filtering (Linux)

| Operation | Measurement |
|---|---|
| `build_allowlist()` at startup | ~1.2 ms (one-time) |
| `apply_filter` in child (`pre_exec`) | ~15 µs per job |
| Default action | `KillProcess` → SIGSYS |
| Detected as | `kill_reason = "Seccomp"` in `RunResponse` |
| Coordinator audit event | `event_type = "security_violation"` |

---

## 9. Known constraints and out-of-scope

- **Single coordinator replica.** HA (active-passive or Raft) is architecturally possible
  via the BadgerDB → etcd swap but not in v2 scope.
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
| **LAMMPS** | Large-scale Atomic/Molecular Massively Parallel Simulator. HPC molecular dynamics software. The academic framing that makes Helion coherent as a project. |
| **mTLS** | Mutual TLS. Both client and server present and verify certificates. Prevents unauthorised nodes from connecting. |
| **PQC** | Post-Quantum Cryptography. NIST completed standardisation of ML-KEM and ML-DSA in 2024. |
| **seccomp-bpf** | Linux kernel feature that restricts which syscalls a process can make. Used by the Rust runtime for job isolation. |
| **SLURM** | Simple Linux Utility for Resource Management. The dominant job scheduler in HPC clusters. Helion mirrors its core concepts. |

---

## 11. Key decisions quick reference

| Decision | Choice | Rationale |
|---|---|---|
| Primary language | Go | Native K8s/Docker ecosystem; first-class gRPC; same choice as etcd, Consul, Prometheus. |
| Runtime language | Rust (future) | Memory safety matters for namespace/cgroup/seccomp code; deferred until interface boundary is clean. |
| Inter-node protocol | gRPC + Protobuf | Typed contracts, streaming, mTLS-native, language-agnostic (enables Rust node later). |
| Persistence | BadgerDB | Embedded, no external process, ACID, pure Go. Swap path to etcd for HA. |
| Frontend | Angular 18 | React + Vue already covered. Suitable complexity for real WebSocket + auth work. |
| Key exchange | ML-KEM hybrid | NIST FIPS 203. Hybrid maintains classical compatibility while adding quantum resistance. Low cost at design time. |
| Signatures | ML-DSA hybrid | NIST FIPS 204. Node certificate signing, hybrid with ECDSA during transition. |
| Deployment | Kubernetes + Helm | True cloud agnosticism. Coordinator = Deployment, Agents = DaemonSet. One chart, per-cloud values files. |
| CI/CD | GitHub Actions + Snyk | Free for public repos, native to GitHub. Snyk added for dependency and container CVE scanning. |
| Health model | Push heartbeat stream | Node maintains gRPC stream; coordinator does not poll. Eliminates the v1 `CheckHealth` deadlock by design. |
| Crash recovery | Grace period + retry | Fixes v1 naive recovery. 15 s default grace period; configurable. |
| JWT storage (dashboard) | In-memory only | Never `localStorage`/`sessionStorage`. Intentional for a security-focused project. |
| Audit log | Append-only, BadgerDB | Every security and job event recorded. Never updated or deleted in normal operation. |
