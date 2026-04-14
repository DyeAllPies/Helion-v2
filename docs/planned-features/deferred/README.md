# Deferred Enhancements — Running Backlog

**Priority:** P3 by default (individual items may be higher — see the priority table)
**Status:** Not started
**Scope:** Items deferred during feature implementation, consolidated here rather than duplicated across per-feature specs.

## Context

With the completion of features 01–07, Helion v2 reached **minimal orchestrator** status.
All capabilities required to express, schedule, and manage real multi-step workloads are
implemented. This document started out consolidating the 14 enhancements identified during
those initial features; it has since grown to cover any deferral flagged during later work
(feature 09's analytics pipeline, feature 10's ML pipeline, etc.).

**When to add here vs. a new numbered feature doc:** if an item is identified mid-slice as
"we could do this but it's not core to the feature we're shipping," file it here under the
feature it came from. New numbered docs under `docs/planned-features/` are for *active
slices*. The backlog is append-only — moving an item into a numbered slice (as happened
with GPU resources → feature 10 step 5) replaces the entry here with a pointer.

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

**Moved out of this backlog.** Promoted into the active ML-pipeline
work; see [`../10-minimal-ml-pipeline.md`](../10-minimal-ml-pipeline.md)
§ Step 5 — GPU as a first-class resource. The label-based node
matching piece already landed in step 4 of the same doc. The entry
is kept here (rather than deleted) so readers coming from older
references (commits, audits) still find the pointer.

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

### Coordinator-side in-use resource tracking (CPU, memory, GPU)

The scheduler's [`ResourceAwarePolicy`](../../../internal/cluster/policy_resource.go) already uses node-reported total CPU / memory / slots / GPUs as bin-packing dimensions, but it does **not** subtract resources currently claimed by jobs that have already been dispatched to a node. The `running_jobs` count is used as a coarse proxy ("each job uses DefaultResourceRequest().CpuMillicores"), which works because the existing CPU/memory tracking is itself coarse — but the proxy breaks down for GPUs, where each whole-device claim is exact and the coordinator should know what's free per node.

**Today's behaviour, all dimensions:**

- Node A reports `TotalGpus=4`. Four 1-GPU jobs are currently dispatched.
- Scheduler picks Node A again for a fifth 1-GPU job (no in-use tracking → still looks free).
- Runtime allocator on Node A correctly fails the dispatch ("insufficient devices"), the dispatch loop transitions the job to Failed, the retry policy re-enqueues, and eventually it lands on a node with capacity.
- Net effect: one wasted RPC + retry round-trip per oversubscription. Correct outcome, slow path.

**The fix:** before each `Pick`, walk `JobStore.NonTerminal()` and count the resource reservations of jobs whose `NodeID` matches each candidate. Subtract from the node's reported totals. Cache the per-node usage per dispatch tick to avoid O(jobs × nodes) cost — the dispatch loop runs at 100 ms today so a single per-tick pass is fine.

Apply the same treatment to CPU millicores and memory bytes (the existing proxy logic was always a placeholder until proper tracking landed). GPU is the case that motivated the work — it's the dimension where the coordinator-side oversubscription cost is highest because every dispatched-then-failed GPU job ties up real device slots on the wrong node for a tick.

**Why deferred:** the runtime-side allocator already provides correctness — bad scheduling decisions fail fast rather than producing duplicate device assignments. The fix is a UX/efficiency improvement, not a correctness one. Bundling it with the broader scheduler-tracking refactor (CPU + memory + GPU all at once) is cleaner than three separate slices.

### Hardware attestation of node labels

The ML pipeline slice in [10-minimal-ml-pipeline.md](10-minimal-ml-pipeline.md) lets nodes self-report labels (`gpu=a100`, `cuda=12.4`, `zone=us-east`) that the scheduler uses for `node_selector` matching. The trust boundary today is mTLS + ML-DSA node certificates: "this is node X that we issued a cert for." It does **not** cover "this node actually owns the hardware it claims." A compromised node under the existing cert can register with `gpu=a100` on a CPU-only host, win GPU-targeted jobs, and either run them incorrectly or exfiltrate the artifacts they stage.

Proper mitigation needs hardware attestation — TPM quotes, Intel SGX / TDX, AMD SEV-SNP, or confidential-VM attestation anchored in the cloud provider's root of trust. The coordinator would verify an attestation quote at Register time and bind the registered labels to the measured hardware.

