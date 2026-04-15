# Deferred: Rust runtime parity for inference services

**Priority:** P2
**Status:** Deferred
**Originating feature:** [feature 17 — ML inference jobs](../17-ml-inference-jobs.md)

## Context

Feature 17 added a long-running inference-service mode to `DispatchRequest` and the Go runtime. The `RunRequest.IsService` flag causes the Go backend (`internal/runtime/go_runtime.go`) to skip default-timeout enforcement and run the process until cancelled; the node-side prober in `internal/nodeserver/service_prober.go` hits `127.0.0.1:<port><health_path>` and emits `ReportServiceEvent` RPCs on readiness transitions.

The Rust runtime (`runtime-rust/`) is the second backend — selected when `HELION_RUNTIME=rust` on the node. It has its own IPC proto (`proto/runtime.proto`) and its own process-lifecycle code (`runtime-rust/src/`). Neither has been updated for feature 17. A service job dispatched to a Rust-backed node today will:

1. Hit the Rust runtime's default timeout (from `RunRequest.timeout_seconds` — the field does propagate) and get killed after a few minutes. The `IsService` flag is not on `proto/runtime.proto` yet, so the Rust side cannot know to skip timeout enforcement.
2. Never be probed — the prober lives in the Go nodeserver, which runs alongside the Rust subprocess regardless of which runtime is handling the command. That part actually works cross-backend. But the underlying service will have been killed by then.

## Why deferred

Three reasons:

1. **The Go backend is the default.** Production deployments today use `HELION_RUNTIME=go` unless they specifically need cgroup v2 + seccomp isolation, which is orthogonal to inference-service work. Feature 17's acceptance criteria can be met entirely on the Go runtime.
2. **Rust parity is a mechanical proto + IPC edit plus cooperative timeout handling in the Rust process supervisor.** None of that is hard; it just takes a full Rust build + test cycle and a matching `runtime-rust/src/proto.rs` regeneration, which is substantial churn for a slice that the Go runtime already covers.
3. **The work is cleaner once the Rust-runtime resource-tracking slice also lands.** That slice (tracked separately under in-use resource tracking; see this folder's index) will regenerate `proto/runtime.proto` anyway. Bundling the `IsService` field into that proto bump avoids two back-to-back regenerations.

Until then, `docs/SECURITY.md` § 5 documents the gap: service jobs on Rust-backed nodes run without timeout bypass or probe coverage, so they will get killed on the default timeout and never reach the `GET /api/services/{id}` mapping. Operators running an inference workload on a Rust-backed cluster should stay on the Go runtime for that node pool.

## Revisit trigger

- The next proto/runtime.proto regeneration (whoever lands first: in-use resource tracking, Rust GPU parity, or an explicit `IsService` parity slice).
- Or: an operator files a "service died after 5 min on a Rust-backed node" issue. That's the confirmation that the gap actually bites someone in practice.
