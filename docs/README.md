# Helion v2

[![CI](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml)

A minimal distributed job scheduler written in Go — built as a student learning project for systems programming, distributed systems theory, container orchestration, and production-grade security practices. **Not intended for production deployment**; the posture throughout is "better safe than sorry" so the project exercises the right patterns for real systems, even on code nobody will actually run in the wild.

The stack covers the full orchestrator story end-to-end: workflow/DAG support, retry policies, resource-aware scheduling, job state machine, priority queues, event bus + WebSocket push, observability (job logs, metrics, subsystem health), an Angular dashboard, an analytics pipeline over Postgres, an ML pipeline (artifact store, dataset/model registry, inference services, parallel-heterogeneous training demo), and a hybrid post-quantum security layer on top of mTLS.

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

The deployment target was Kubernetes — the coordinator as a `Deployment`, node agents as a `DaemonSet`, dashboard as its own container behind an Nginx reverse proxy. **Helm manifests live under [`deploy/helm/`](../deploy/helm/) but have never been exercised against a real cluster** — the project only runs under Docker Compose and GitHub Actions CI. The chart ships as design scaffolding, not a deployment artefact.

---

## Stack

| Concern | Choice | Why |
|---|---|---|
| Primary language | Go 1.26 | Native to the K8s/Docker ecosystem; same as etcd, Consul, Prometheus |
| Runtime | Rust | Memory safety without GC for namespace/cgroup/seccomp code |
| Inter-node protocol | gRPC + Protocol Buffers | Typed contracts, bidirectional streaming, mTLS-native |
| Persistence | BadgerDB | Embedded, ACID, pure Go; swap path to etcd if HA is needed |
| Dashboard | Angular 21 | Covers the enterprise framework gap; real WebSocket + auth complexity |
| Key exchange | ML-KEM / Kyber (NIST FIPS 203) | Hybrid PQC — quantum-resistant from day one |
| Signatures | ML-DSA / Dilithium (NIST FIPS 204) | Node certificate signing, hybrid with ECDSA |
| Deployment (planned, untested) | Kubernetes + Helm | Planning-only scaffold — chart exists under `deploy/helm/` but the project has only ever run via Docker Compose |
| CI/CD | GitHub Actions + Snyk | Build, lint, test (with `-race`), coverage gate (internal/ ≥ 85%, cmd/ ≥ 25%, dashboard 85/60/85/85), dependency CVE scanning |

---

## Security model

All coordinator↔node communication is mutually authenticated via mTLS. The coordinator acts as its own internal CA and issues per-node X.509 certificates on first registration. The signed certificate is returned to the node in the `RegisterResponse` so the node presents a coordinator-verified cert on its own gRPC server — the coordinator validates the cert chain during job dispatch.

Key exchange uses a hybrid classical + post-quantum mode (X25519 + ML-KEM/Kyber-768). The primary motivation is simply "better safe than sorry" — Helion is a student learning project, not a production target, but wiring the right patterns on a non-production codebase is cheap and demonstrates the posture real systems should take. A secondary motivation is harvest-now-decrypt-later resistance, which matters most once a system is actually deployed; Helion is not, so HNDL is a longevity concern rather than a live threat here. Node certificates are signed with ML-DSA (Dilithium) in hybrid mode alongside ECDSA. The coordinator verifies the ML-DSA out-of-band signature on every registration, and pins the cert fingerprint (SHA-256) so a newly-issued cert for the same node ID is rejected unless the node goes through the full revoke → re-register cycle.

The REST API uses short-lived JWTs (15-minute expiry). WebSocket endpoints authenticate via a first-message pattern — the token is sent as the first frame after the handshake, never as a URL query parameter, keeping tokens out of server logs and browser history. The root token is rotated on every coordinator restart — the previous token is immediately revoked so a leaked token from a prior run is dead on restart. Scoped tokens for individual users or services can be issued and revoked via `POST /admin/tokens` and `DELETE /admin/tokens/{jti}` (admin role required). Tokens are stored in memory only — never in `localStorage` or `sessionStorage`. Every job state transition, node registration, auth failure, and token event is written to an append-only audit log in BadgerDB.

Snyk scans Go dependencies and the coordinator container image on every push, blocking on high-severity CVEs. Coverage gates: internal/ ≥ 85%, cmd/ ≥ 25%, dashboard 85% statements / 60% branches / 85% functions / 85% lines.

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
├── dashboard/                # Angular 21 project
│   └── e2e/                  # Playwright E2E tests (78 tests)
├── deploy/
│   ├── helm/                 # Helm chart
│   ├── k8s/                  # Raw Kubernetes manifests
│   └── docker-compose.yml    # Local dev only
├── runtime-rust/             # Rust runtime binary (cgroup v2 + seccomp)
├── tests/
│   ├── bench/                # Runtime benchmarks (Go vs Rust)
│   └── integration/
│       └── security/         # mTLS, JWT, rate-limit integration tests
├── scripts/
│   └── run-e2e.sh            # One-command full-stack E2E test runner
├── docs/                     # All project documentation — see Documentation index below
├── docker-compose.e2e.yml    # E2E overlay (exposes coordinator HTTP + stable token)
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

# Dashboard unit tests
cd dashboard && ng test --watch=false --browsers=ChromeHeadless

# Full-stack E2E tests (boots cluster, runs the Playwright suite, tears down)
make test-e2e

# E2E with visible browser
make test-e2e-headed

# All test suites in one command (Go + Rust + Angular + E2E)
make test-all

# Pre-push validation (Go lint + test + -race + coverage, Angular lint +
# test + coverage, repo hygiene). No Docker cluster, no E2E.
make check

# Pre-push validation including Playwright E2E. Use this when the
# change touches infra (docker-compose*, Dockerfile*, .github/workflows,
# scripts/run-e2e.sh, or cluster startup wiring).
make check-full
```

CI enforces coverage thresholds: internal/ ≥ 85%, cmd/ ≥ 25%, dashboard 85/60/85/85 (statements/branches/functions/lines). Failing either tier fails the build.

---

## Environment variables

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_COORDINATOR` | Node agent, CLI | `localhost:9090` | Coordinator gRPC address |
| `HELION_ALLOW_ISOLATION` | Node agent | `false` | Enable Linux namespace isolation (requires root or `CAP_SYS_ADMIN`) |
| `HELION_ROTATE_TOKEN` | Coordinator | `true` | Rotate root token on each startup; `false` reuses the stored token |
| `HELION_TOKEN_FILE` | Coordinator | `/var/lib/helion/root-token` | Path the rotated root token is written to (mode `0600`) |
| `HELION_NODE_PINS` | Coordinator | _(unset)_ | Pre-configured cert pins `nodeID:sha256hex,…`; unlisted nodes use first-seen |
| `HELION_DEFAULT_TIMEOUT_SEC` | Runtime | `300` | Fallback job timeout when `TimeoutSeconds` is unset or ≤ 0 |
| `HELION_ALLOWED_COMMANDS` | Runtime | _(unset)_ | Comma-separated command allowlist; unset = allow-all (dev mode) |
| `HELION_CA_CERT_TTL_DAYS` | Coordinator | `730` | Internal CA certificate lifetime |
| `HELION_NODE_CERT_TTL_HOURS` | Coordinator | `24` | Node certificate lifetime (renewed on each Register) |
| `HELION_RATE_LIMIT_RPS` | Coordinator | `10` | Per-node job submission rate limit (jobs/second) |
| `HELION_RUNTIME` | Node agent | `go` | Runtime backend: `go` (subprocess) or `rust` (cgroup v2 + seccomp) |
| `HELION_RUNTIME_SOCKET` | Node agent | _(unset)_ | Unix socket path for Rust runtime |
| `HELION_TOKEN` | CLI (`helion-run`) | _(unset)_ | Bearer token attached to all API requests |
| `HELION_JOB_ID` | CLI (`helion-run`) | _(unset)_ | Stable job ID for idempotent retries |
| `PORT` | Node agent | `8080` | Node agent listen port |

---

## Kubernetes deployment (planning only — never exercised)

The Helm chart under [`deploy/helm/`](../deploy/helm/) is **design scaffolding**. It has never been installed against a real Kubernetes cluster; every test path in this project runs under Docker Compose locally and GitHub Actions CI. The commands below are what a deployment *would* look like if the chart were ever taken through a real apply-and-verify cycle:

```bash
# Hypothetical — not part of any CI or release path:
helm install helion ./deploy/helm
helm install helion ./deploy/helm -f deploy/helm/values-eks.yaml
helm install helion ./deploy/helm -f deploy/helm/values-gke.yaml
```

The coordinator is shaped as a single-replica `Deployment`, node agents as a `DaemonSet`. HA (multi-coordinator) is out of scope. Treat the chart as a "what production would look like" reference, not a supported install path.

---

## Known constraints

- **Not production-deployed.** The project has never been installed against a real Kubernetes cluster; every validation path ends at Docker Compose + CI. Treat the security posture + Helm scaffolding as "what production would look like" rather than "what production does look like."
- **GPU scheduling is code-complete but unverified on real hardware.** Feature 15 ships the scheduler's GPU allocator, `CUDA_VISIBLE_DEVICES` pinning, and a build-tag-gated test harness (`tests/gpu/`) that exercises `nvidia-smi` when present. GitHub Actions' free tier has no GPU runners, so no run of CI has ever touched a real GPU — everything is asserted on the CPU-path stub or a simulated nvidia-smi. See [`planned-features/implemented/15-ml-gpu-first-class-resource.md`](planned-features/implemented/15-ml-gpu-first-class-resource.md) for the full status.
- **Single coordinator replica.** HA requires swapping BadgerDB for etcd — the interface is already designed for it.
- **Linux-only isolation.** Node agents require Linux for namespace isolation. Cross-compiled binaries run on other OS targets without isolation, suitable for local development.
- **Namespace isolation requires root or `CAP_SYS_ADMIN`.** On a hypothetical Kubernetes deployment this would be set in the DaemonSet `SecurityContext`.
- **Resource limits enforced only by Rust runtime.** `memory_bytes` and `cpu_quota_us` are honoured by the Rust runtime (`HELION_RUNTIME=rust`). The Go runtime ignores them.

---

## Documentation index

All project documentation lives under `docs/`:

| File | Contents |
|---|---|
| [README.md](README.md) | This file — project overview, stack, environment variables, quick start |
| [ARCHITECTURE.md](ARCHITECTURE.md) | High-level design, technology decisions, protocol contracts, CI/CD |
| [COMPONENTS.md](COMPONENTS.md) | Coordinator, node agent, and runtime interface internals |
| [PERFORMANCE.md](PERFORMANCE.md) | Go vs Rust benchmarks, cgroup/seccomp overhead |
| [SECURITY.md](SECURITY.md) | Threat model, mTLS, PQC, rate limiting, audit logging |
| [JWT-GUIDE.md](JWT-GUIDE.md) | JWT token lifecycle, issuance, usage, revocation |
| [SECURITY-OPS.md](SECURITY-OPS.md) | Operational checklist, env vars, troubleshooting |
| [DOCS-WORKFLOW.md](DOCS-WORKFLOW.md) | How `audits/`, `planned-features/`, and `planned-features/deferred/` work together — templates, naming, cross-references |
| [audits/](audits/) | Closed audits (one file per run, `YYYY-MM-DD-NN.md`). Template at [`audits/TEMPLATE.md`](audits/TEMPLATE.md). |
| [dashboard.md](dashboard.md) | Angular dashboard — stack, testing, local dev |
| [persistence.md](persistence.md) | `internal/persistence` rules, key schema, test invariants |
| [docker-compose-dev-notes.md](docker-compose-dev-notes.md) | Local Docker Compose workflow notes |
| [planned-features/](planned-features/) | Active feature specs. Deferred items live under [`planned-features/deferred/`](planned-features/deferred/). Templates: [`planned-features/TEMPLATE.md`](planned-features/TEMPLATE.md), [`planned-features/deferred/TEMPLATE.md`](planned-features/deferred/TEMPLATE.md). |

### Packing the repo without audits and planned features

`docs/audits/` and `docs/planned-features/` grow over time. To bundle the
source tree for download, an AI assistant, or offline review **without**
those directories:

```bash
# With repomix:
npx repomix --ignore "docs/audits/**,docs/planned-features/**"

# Or with git:
git archive --format=tar.gz -o helion-v2.tar.gz HEAD \
  -- ':(exclude)docs/audits' ':(exclude)docs/planned-features'
```

This keeps [`docs/DOCS-WORKFLOW.md`](DOCS-WORKFLOW.md) and the three
`TEMPLATE.md` files (so a fresh reader still sees how the process
works) and drops only the dated audit files and feature spec archive.

---

*Dennis Alves Pedersen · April 2026*

**Further reading:** [Architecture reference](ARCHITECTURE.md) · [Security reference](SECURITY.md)