**Why deferred:** All four attestation paths add heavy dependencies and meaningful per-deployment setup. None of them is universally available — bare-metal clusters, mixed clouds, and ARM dev boxes don't all have the same attestation surface. Shipping minimal ML support should not block on choosing one. The operator mitigation in the interim is to set labels via deployment env (`HELION_LABEL_*`) from a trusted control plane (k8s Deployment, Nomad job spec, systemd unit), treating the node-agent's `nvidia-smi` auto-probe as best-effort metadata for friendly clusters only.

### Registry lineage enforcement

*From the 2026-04-14 audit (M3, M4) — see [`../../audits/2026-04-14.md`](../../audits/2026-04-14.md).*

Step 6 of the ML pipeline introduced a dataset + model registry where a model carries `source_job_id` and `source_dataset` fields that point back to the training job and its inputs. Today those pointers are **soft**: the registry does not validate `source_job_id` against the JobStore at register time, and deleting a dataset does not detect or cascade to models that reference it. A model can end up with a `source_dataset` pointing at a name+version that no longer resolves.

Tightening this has three shapes, each with a real cost:

1. **Reject-on-reference delete** — full model-prefix scan per dataset delete; blocks legitimate retention / GDPR deletes.
2. **Cascade delete** — silently removes downstream artifacts that may be in production serving paths. Dangerous default.
3. **Dangle detection on read** — materialise the lineage join at `GET /api/models/...` time; changes response shape + couples registry read-path to dataset store.

Similarly, validating `source_job_id` at register time would require the registry package to import the JobStore (collapsing the current clean separation) and is race-prone against job GC.

**Why deferred:** the explicit step-6 design treats lineage as a historical audit trail, not a foreign-key constraint. The spec is internally consistent and the failure mode is cosmetic (broken UI link at worst). Revisit when either (a) a deployment reports that broken lineage confused an operator in practice, or (b) the ML pipeline grows a "model delete" UX that would benefit from automatic dependent-model cleanup. If (b) lands first, (1) becomes the natural fix shape; if (a) lands first, (3) is the lower-risk path.

### Registry indexed listing

*From the 2026-04-14 audit (L1) — see [`../../audits/2026-04-14.md`](../../audits/2026-04-14.md).*

`ListDatasets` / `ListModels` currently full-scan the prefix, JSON-decode every entry, sort by `CreatedAt`, and slice to the requested page. Cost is O(n) in total registered entries per list call regardless of page size.

**Why deferred:** the handler is behind the registry rate limiter (2/s per subject, burst 30) and even at 100k entries the scan is sub-50 ms with BadgerDB's LSM layout. The fix — either a secondary `CreatedAt` index or cursor-based pagination — is a meaningful scope increase that earns nothing at current traffic. Revisit if a real operator reports registry size past the 10k mark, or if the dashboard's ML module (step 8) starts driving a lot of parallel list requests.

### Registry integration test through a real coordinator

*From the 2026-04-14 audit (L3) — see [`../../audits/2026-04-14.md`](../../audits/2026-04-14.md).*

`internal/api/handlers_registry_test.go` exercises the handlers through the ServeMux in a single process with an in-memory BadgerDB. There is no test under `tests/integration/` that spins up the coordinator binary with mTLS + a real registered dataset end-to-end.

**Why deferred:** the existing `tests/integration` harness is shaped around gRPC node registration and workflow dispatch. Registry is HTTP-only, and the handler tests already exercise the full validator / rate-limit / audit / event-emission chain within a single process. A new integration shape for this surface is boilerplate-heavy and would mostly catch wiring regressions in `cmd/helion-coordinator/main.go`. If a wiring regression ever lands it will also be caught by the step-10 iris example, which drives the registry end-to-end as part of its own acceptance test.

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
| GPU resources | — | — | **Moved** to feature 10 step 5 — see the pointer in the GPU section above. |
| Affinity/anti-affinity | High | Low | P3 — complex scheduler change |
| Per-job resource tracking | Medium | Low | P3 — cardinality issues |
| Alerting / Grafana templates | Low | Low | P3 — users own their stack |
| Node label hardware attestation | High | High | P3 — needs TPM/SGX/SEV-SNP integration, deployment-specific |
| In-use resource tracking (CPU/mem/GPU) | Medium | Medium | P2 — runtime fail-fast keeps things correct, but oversubscription wastes a dispatch RPC per attempt |
| Registry lineage enforcement (M3/M4, 2026-04-14) | Medium | Low | P3 — lineage is spec'd as soft; revisit when a deployment reports broken-link confusion or a model-delete UX needs cascade |
| Registry indexed listing (L1, 2026-04-14) | Medium | Low | P3 — rate limiter + LSM scan keeps current size bands safe; revisit past 10k entries |
| Registry integration test (L3, 2026-04-14) | Low | Low | P3 — handler tests cover end-to-end; step-10 iris example will drive real-coordinator path |
