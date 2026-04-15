# Feature: ML Node Labels and Selectors

**Priority:** P1
**Status:** Done
**Affected files:** `internal/cluster/registry_node.go`, `internal/cluster/scheduler.go`, `internal/cluster/dispatch.go`, `internal/cluster/dag.go`, `cmd/helion-node/labels.go`.
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## Node labels and selectors

Extend node registration:

```go
type nodeEntry struct {
    // ... existing fields ...
    Labels map[string]string // e.g. {"gpu": "a100", "cuda": "12.4", "zone": "us-east"}
}
```

Labels are reported by the node binary at registration time, sourced from:

- Environment variables prefixed with `HELION_LABEL_` (operator-set).
- Auto-detected: `gpu=<model>` if `nvidia-smi` succeeds, `os=<linux|darwin|windows>`,
  `arch=<amd64|arm64>`. Auto-detection is best-effort and additive.

The scheduler gains a `node_selector` filter applied **before** the
resource policy runs — selectors narrow the candidate set, then bin-packing
chooses among survivors. Selector semantics are exact-match equality only
(no `In`, no `NotIn`, no glob) — this is the minimal cut.

If no node matches, the job stays pending and emits a
`job.unschedulable` event with the unsatisfied selector. (We do **not**
add a "wait forever vs. fail fast" policy here; it surfaces in feedback
naturally and can be a P2 follow-up.)

### Step-3 follow-up to pick up during this step

While the DAG validator is being touched for selector-aware checks,
extend `validateFromReferences` to reject `from:` references whose
upstream dependency condition is `on_failure` or `on_complete`. A
downstream that uses `from: X.OUT` can only ever succeed when X
produced `OUT`, which means X must have completed successfully —
the [step-2](12-ml-job-io-staging.md) stager only uploads on success. Writing a workflow that
combines `on_failure` + `from` is therefore always unreachable at
runtime, but today it slips past submit and fails late at dispatch
with `ErrResolveOutputMissing`. Catching it at submit saves a
debugging round-trip for the first user who tries the pattern. One
extra pass in `validateFromReferences`; errors surface as a new
`ErrDAGFromConditionUnreachable`.

## Implementation notes — node labels + node_selector scheduling (done)

Data flow: the node agent collects labels at startup and passes them
through `RegisterRequest.labels`; the coordinator sanitises, persists
them on `cpb.Node`, and the scheduler's new `PickForSelector` applies
exact-match filtering before the configured policy runs its
bin-packing / round-robin logic.

Node-agent label sources
([`cmd/helion-node/labels.go`](../../cmd/helion-node/labels.go)):

- **Auto-detected baseline** — `os=<goos>`, `arch=<goarch>`, and
  `gpu=<model>` when `nvidia-smi --query-gpu=name` succeeds. The GPU
  probe is a `var gpuProbe = runNvidiaSmi` injection seam so unit
  tests stub it without touching real hardware (the project memory
  note about keeping GPU tests small and local applies here).
- **Operator overrides** via `HELION_LABEL_<KEY>=<VALUE>`
  environment variables. Key is lower-cased; later wins over
  auto-detection, so an explicit `HELION_LABEL_GPU=none` hides a
  physical card from the scheduler.

Coordinator sanitisation
([`registry_node.go`](../../internal/cluster/registry_node.go)):
NUL / C0 / DEL rejection, `=` in keys rejected (would break env
round-trips), k8s-compatible caps (≤32 entries, ≤63-byte keys,
≤253-byte values). A malicious or misconfigured node sending
oversize or malformed labels has them dropped silently — the node
stays addressable, only the bad labels are stripped.

Scheduler
([`scheduler.go`](../../internal/cluster/scheduler.go)): new
`PickForSelector(selector)` filters healthy nodes by exact-equality
label match before delegating to the configured Policy
(RoundRobin / LeastLoaded / ResourceAware — all untouched). Two
distinct sentinels make dispatch-time handling precise:

- `ErrNoHealthyNodes` — nothing to pick from (retriable; wait for
  a node).
