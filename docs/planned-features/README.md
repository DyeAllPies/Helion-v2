# Planned Features

Feature plans for evolving Helion v2 into a minimal production orchestrator.

## Gap Analysis

| Feature | Current Status | Priority | Doc |
|---------|---------------|----------|-----|
| Workflow / DAG support | **Implemented** | P0 | [01-workflow-dag.md](01-workflow-dag.md) |
| Retry + failure policies | **Implemented** | P0 | [02-retry-failure-policies.md](02-retry-failure-policies.md) |
| Resource-aware scheduling | Partial | P1 | [03-resource-aware-scheduling.md](03-resource-aware-scheduling.md) |
| Job state machine improvements | Partial | P1 | [04-job-state-machine.md](04-job-state-machine.md) |
| Priority queues | Missing | P1 | [05-priority-queues.md](05-priority-queues.md) |
| Event system | Missing | P2 | [06-event-system.md](06-event-system.md) |
| Observability improvements | Partial | P2 | [07-observability.md](07-observability.md) |

### What already works well

These areas are solid and do not need feature plans:

- **Node health + heartbeats** — Push-based heartbeat (10s default), stale detection, stream revocation, crash recovery with 15s grace period.
- **Execution isolation** — Dual runtime (Go subprocess with namespace gating + Rust runtime with cgroup v2, seccomp-bpf). Resource limits enforced at job level.
- **Authentication + security** — JWT, mTLS, PQC (ML-DSA/ML-KEM hybrid), per-node rate limiting, append-only audit log.
- **API surface** — REST, gRPC, WebSocket endpoints with comprehensive coverage.
- **Persistence** — BadgerDB with TTL, type-safe Go wrapper, atomic transitions.

### Priority definitions

- **P0** — Required for minimal orchestrator. Without these, the system cannot express real workloads.
- **P1** — Required for production use. System works without them but will hit scaling/reliability walls quickly.
- **P2** — High-impact improvements. Make the system significantly more useful but aren't blockers.
