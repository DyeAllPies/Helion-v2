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

## CI/CD pipeline

| Trigger | Job | Steps |
|---|---|---|
| Every push / PR | `build` | `go vet` · `golangci-lint` · `go test -race -count=1 ./...` · coverage gates (internal/ ≥ 85%, cmd/ ≥ 25%) |
| Every push / PR | `test-rust` | `cargo clippy -D warnings` · `cargo llvm-cov` with ≥ 85% coverage gate |
| Every push / PR | `test-dashboard` | `npm ci` · `ng lint` · `ng test --browsers=ChromeHeadless` with coverage thresholds (85 / 60 / 85 / 85) |
| Every push / PR | `docs-lint` | Frontmatter + per-folder line-budget gate (feature 44) |
| After unit suites pass | `e2e` | Build Docker images · boot cluster · wait for healthy nodes · run Playwright non-ML specs · tear down |
| After tier 1 | `e2e-iris` → `e2e-mnist` | ML-pipeline Playwright walkthroughs chained sequentially |
| After all E2E | `snyk`, `docker` | CVE scans (Go deps + coordinator image) + `docker buildx` caching to GHA |

Test categories:

- **Unit tests.** Co-located with source (`*_test.go`). Always run with `-race`.
- **Integration tests.** `tests/integration/` — real BadgerDB in a temp dir.
- **Security tests.** `tests/integration/security/` — TLS rejection, revoked nodes, rate limits, audit completeness.
- **Angular unit tests.** Karma + Jasmine. Coverage thresholds enforced by `scripts/check-dashboard-coverage.sh` because the Karma builder ignores `karma.conf.js`'s `check:` block.
- **E2E tests.** `dashboard/e2e/` — Playwright specs covering login → jobs → workflows → analytics → ML pipelines. Against a real cluster, no mocks.
- **Benchmarks.** `tests/bench/` — Go vs Rust runtime. See [performance.md](performance.md).

## Glossary

| Term | Definition |
|---|---|
| **BadgerDB** | Embeddable key-value store in Go. LSM-tree design, ACID transactions, optional per-key TTL. No external process required. |
| **cgroup v2** | Linux control group v2. Used by the Rust runtime to enforce per-job CPU and memory limits. |
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
| **Artifact store** | Object-storage abstraction for ML job bytes. S3-compatible (MinIO in dev); `file://` fallback for local testing. Addressed by `s3://<bucket>/jobs/<job-id>/<path>`. |
| **Stager** | Node-side component that prepares a per-job working directory: downloads declared inputs before `Run()`, uploads declared outputs after exit 0. |
| **`from:` reference** | Workflow YAML syntax for "rewrite this input's URI to the upstream job's resolved output URI at dispatch time." |
| **ResolvedOutputs** | Per-job record of the `(name, uri, sha256, size)` tuples the coordinator persists once the stager uploads on exit 0. Attested via scheme + prefix + suffix + declared-name checks. |
| **Service job** | Long-running job with a `service: {port, health_path}` block. Runtime skips timeout enforcement; node runs a readiness prober. |
| **`CUDA_VISIBLE_DEVICES`** | Env var set by the runtime on GPU jobs (list of claimed device indices) and on CPU jobs running on GPU-equipped nodes (empty string, hides all devices from the process). |
