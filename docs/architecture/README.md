> **Audience:** engineers
> **Scope:** Index for the engineer-facing technical reference — how Helion is built.
> **Depth:** reference

# Architecture

Engineer-facing reference for extending Helion. For the project landing page, see
[`../README.md`](../README.md). For the contributor process (templates, audits,
feature specs) see [`../DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md).

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

- [`../ARCHITECTURE.md`](../ARCHITECTURE.md) — tech-decision summary + protocol contracts + pipeline sketch.
- [`../COMPONENTS.md`](../COMPONENTS.md) — coordinator / node / runtime internals.
