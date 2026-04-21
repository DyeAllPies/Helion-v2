> **Audience:** engineers
> **Scope:** Index for the engineer-facing technical reference — how Helion is built.
> **Depth:** reference

# Architecture

Engineer-facing reference for extending Helion. For the project landing page
see [`../README.md`](../README.md); for the contributor process (templates,
audits, feature specs) see [`../DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md).

## Files in this folder

| File | Contents |
|---|---|
| [components.md](components.md) | Coordinator, node agent, and runtime interface internals. *Populated in feature 44 commit 5 — see [../COMPONENTS.md](../COMPONENTS.md) until then.* |
| [protocols.md](protocols.md) | gRPC + REST + WebSocket + event-bus contracts. *Populated in feature 44 commit 5 — see [../ARCHITECTURE.md § 4](../ARCHITECTURE.md#4-protocol-contracts) until then.* |
| [persistence.md](persistence.md) | `internal/persistence` rules, key schema, TTL conventions. |
| [runtime-rust.md](runtime-rust.md) | Rust runtime: cgroup v2, seccomp, IPC protocol. |
| [dashboard.md](dashboard.md) | Angular 21 dashboard — stack, testing, local dev. |
| [performance.md](performance.md) | Go vs Rust runtime benchmarks; cgroup + seccomp overhead measurements. |

## Where things still live

The following engineer-audience reference still lives at the top level during
the feature 44 restructure, moving under `architecture/` in commit 5:

- [`../ARCHITECTURE.md`](../ARCHITECTURE.md) — tech-decision summary, protocol contracts, pipeline sketch.
- [`../COMPONENTS.md`](../COMPONENTS.md) — coordinator / node / runtime internals.

## Repository layout

```
helion-v2/
├── cmd/
│   ├── helion-coordinator/     # Coordinator binary
│   ├── helion-node/            # Node agent binary
│   ├── helion-run/             # CLI client (submits jobs)
│   └── helion-issue-op-cert/   # Operator-cert issuance CLI
├── internal/
│   ├── analytics/              # PostgreSQL sink + /api/analytics/* handlers
│   ├── api/                    # REST + WebSocket handlers
│   ├── artifacts/              # S3 + local artifact store
│   ├── audit/                  # Append-only audit logger
│   ├── auth/                   # JWT issuance, CA, certificate management
│   ├── authz/                  # Policy engine (features 35-38)
│   ├── cluster/                # Scheduler, crash recovery, job lifecycle
│   ├── grpcclient/             # gRPC client (node agent side)
│   ├── grpcserver/             # gRPC server (coordinator side)
│   ├── groups/                 # IAM groups store (feature 38)
│   ├── logstore/               # Badger + PG-authoritative log store
│   ├── metrics/                # Prometheus metrics provider
│   ├── nodeserver/             # Node agent gRPC server
│   ├── persistence/            # BadgerDB wrapper + key definitions
│   ├── pqcrypto/               # PQC key generation + hybrid TLS helpers
│   ├── principal/              # Principal identity model (feature 35)
│   ├── ratelimit/              # Per-node token-bucket rate limiter
│   ├── registry/               # Dataset / model registry
│   ├── runtime/                # Runtime interface, Go impl, Rust client
│   ├── secretstore/            # Envelope encryption for secrets at rest
│   └── webauthn/               # WebAuthn / FIDO2 (feature 34)
├── runtime-rust/               # Rust runtime binary (cgroup v2 + seccomp)
├── proto/                      # .proto definitions + generated Go stubs
├── dashboard/                  # Angular 21 project
│   └── e2e/                    # Playwright E2E tests
├── deploy/
│   ├── helm/                   # Helm chart (planning-only scaffold)
│   └── k8s/                    # Raw Kubernetes manifests
├── examples/
│   ├── ml-iris/                # Reference ML pipeline (iris)
│   └── ml-mnist/               # Parallel-heterogeneous MNIST demo
├── tests/
│   ├── bench/                  # Runtime benchmarks (Go vs Rust)
│   ├── gpu/                    # GPU-path tests (build-tag gated; no CI runners)
│   └── integration/
│       └── security/           # mTLS, JWT, rate-limit integration tests
├── scripts/
│   ├── run-e2e.sh              # One-command full-stack E2E runner
│   ├── run-iris-e2e.sh         # ML iris-pipeline E2E
│   └── docs-lint.sh            # docs frontmatter + line-budget gate (feature 44)
├── docs/                       # All project documentation
├── docker-compose.yml          # Local dev compose root
├── docker-compose.e2e.yml      # E2E overlay
└── .github/workflows/          # GitHub Actions CI/CD
```

## Key decisions at a glance

| Choice | Rationale |
|---|---|
| Go 1.26 | Same language as Kubernetes, Docker, etcd, Consul, Prometheus; goroutines + single-binary output suit network infrastructure. |
| Rust for the hardening runtime | Memory safety without a GC matters specifically for code that manipulates namespaces / cgroups / seccomp — the same code `runc` and `youki` are written in. |
| gRPC + Protobuf for coordinator↔node | Typed contracts enforced at compile time, bidirectional streaming, native mTLS, language-agnostic (a Rust node agent is a drop-in replacement). |
| REST + JSON for the public API | Browser compatibility, dashboard simplicity, and the existing ecosystem of curl-based tooling. |
| BadgerDB for persistence | Embedded, ACID, pure Go, no external process. Swap path to etcd is a one-file change — business logic accesses storage through a typed interface. |
| Angular 21 for the dashboard | Fills the enterprise-framework gap; the dashboard consumes real WebSocket streams, renders live metrics, and handles JWT auth with automatic session management — it is not a UI exercise. |
| Hybrid PQC from day one | "Better safe than sorry" on a non-production codebase means the right patterns exist if the project ever is taken to production. Secondary: harvest-now-decrypt-later resistance for deployed systems. |
