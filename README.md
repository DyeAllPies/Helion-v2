# Helion v2

[![CI](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml)

A from-scratch distributed job scheduler written in Go — built as a vehicle for studying systems programming, distributed systems theory, and production security practices.

v1 (4th semester) demonstrated the core lifecycle: job submission, node registration, heartbeat health checking, crash recovery, and Docker Compose packaging. v2 is a clean-room redesign with a production-grade stack: gRPC, BadgerDB, mTLS, post-quantum cryptography, an Angular dashboard, and Kubernetes deployment.

> Helion mirrors the concerns of production HPC schedulers like SLURM — job queuing, node health, resource accounting, and crash recovery — making it academically coherent alongside LAMMPS-based simulation work, not merely a portfolio piece.

---

## Architecture

```
┌─────────────────────────────────────────┐
│           Angular Dashboard             │
│     REST + WebSocket  ·  JWT auth       │
└──────────────────┬──────────────────────┘
                   │ HTTPS
┌──────────────────▼──────────────────────┐
│             Coordinator (Go)            │
│  gRPC Server · Scheduler · BadgerDB     │
│  REST/WS API · Audit Log · Internal CA  │
└──────────────────┬──────────────────────┘
                   │ gRPC + mTLS + PQC
          ┌────────┴────────┐
┌─────────▼──────┐  ┌───────▼────────┐
│  Node Agent    │  │  Node Agent    │
│  Job Executor  │  │  Job Executor  │
│  Log Streamer  │  │  Log Streamer  │
└────────────────┘  └────────────────┘
        │                   │
  Runtime interface   Runtime interface
  (Go subprocess + Rust/cgroup/seccomp)
```

The coordinator runs as a Kubernetes `Deployment`. Node agents run as a `DaemonSet` — one per cluster node. The Angular dashboard is served from its own container behind an Nginx reverse proxy.

---

## Stack

| Concern | Choice | Why |
|---|---|---|
| Primary language | Go 1.26 | Native to the K8s/Docker ecosystem; same as etcd, Consul, Prometheus |
| Runtime (Phase 6) | Rust | Memory safety without GC for namespace/cgroup/seccomp code |
| Inter-node protocol | gRPC + Protocol Buffers | Typed contracts, bidirectional streaming, mTLS-native |
| Persistence | BadgerDB | Embedded, ACID, pure Go; swap path to etcd if HA is needed |
| Dashboard | Angular 18 | Covers the enterprise framework gap; real WebSocket + auth complexity |
| Key exchange | ML-KEM / Kyber (NIST FIPS 203) | Hybrid PQC — quantum-resistant from day one |
| Signatures | ML-DSA / Dilithium (NIST FIPS 204) | Node certificate signing, hybrid with ECDSA |
| Deployment | Kubernetes + Helm | Cloud-agnostic; one chart, per-cloud values files |
| CI/CD | GitHub Actions + Snyk | Build, lint, test, coverage gate (≥ 90%), dependency CVE scanning |

---

## Security model

All coordinator↔node communication is mutually authenticated via mTLS from the first commit. The coordinator acts as its own internal CA and issues per-node X.509 certificates on first registration.

Key exchange uses a hybrid classical + post-quantum mode (X25519 + ML-KEM/Kyber-768) so sessions are resistant to harvest-now-decrypt-later attacks. Node certificates are signed with ML-DSA (Dilithium) in hybrid mode alongside ECDSA.

The Angular dashboard authenticates with short-lived JWTs (15-minute expiry). Tokens are stored in memory only — never in `localStorage` or `sessionStorage`. Every job state transition, node registration, and auth failure is written to an append-only audit log in BadgerDB.

Snyk scans Go dependencies and the coordinator container image on every push, blocking on high-severity CVEs.

---

## Build phases

| Phase | Scope | Status |
|---|---|---|
| 1 — Foundation | Repo scaffold, protobuf toolchain, mTLS skeleton, CI, Docker Compose | Complete |
| 2 — Core scheduler | BadgerDB persistence, node registry, scheduler engine, job lifecycle, crash recovery | Complete |
| 3 — Angular dashboard | REST/WebSocket API, auth module, nodes/jobs/metrics/audit pages | Complete |
| 4 — Security hardening | Hybrid PQC key exchange, ML-DSA certificates, JWT revocation, rate limiting | Complete |
| 5 — Kubernetes & cloud | Helm chart, health probes, Prometheus metrics, CD pipeline | Complete |
| 6 — Rust runtime | Runtime interface swap, cgroup v2 limits, seccomp filtering | Complete |

Each phase produces a working, tested, runnable artifact. No phase is purely theoretical.

---

## Repository layout

```
helion-v2/
├── cmd/
│   ├── helion-coordinator/   # Coordinator binary
│   ├── helion-node/          # Node agent binary
│   └── helion-run/           # CLI client (submits jobs)
├── internal/
│   ├── api/                  # REST + WebSocket handlers
│   ├── audit/                # Append-only audit logger
│   ├── auth/                 # JWT issuance, CA, certificate management
│   ├── cluster/              # Scheduler, crash recovery, job lifecycle
│   ├── grpcserver/           # gRPC server (coordinator side)
│   ├── grpcclient/           # gRPC client (node agent side)
│   ├── metrics/              # Prometheus metrics provider
│   ├── nodeserver/           # Node agent gRPC server
│   ├── persistence/          # BadgerDB wrapper + key definitions
│   ├── ratelimit/            # Per-node token-bucket rate limiter
│   ├── runtime/              # Runtime interface, Go impl, Rust client
│   └── pqcrypto/             # PQC key generation + hybrid TLS helpers
├── proto/                    # .proto definitions + generated Go stubs
├── dashboard/                # Angular 18 project
├── deploy/
│   ├── helm/                 # Helm chart
│   ├── k8s/                  # Raw Kubernetes manifests
│   └── docker-compose.yml    # Local dev only
├── runtime-rust/             # Rust runtime binary (cgroup v2 + seccomp)
├── tests/
│   ├── bench/                # Runtime benchmarks (Go vs Rust)
│   └── integration/
│       └── security/         # mTLS, JWT, rate-limit integration tests
├── docs/                     # ARCHITECTURE.md · SECURITY.md · benchmarks
└── .github/workflows/        # GitHub Actions CI/CD
```

---

## Local development

### Prerequisites

- Go 1.26+
- Docker + Docker Compose
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (for protobuf codegen)
- Node.js 20+ (for the Angular dashboard)
- Rust toolchain (optional — only for `runtime-rust/` benchmarks)

### Run locally with Docker Compose

```bash
docker compose up --build
```

This starts one coordinator and two node agents on a shared bridge network. Ports are bound to `127.0.0.1` only.

### Build all binaries

```bash
go build ./...
```

### Regenerate protobuf stubs

```bash
make proto
```

### Run tests

```bash
# All packages — integration tests included
go test -race -count=1 ./...

# Internal packages with coverage report
go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./internal/...
go tool cover -func=coverage.out | grep total:
```

CI enforces a ≥ 90% coverage threshold on `./internal/...`.

---

## Environment variables

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_COORDINATOR` | Node agent, CLI | `localhost:9090` | Coordinator gRPC address |
| `HELION_ALLOW_ISOLATION` | Node agent | `false` | Enable Linux namespace isolation (requires root or `CAP_SYS_ADMIN`) |
| `HELION_SCHEDULER` | Coordinator | `roundrobin` | Scheduling policy: `roundrobin` or `least` |
| `HELION_RATE_LIMIT_RPS` | Coordinator | `10` | Per-node job submission rate limit (jobs/second) |
| `HELION_RUNTIME_SOCKET` | Node agent | _(unset)_ | Unix socket path for Rust runtime; falls back to Go runtime if unset |
| `PORT` | Node agent | `8080` | Node agent listen port |

---

## Kubernetes deployment

```bash
helm install helion ./deploy/helm
```

For cloud-specific values:

```bash
helm install helion ./deploy/helm -f deploy/helm/values-eks.yaml
helm install helion ./deploy/helm -f deploy/helm/values-gke.yaml
```

The coordinator runs as a single-replica `Deployment`. Node agents run as a `DaemonSet`. HA (multi-coordinator) is a v3 concern.

---

## Comparison with SLURM

| Concept | SLURM | Helion v2 |
|---|---|---|
| Job submission | `sbatch` script | `helion-run` CLI / REST API |
| Node management | `slurmctld` + `slurmd` | Coordinator + Node Agents |
| Scheduling policy | Priority queues, backfill, fairshare | Round-robin, least-loaded (extensible) |
| State persistence | StateSave / MySQL / MariaDB | BadgerDB (embedded) |
| Health monitoring | `slurmctld` polls `slurmd` | gRPC heartbeat stream + prune |
| Isolation | cgroups, PAM, users | Linux namespaces + cgroup v2 + seccomp (Phase 6) |
| Security | Munge authentication | mTLS + PQC + JWT |
| Observability | `sacct`, `sinfo`, `sreport` | Angular dashboard + Prometheus metrics |
| Cloud deployment | Bare metal / on-premise | Kubernetes / Helm (cloud-agnostic) |

---

## Known constraints

- Single coordinator replica in v2. HA requires swapping BadgerDB for etcd — the interface is already designed for it.
- Node agents require Linux for namespace isolation. Cross-compiled binaries run on other OS targets without isolation, suitable for local development.
- Namespace isolation requires root or `CAP_SYS_ADMIN`. In Kubernetes this is set in the DaemonSet `SecurityContext`.
- No multi-tenancy, no GPU scheduling, no MapReduce demo in v2 scope.

---

## v1 → v2 changelog

The following bugs from v1 are fixed by design in v2, not patched:

| Bug | v1 | v2 fix |
|---|---|---|
| `CheckHealth()` deadlock | Held write lock during blocking HTTP calls to every node | Replaced with push heartbeat stream — coordinator never polls |
| `lastIndex` race | Written under `RLock` | Updated via `atomic.Int64` |
| Double-close on heartbeat channel | `Stop()` called twice | Single ownership, `sync.Once` guard |
| State round-trip of `*PersistentState` | Serialised pointer to its own path, silent nil on reload | State excluded from JSON; BadgerDB handles persistence |
| Crash recovery timing | `recoverLostJobs()` fired before nodes re-registered | 15 s startup grace period before dispatch is attempted |

---

*Dennis Alves Pedersen · April 2026*

**Further reading:** [Architecture reference](docs/ARCHITECTURE.md) · [Security reference](docs/SECURITY.md)
