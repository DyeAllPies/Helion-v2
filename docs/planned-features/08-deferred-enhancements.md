# Deferred Enhancements — Post-Orchestrator Backlog

**Priority:** P3
**Status:** Not started — consolidated from deferred items across features 01–07
**Affected files:** Various

## Context

With the completion of features 01–07, Helion v2 has reached **minimal orchestrator** status.
All capabilities required to express, schedule, and manage real multi-step workloads are
implemented. This document consolidates the 14 enhancements that were identified but
intentionally deferred during implementation to keep each feature focused on its core value.

---

## Workflow / DAG (from 01)

### Workflow parameters and templating

Allow workflows to accept parameters that are substituted into job commands/env:

```json
{
  "id": "deploy-v3",
  "params": { "version": "3.2.1", "env": "prod" },
  "jobs": [
    { "name": "build", "command": "make", "args": ["VERSION={{version}}"] }
  ]
}
```

**Why deferred:** Adds complexity without core value for the minimal orchestrator.

### Workflow re-run from failed step

Allow restarting a failed workflow from the step that failed, skipping already-completed jobs:

```
POST /workflows/{id}/retry?from=test
```

**Why deferred:** Requires tracking per-job completion state history and selective re-dispatch.

---

## Retry / Failure (from 02)

### Permanent failure classification

Distinguish transient failures (node unreachable, OOM) from permanent failures (command not found, permission denied) to avoid wasting retries:

| Failure type | Retry? | Signal |
|-------------|--------|--------|
| Transient | Yes | Node unreachable, timeout, OOM |
| Permanent | No | Exit code 127 (not found), 126 (permission denied) |
| Unknown | Yes (with limit) | Non-zero exit code |

**Why deferred:** All failures are currently retryable up to `max_attempts`. Classification requires inspecting exit codes and error messages from the runtime.

---

## Resource Scheduling (from 03)

### Resource overcommit

Allow scheduling more work than a node's physical capacity (e.g., 120% CPU). Useful for I/O-bound workloads that don't saturate CPU.

**Why deferred:** Safety risk — start with strict no-overcommit. Revisit when usage patterns are better understood.

### GPU / accelerator resources

Model GPU, TPU, or other accelerator resources as custom resource labels on nodes, matchable by job resource requests.

**Why deferred:** Requires Kubernetes device plugin integration and a custom resource label system.

### Affinity / anti-affinity scheduling

Allow jobs to express preferences: "run on same node as job X" (affinity) or "never on same node as job Y" (anti-affinity).

**Why deferred:** Not needed for the minimal orchestrator. Adds significant complexity to the scheduler.

---

## Priority (from 05)

### Mutable priority after submission

Allow changing a pending job's priority via `PATCH /jobs/{id}`:

```json
PATCH /jobs/{id}
{ "priority": 95 }
```

Only permitted for jobs in `pending` or `scheduled` state.

**Why deferred:** Requires a new API endpoint and validation that only non-dispatched jobs can be modified.

### Per-user priority limits

Cap the maximum priority a non-admin user can set (e.g., normal users max 70, admins max 100).

**Why deferred:** Requires a user/tenant model with role-based priority caps.

### Starvation reserve percentage

Reserve a configurable fraction (default 20%) of dispatch slots for the oldest pending jobs regardless of priority, preventing complete starvation of low-priority work.

**Why deferred:** The age-based priority boost (+1/min) already provides starvation prevention. The reserve adds complexity and may cause priority inversion.

---

## Event System (from 06)

### Webhook registration and delivery

HTTP webhook endpoints for external integrations:

```
POST /webhooks { "url": "https://...", "topics": ["job.completed"], "secret": "..." }
GET  /webhooks
DELETE /webhooks/{id}
```

Delivery guarantees: at-least-once, exponential backoff (5 attempts), HMAC-SHA256 signature, auto-disable after 50 consecutive failures.

**Why deferred:** The in-memory event bus + WebSocket stream covers the dashboard use case. Webhooks are a standalone add-on for external integrations.

### Event-driven DAG evaluation

Replace the poll-based "check all pending workflow jobs every tick" with event-driven evaluation: subscribe to `job.completed` events and immediately check downstream eligibility.

