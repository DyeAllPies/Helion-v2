> **Audience:** everyone
> **Scope:** Landing page — project description, architecture diagram, quickstart, audience lanes.
> **Depth:** reference

# Helion v2

[![CI](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/DyeAllPies/Helion-v2/actions/workflows/ci.yml)

A minimal distributed job scheduler written in Go — built as a student
learning project for systems programming, distributed systems theory,
container orchestration, and production-grade security practices. **Not
intended for production deployment**; the posture throughout is "better
safe than sorry" so the project exercises the right patterns for real
systems, even on code nobody will actually run in the wild.

The stack covers the full orchestrator story end-to-end: workflow / DAG
support, retry policies, resource-aware scheduling, a job state machine,
priority queues, an event bus with WebSocket push, observability (job
logs, metrics, subsystem health), an Angular dashboard, an analytics
pipeline over Postgres, an ML pipeline (artifact store, dataset/model
registry, inference services, parallel-heterogeneous training demo), and
a hybrid post-quantum security layer on top of mTLS.

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

The deployment target was Kubernetes — coordinator as a `Deployment`,
node agents as a `DaemonSet`, dashboard behind an Nginx reverse proxy.
**Helm manifests live under [`../deploy/helm/`](../deploy/helm/) but have
never been exercised against a real cluster** — the project only runs
under Docker Compose and GitHub Actions CI. The chart ships as design
scaffolding, not a deployment artefact.

## Stack

| Concern | Choice | Why |
|---|---|---|
| Primary language | Go 1.26 | Native to the K8s / Docker ecosystem; same as etcd, Consul, Prometheus |
| Runtime | Rust | Memory safety without GC for namespace / cgroup / seccomp code |
| Inter-node protocol | gRPC + Protocol Buffers | Typed contracts, bidirectional streaming, mTLS-native |
| Persistence | BadgerDB | Embedded, ACID, pure Go; swap path to etcd if HA is needed |
| Dashboard | Angular 21 | Covers the enterprise framework gap; real WebSocket + auth complexity |
| Key exchange | ML-KEM / Kyber (NIST FIPS 203) | Hybrid PQC — quantum-resistant from day one |
| Signatures | ML-DSA / Dilithium (NIST FIPS 204) | Node certificate signing, hybrid with ECDSA |
| Deployment (planned, untested) | Kubernetes + Helm | Scaffold only — never run against a real cluster |
| CI/CD | GitHub Actions + Snyk | Build, lint, test (with `-race`), coverage gate, dependency CVE scanning |

## Start here

Pick a lane based on what you're trying to do:

- **"I want to read how Helion is built"** → [`architecture/`](architecture/).
  Component internals, protocol contracts, persistence, the Rust runtime,
  the Angular dashboard, and runtime benchmarks.
- **"I want to run a cluster"** → [`operators/`](operators/). Environment
  variables, startup checklists, cert rotation runbooks, WebAuthn setup,
  JWT handling, the Docker-Compose workflow.
- **"I want to submit a workflow"** → [`guides/`](guides/). User-facing
  guides for writing workflows and ML pipelines on Helion.
- **"I want to understand the threat model"** →
  [`security/`](security/). Threat model grouped by subsystem (crypto,
  auth, operator-auth, runtime, data-plane).

For the contributor process behind feature specs, audits, and deferred
items, see [`DOCS-WORKFLOW.md`](DOCS-WORKFLOW.md).

## Quickstart

```bash
# 1. Boot the cluster (one coordinator + two node agents on a bridge net).
docker compose up --build

# 2. Build all binaries.
go build ./...

# 3. Run the full test suite.
go test -race -count=1 ./...

# 4. Run all local gates before pushing (Go + Rust + Angular + docs-lint).
make check

# 5. Add the Playwright E2E suite (use before pushing infra changes).
make check-full
```

CI coverage gates: `internal/` ≥ 85%, `cmd/` ≥ 25%, dashboard 85 / 60 / 85 / 85
(statements / branches / functions / lines). Failing either tier fails
the build.

## Status and known constraints

- **Not production-deployed.** The project has never been installed against
  a real Kubernetes cluster; every validation path ends at Docker Compose
  + CI. Treat the security posture and Helm scaffolding as "what
  production would look like" rather than "what production does look like."
- **GPU scheduling is code-complete but unverified on real hardware.**
  Feature 15 ships the scheduler's GPU allocator, `CUDA_VISIBLE_DEVICES`
  pinning, and a build-tag-gated test harness under `tests/gpu/` that
  exercises `nvidia-smi` when present. GitHub Actions' free tier has no
  GPU runners. See
  [`planned-features/implemented/15-ml-gpu-first-class-resource.md`](planned-features/implemented/15-ml-gpu-first-class-resource.md).
- **Single coordinator replica.** HA requires swapping BadgerDB for etcd
  — the interface is already designed for it.
- **Linux-only isolation.** Node agents require Linux for namespace
  isolation. Cross-compiled binaries run on other OS targets without
  isolation, suitable for local development.
- **Resource limits enforced only by the Rust runtime.** `memory_bytes` and
  `cpu_quota_us` are honoured by `HELION_RUNTIME=rust`. The Go runtime
  ignores them.

## Packing the repo without audits and planned features

`audits/` and `planned-features/` grow over time. To bundle the source
tree for download, an AI assistant, or offline review **without** those
directories:

```bash
# With repomix:
npx repomix --ignore "docs/audits/**,docs/planned-features/**"

# Or with git:
git archive --format=tar.gz -o helion-v2.tar.gz HEAD \
  -- ':(exclude)docs/audits' ':(exclude)docs/planned-features'
```

This keeps [`DOCS-WORKFLOW.md`](DOCS-WORKFLOW.md) and the three
`TEMPLATE.md` files (so a fresh reader still sees how the process works)
and drops only the dated audit files and the feature-spec archive.

---

*Dennis Alves Pedersen · April 2026*