- `ErrNoNodeMatchesSelector` — healthy nodes exist but none have
  the requested labels (**not** retriable — retrying won't invent
  labels; the job stays pending and emits
  `job.unschedulable`).

Dispatch + event
([`dispatch.go`](../../internal/cluster/dispatch.go)): on
`ErrNoNodeMatchesSelector` the dispatch loop publishes a
[`TopicJobUnschedulable`](../../internal/events/topics.go) event
carrying `job_id` + `unsatisfied_selector`, then moves on. A
per-job debounce (`unschedulableEmitCooldown = 30s`) prevents event
spam while a job is stuck; the debounce state clears the moment a
successful pick happens, so recovery is observable.

DAG validator — step-3 follow-up landed here
([`dag.go`](../../internal/cluster/dag.go)):
`ErrDAGFromConditionUnreachable` rejects a workflow at submit time
when a job has `from:` references but its dependency condition is
`on_failure` or `on_complete`. The stager only uploads on success,
so that combination could only ever fail resolution at dispatch —
catching it at submit saves the user a confusing late failure.

Security summary: labels ride the existing mTLS + hybrid PQ channel
(no new crypto). The sanitiser is the coordinator-side trust gate
against node-supplied metadata; a compromised node cannot influence
selector matching beyond what its own sanitised labels advertise.
Label values are treated as untrusted strings throughout — never
passed through shell / env-var expansion, only compared with
`string ==` and rendered in structured log / event fields.

Tests: **24 across the slice**
([`scheduler_selector_test.go`](../../internal/cluster/scheduler_selector_test.go),
[`registry_labels_test.go`](../../internal/cluster/registry_labels_test.go),
[`dispatch_unschedulable_test.go`](../../internal/cluster/dispatch_unschedulable_test.go),
[`dispatch_debounce_internal_test.go`](../../internal/cluster/dispatch_debounce_internal_test.go),
[`persistence_labels_test.go`](../../internal/cluster/persistence_labels_test.go),
[`dag_from_condition_test.go`](../../internal/cluster/dag_from_condition_test.go),
[`cmd/helion-node/labels_test.go`](../../cmd/helion-node/labels_test.go)).
Coverage: selector match / no-match / partial-match / empty-selector,
registry re-registration replacing labels, sanitiser drops for
bad entries, debounce window + recovery + cleanup on successful
pick, BadgerDB round-trip of labels + forward-compat with
pre-label records + omitempty on empty-labels, audit detail
contains sorted labels, condition-gate DAG rejection,
`nvidia-smi` stubbed probe.

### Deliberately not fixed, with rationale

Second-pass audit identified three concerns that were left
unaddressed in this slice. Each is documented in the section of
feature 10 (or the deferred-enhancements doc) where the fix would
naturally land, so the next author working on those areas picks
them up without having to re-discover the context:

- **Round-robin fairness under selector filtering** — deferred to
  [step 5](15-ml-gpu-first-class-resource.md) (GPU as a first-class resource). See that step's
  "Step-4 follow-up" subsection. The GPU slice already wants
  per-selector scheduler state for device-index tracking, so the
  refactor is a single change.
- **Unschedulable event payload doesn't distinguish "no healthy
  match" from "no match at all"** — deferred to [step 8](18-ml-dashboard-module.md) (Dashboard
  ML module). See that step's "Step-4 follow-up" subsection. The
  richer payload is load-bearing for the dashboard's Pipelines
  view, not for the dispatch loop itself.
- **Hardware attestation of node labels** — out of scope for
  feature 10 entirely; recorded in the backlog at
  [deferred/README.md § ML Pipeline / Hardware attestation of node labels](deferred/README.md#hardware-attestation-of-node-labels)
  with the mitigation operators can apply in the meantime
  (deployment-supplied labels via `HELION_LABEL_*`).

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| Scheduler queries node-reported labels | Labels entered via mTLS-authenticated Register RPC (§2); audit every `node.registered` with label set; admin override requires admin JWT | — |

| Threat | Mitigation |
|---|---|
| Node label spoofing to win GPU jobs | Labels carried in the Register RPC which is already mTLS-authenticated; audit every registration; admin override only via `POST /admin/nodes/{id}/labels` |