**Why deferred:** The polling approach works correctly. Event-driven evaluation is an optimisation that reduces latency for workflow job transitions but isn't required for correctness.

---

## Observability (from 07)

### OpenTelemetry distributed tracing

Full trace propagation: API request → scheduler → dispatch RPC → node runtime → result. Trace IDs in logs and `X-Trace-ID` response headers.

Dependencies: `go.opentelemetry.io/otel`, OTLP exporter, gRPC/HTTP interceptors.

**Why deferred:** Adds external dependencies. The structured logging with `job_id`/`node_id` fields and the event bus provide adequate correlation for single-coordinator deployments.

### Per-job resource utilisation tracking

Track actual CPU/memory usage per job (via cgroup stats from the Rust runtime) and expose as metrics.

**Why deferred:** Per-job-ID metrics cause cardinality explosion in Prometheus. Requires a different metric model (e.g., histograms by priority tier, not per-job counters).

### Alerting rules and SLO definitions

Ship predefined Prometheus alerting rules (e.g., "alert if pending queue > 100 for > 5 min") and SLO templates.

**Why deferred:** Users bring their own alerting stack. Providing metrics is sufficient; opinionated alerting rules may not match each deployment's SLOs.

### Grafana dashboard templates

JSON dashboard templates for Grafana showing job throughput, node health, queue depth, etc.

**Why deferred:** Same reason — users bring their own dashboards. The Prometheus metrics endpoint and our Angular dashboard are the primary interfaces.

---

## ML Pipeline (from feature 10)

### Hardware attestation of node labels

The ML pipeline slice in [10-minimal-ml-pipeline.md](10-minimal-ml-pipeline.md) lets nodes self-report labels (`gpu=a100`, `cuda=12.4`, `zone=us-east`) that the scheduler uses for `node_selector` matching. The trust boundary today is mTLS + ML-DSA node certificates: "this is node X that we issued a cert for." It does **not** cover "this node actually owns the hardware it claims." A compromised node under the existing cert can register with `gpu=a100` on a CPU-only host, win GPU-targeted jobs, and either run them incorrectly or exfiltrate the artifacts they stage.

Proper mitigation needs hardware attestation — TPM quotes, Intel SGX / TDX, AMD SEV-SNP, or confidential-VM attestation anchored in the cloud provider's root of trust. The coordinator would verify an attestation quote at Register time and bind the registered labels to the measured hardware.

**Why deferred:** All four attestation paths add heavy dependencies and meaningful per-deployment setup. None of them is universally available — bare-metal clusters, mixed clouds, and ARM dev boxes don't all have the same attestation surface. Shipping minimal ML support should not block on choosing one. The operator mitigation in the interim is to set labels via deployment env (`HELION_LABEL_*`) from a trusted control plane (k8s Deployment, Nomad job spec, systemd unit), treating the node-agent's `nvidia-smi` auto-probe as best-effort metadata for friendly clusters only.

---

## Implementation priority (suggested)

| Item | Effort | Impact | Suggested priority |
|------|--------|--------|-------------------|
| Webhook delivery | Medium | High | P1 — enables CI/CD integrations |
| Event-driven DAG | Low | Medium | P1 — reduces workflow latency |
| Permanent failure classification | Low | Medium | P2 — improves retry efficiency |
| Mutable priority | Low | Low | P2 — convenience feature |
| Workflow re-run from failed step | Medium | Medium | P2 — error recovery |
| OpenTelemetry tracing | High | Medium | P3 — adds external deps |
| Starvation reserve | Low | Low | P3 — age boost is sufficient |
| Workflow templating | Medium | Low | P3 — syntactic sugar |
| Resource overcommit | Low | Low | P3 — niche use case |
| Per-user priority limits | Medium | Low | P3 — needs tenant model |
| GPU resources | High | Low | P3 — needs K8s device plugin |
| Affinity/anti-affinity | High | Low | P3 — complex scheduler change |
| Per-job resource tracking | Medium | Low | P3 — cardinality issues |
| Alerting / Grafana templates | Low | Low | P3 — users own their stack |
| Node label hardware attestation | High | High | P3 — needs TPM/SGX/SEV-SNP integration, deployment-specific |
