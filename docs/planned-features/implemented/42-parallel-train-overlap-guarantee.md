# Feature: Verify + enforce parallel `train_light` ‖ `train_heavy` execution

**Priority:** P2
**Status:** Implemented (2026-04-21) — the E2E overlap assertion surfaced a real dispatcher serialisation bug; fixed by goroutine-per-job in `internal/cluster/dispatch.go`.
**Affected files:**
`internal/cluster/workflow_dispatch_test.go` (new or appended —
parallel-siblings unit test),
`internal/cluster/policy_resource.go` (only if root-cause turns out
to be a scheduling bug; otherwise no change),
`docker-compose.iris.yml` (possibly — bump `HELION_MAX_SLOTS` on the
iris-node-2 + mnist-node-rust nodes if slot contention is the cause),
`dashboard/e2e/specs/ml-mnist-parallel-walkthrough.spec.ts` (new
post-run assertion that `train_light` and `train_heavy` wall-clock
intervals overlap),
`examples/ml-mnist/workflow.yaml` (no change expected — verify only).

## Problem

Feature 40 shipped a 5-job MNIST workflow where `train_light` and
`train_heavy` are structurally parallel (both depend only on
`preprocess`, no edge between them), but **concurrency is assumed,
not measured.** The walkthrough spec waits for 5 job rows to appear
and parks for a "live camera beat," without ever asserting that the
two training jobs overlap in wall-clock time.

A silent serialisation would:
- Break the video's narrative (two runtimes at once is the whole
  point of the heterogeneous demo).
- Miss a real bug — a single-slot node, a selector that pins both
  jobs to the same host, or a dispatcher FIFO that waits for one
  `Running → Succeeded` before picking the next `Pending` sibling
  would all produce a correct-looking DAG with serial execution.

## Current state

- [`examples/ml-mnist/workflow.yaml`](../../../examples/ml-mnist/workflow.yaml)
  lines 70-121: `train_light.depends_on: [preprocess]` and
  `train_heavy.depends_on: [preprocess]` — structurally parallel.
- [`ml-mnist-parallel-walkthrough.spec.ts:310-321`](../../../dashboard/e2e/specs/ml-mnist-parallel-walkthrough.spec.ts#L310-L321):
  waits for all 5 rows, doesn't assert start/finish overlap.
- `workflow_outcomes` row written by feature 40 carries
  `started_at` + `duration_ms` at workflow grain — **not at job
  grain.** Per-job timing lives in the `jobs` table (or equivalent
  analytics sink). Overlap needs the per-job columns.
- No existing unit test in `internal/cluster/*` asserts "two
  sibling jobs both hit Running before either hits Succeeded."
  The closest is the DAG-validation tests, which check the graph
  shape, not the dispatch timeline.

## Design

### 1. Unit test — parallel-siblings invariant

A new table-driven test in `internal/cluster` that spins up a fake
cluster with two nodes (`go-node`, `rust-node`), submits a 3-job
workflow (`root → {leftChild, rightChild}`), and asserts:

```go
// Both children in Running before either transitions to Succeeded.
require.Eventually(t, func() bool {
    left, right := cluster.getJobState("leftChild"), cluster.getJobState("rightChild")
    return left == pb.JobState_RUNNING && right == pb.JobState_RUNNING
}, 2*time.Second, 10*time.Millisecond,
    "expected both siblings to reach Running concurrently; left=%s right=%s",
    left, right)
```

Fake nodes delay `FinishJob` by 200 ms so the `Running` window is
observable without race-condition flake. The test is deterministic
because both nodes are pre-registered and have ≥1 free slot each.

### 2. E2E overlap assertion

After the MNIST walkthrough's workflow reaches `completed`, fetch
per-job timing via
`GET /api/jobs?workflow_id=<wfid>` and assert:

```typescript
const trainLight = jobs.find(j => j.id === 'train_light')!;
const trainHeavy = jobs.find(j => j.id === 'train_heavy')!;

const lightStart  = Date.parse(trainLight.started_at);
const lightFinish = Date.parse(trainLight.finished_at);
const heavyStart  = Date.parse(trainHeavy.started_at);
const heavyFinish = Date.parse(trainHeavy.finished_at);

// Intervals [a,b] and [c,d] overlap iff a < d && c < b.
expect(lightStart).toBeLessThan(heavyFinish);
expect(heavyStart).toBeLessThan(lightFinish);
```

Tolerance: no `toBeCloseTo` — a strict overlap check is what we want.
The lightest train run still takes ≥2 s, so there's room for the
scheduler's 100-ms tick without the intervals being degenerate.

### 3. Root-cause fix (conditional)

If the unit test or E2E assertion fails on first run, the fix is
one of the following. **Do not speculate; fix only what the failing
test proves:**

- **Slot contention.** Iris overlay gives each node `max_slots=1`.
  Bump to 2 (or higher) on `iris-node-2` and `mnist-node-rust` via
  `HELION_MAX_SLOTS=2` in `docker-compose.iris.yml`.
- **Selector overlap.** If both jobs match the same node, the
  second waits. Verify via the `HELION_LABEL_runtime=<go|rust>`
  pinning in the overlay — should already be exclusive per feature
  21.
- **Dispatcher serialisation.** The workflow lifecycle emits a
  single `dispatch` tick per state change. If it only emits for the
  first Ready job, the second waits until the next heartbeat.
  Read [`workflow_lifecycle.go`](../../../internal/cluster/workflow_lifecycle.go)
  and fix at the source.

Landing the unit test first catches this regardless of which of the
three causes is real.

## Security plan

No new attack surface. Bumping `max_slots` is the only possibly
risky change — it increases per-node concurrency. Mitigations:

- Existing per-node CPU / memory bin-packing
  ([`policy_resource.go`](../../../internal/cluster/policy_resource.go))
  still bounds real resource use; a higher slot count doesn't
  override resource checks, just permits more jobs to pass the
  slot gate.
- E2E nodes are demo-only — no shared tenants. Production
  deployments keep their own slot ceilings.
- No new audit events, no new endpoints, no RBAC changes.

## Implementation order

| # | Step                                                               | Depends on | Effort |
|---|--------------------------------------------------------------------|-----------|--------|
| 1 | Parallel-siblings unit test in `internal/cluster`                  | —         | Small  |
| 2 | Run it — if red, root-cause per Design §3 and land the fix         | 1         | Small–Medium (depends on cause) |
| 3 | E2E overlap assertion at end of MNIST walkthrough                  | 2         | Small  |
| 4 | Re-record walkthrough video if overlap window changes meaningfully | 3         | Small  |

## Tests

**Unit:**
- `TestDispatcher_ParallelSiblings_BothReachRunning`: asserts the
  two-children invariant above.
- `TestDispatcher_ParallelSiblings_SingleNodeSingleSlot_Serialises`:
  negative control — with `max_slots=1` on the only matching node,
  the siblings serialise. Proves the test measures what it claims
  (no false positive where both jobs just happen to run fast).

**E2E:**
- Overlap assertion in `ml-mnist-parallel-walkthrough.spec.ts`
  (per Design §2).
- New short spec `parallel-dispatch-overlap.spec.ts` covering a
  minimal 3-job workflow — runs in the standard suite so a
  regression surfaces without waiting on the full MNIST flow.

## Open questions

- Should we promote per-job `started_at` / `finished_at` into the
  `workflow_outcomes` row as a JSONB timeline column? It would
  unblock a "Gantt chart" dashboard view later. Out of scope for
  this slice — log as a followup if the Analytics UI wants it.

## Deferred

- GPU-accelerated training variant (would make `train_heavy`
  genuinely slow and showcase GPU dispatch alongside parallelism).
  Tracked as a dataset-size concern in
  [feature 43](43-mnist-asymmetric-variants.md) for now.

## Implementation status

_Filled in as the slice lands._
