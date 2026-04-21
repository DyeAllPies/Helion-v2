# Feature: Parallel-heterogeneous MNIST demo + workflow-level analytics

**Priority:** P2
**Status:** Implemented (2026-04-20)
**Affected files:**
`examples/ml-mnist/train.py` (new MAX_ITER + VARIANT env vars),
`examples/ml-mnist/compare.py` (new — reads both metrics, picks
winner, registers both models with winner tag),
`examples/ml-mnist/workflow.yaml` (5-job parallel DAG:
ingest → preprocess → train_light (Go) ‖ train_heavy (Rust) →
compare),
`internal/analytics/migrations/006_workflow_outcomes.{up,down}.sql`
(new denormalised rollup table),
`internal/events/topics.go`
(`WorkflowCompletedWithCounts` + `WorkflowFailedWithCounts` —
feature-40 enriched constructors with job_count / success_count /
failed_count / owner / tags),
`internal/cluster/workflow_lifecycle.go` (computes counts on
terminal transition + publishes enriched events),
`internal/analytics/sink_unified.go` (real
`upsertWorkflowOutcome` backed by the new table, was a stub
before),
`internal/api/handlers_analytics_unified.go` (new
`GET /api/analytics/ml-runs` endpoint + `MLRunRow`/`MLRunsResponse`
wire types),
`internal/api/handlers_analytics.go` (route registration),
`dashboard/e2e/specs/ml-mnist-parallel-walkthrough.spec.ts`
(new human-eye video spec — DAG builder opener → Nodes page
→ live heterogeneous dispatch on the Jobs page → DAG
completion → Analytics row verification),
`internal/events/topics_test.go` (4 new tests for the
enriched event constructors + defensive-copy invariant),
`internal/analytics/sink_unified_extras_test.go` (3 new tests
for the feature-40 insert branch — completed, failed, blank
workflow_id guard),
`internal/analytics/flush_test.go` (count assertion bumped
from 9 to 11 to reflect the two new workflow_outcomes
inserts),
`internal/analytics/migrations_test.go` (expected migration
list extended to include 006),
`internal/api/handlers_analytics_unified_test.go` (4 new tests
for the ml-runs endpoint — empty/DB-error/query-shape/
limit-clamp).

## Problem

Three user-facing gaps surfaced at once after features 21 + 28
shipped:

1. **The existing MNIST walkthrough was serial.** `train` was a
   single step on a Rust node. There was no observable moment
   where the dashboard user could see two different runtimes
   dispatching the same workflow's jobs at the same moment.
   The heterogeneous-scheduling story was real (feature 21
   wired it), but never showcased.

2. **The analytics sink recorded workflow-level outcomes as raw
   events only.** The `events` table captured every
   `workflow.completed` row, but the dashboard's only query
   path was a GROUP BY on event_type with no visible "this run
   contained 5 jobs, 4 succeeded, 1 failed on job X" row. The
   `upsertWorkflowOutcome` function was an explicit stub with
   a "wire this later" comment.

3. **Model comparison as a first-class concept didn't exist.**
   The registry could store N models with N metric maps, but
   there was no path that produced two models in a single run
   and tagged the winner. A `compare` step would have to exist
   outside the pipeline.

## Current state

After this feature lands:

- `examples/ml-mnist/workflow.yaml` is a 5-job DAG with a
  parallel training fork. `train_light` (Go node, max_iter=50)
  and `train_heavy` (Rust node, max_iter=400) run
  concurrently; `compare` merges their metrics, picks the
  winner by accuracy, and registers both models with a
  `tags.winner=true|false` badge.
- `internal/analytics/migrations/006_workflow_outcomes` adds a
  denormalised rollup table keyed on workflow_id with status,
  completed_at, job_count, success_count, failed_count,
  failed_job, owner_principal, and tags JSONB. Three indexes
  (completed_at DESC, status, owner_principal partial) serve
  the dashboard's "recent runs / by-status / by-owner" queries.
- The sink's real `upsertWorkflowOutcome` writes one row per
  workflow.{completed,failed} event. The events table keeps
  the raw history as the forever-record; `workflow_outcomes`
  is the operational window rollup.
- `WorkflowCompletedWithCounts` + `WorkflowFailedWithCounts`
  carry job counts + owner + tags on the event payload so the
  sink never has to query back into the job store mid-flush.
  Defensive tag-map copy prevents subscriber mutation from
  bleeding into other subscribers' views.
- `GET /api/analytics/ml-runs` returns the workflow_outcomes
  rows sorted newest-first, with a configurable limit (default
  50, cap 500).
- The parallel MNIST spec narrates the demo as: DAG builder
  opener → Nodes page (both runtimes visible) → Jobs page
  (watch parallel dispatch across nodes) → DAG detail
  (mermaid chart shows the T-shape) → Models (both registered,
  winner badge) → Analytics (REST asserts the
  workflow_outcomes row).

## Design

### 1. Parallel training via the existing scheduler

No scheduler changes. `train_light` and `train_heavy` each
`depends_on: [preprocess]` — they have no edge between them —
so the feature-21 dispatcher sees them both as eligible once
`preprocess` completes and picks the runtime-matching node
independently. The `node_selector: {runtime: go|rust}` label
is the same one `ingest` + `preprocess` + `register` already
use; the dispatcher's `nodeMatchesSelector()` path didn't need
a new code path.

### 2. `compare.py` — the merge step

Reads both metrics JSONs, picks the winner by a configurable
metric (`HELION_COMPARE_METRIC`, default `accuracy`), and
POSTs BOTH model records to the registry with distinct names
(`mnist-logreg-light`, `mnist-logreg-heavy`) and a winner tag.

Defensive properties:

- **Registers both models**, not just the winner. The losing
  model stays available for rollback, so an operator noticing
  a regression in the winner can revert without re-running the
  entire workflow.
- **Idempotent**: 409 on duplicate registration is treated as
  success, matching `register.py`.
- **Only numeric metrics survive the model body**, including a
  NaN/Inf filter. The registry's `Metrics` field is
  `map[string]float64` server-side; non-numeric values would
  have been silently dropped by `json.Marshal` anyway, but the
  explicit filter makes the intent obvious.

### 3. Analytics denormalisation

The feature-28 sink persisted raw events. Feature 40 adds one
UPSERT per workflow terminal event into `workflow_outcomes`
with `ON CONFLICT (workflow_id) DO UPDATE` — re-submitting a
workflow-id replaces the prior row with the new run's counts.
Event history in `events` is untouched so forensic reviewers
can still reconstruct prior runs.

### 4. DAG-builder opener in the walkthrough video

`ml-mnist-parallel-walkthrough.spec.ts` starts on `/submit`
(the DAG builder), clicks `+ add job` twice, types `ingest`
into the first job's name field so the live JSON preview at
the bottom updates on camera, then navigates to `/ml/pipelines`
and submits via REST. The narrative is "here's where you'd
build a DAG; we already have one ready to go." This avoids
the 40+ s flake risk of filling 5 jobs via click-by-click
form interactions while still showing the builder exists.

## Security plan

| Threat | Control |
|---|---|
| Operator poisons the rollup with a blank workflow_id | Sink's `upsertWorkflowOutcome` early-returns on empty workflow_id — never writes a row with "" as the primary key. |
| Malformed tags JSONB crashes the flush | JSONB marshal errors are swallowed and replaced with '{}'::JSONB. A corrupt tag map should never fail the entire analytics batch. |
| Caller's shared tag map is mutated between event creation and subscriber consumption | `WorkflowCompletedWithCounts` / `WorkflowFailedWithCounts` defensively copy the tags argument before storing it on the event payload. Unit-tested. |
| Legacy `WorkflowCompleted(workflowID)` caller produces a partial row | Sink's extractors default to zero/empty for missing fields, so a degraded row lands instead of no row at all. Operator sees `job_count=0` and can grep the event stream for the full history. |
| Compare script silently picks the wrong winner on tied metrics | `tied=true` is recorded in the comparison summary when v_light == v_heavy; the tiebreaker picks the lighter variant explicitly (cheaper to serve), not implicitly. |
| Two runs share a workflow_id (duplicate submission) | ON CONFLICT … DO UPDATE replaces the row. The events table retains the prior run's data as history. |
| A buggy publisher sends WorkflowFailed without `failed_job` | Column is NULLable; dashboard handles NULL as "multi-failure attribution unknown" rather than crashing. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | Extend `train.py` with HELION_TRAIN_MAX_ITER + HELION_TRAIN_VARIANT env vars. | — | Small |
| 2 | Add `compare.py` — parallel-variant merge + winner selection + dual registration. | 1 | Medium |
| 3 | Rewrite `workflow.yaml` into the 5-job parallel DAG. | 2 | Small |
| 4 | Migration 006 `workflow_outcomes` up/down SQL. | — | Small |
| 5 | Add `WorkflowCompletedWithCounts` + `WorkflowFailedWithCounts` event constructors with defensive tag-copy. | — | Small |
| 6 | Teach `workflow_lifecycle.go` to count jobs during terminal check + emit the enriched events. | 5 | Medium |
| 7 | Implement real `upsertWorkflowOutcome` targeting `workflow_outcomes` with ON CONFLICT upsert. | 4, 5 | Medium |
| 8 | Add `GET /api/analytics/ml-runs` endpoint + response types. | 7 | Small |
| 9 | Unit tests — event constructors, sink upsert (happy + blank-id guard), REST handler shape. | 5-8 | Medium |
| 10 | `ml-mnist-parallel-walkthrough.spec.ts` — gated by E2E_RECORD_MNIST_PARALLEL=1. | 1-8 | Medium |

## Tests

Added in this feature:

- `internal/events/topics_test.go`:
  - `TestWorkflowCompletedWithCounts_FullPayload` — every
    field round-trips.
  - `TestWorkflowCompletedWithCounts_EmptyOwnerAndTags_Omitted`
    — defensive omission matches `JobCompletedWithOutputs`
    pattern.
  - `TestWorkflowCompletedWithCounts_TagsDefensiveCopy` —
    caller mutation after construction does not bleed into
    the payload.
  - `TestWorkflowFailedWithCounts_FullPayload` +
    `TestWorkflowFailedWithCounts_NoOwnerTags`.

- `internal/analytics/sink_unified_extras_test.go`:
  - `TestFlush_WorkflowCompletedWithCounts_InsertsWorkflowOutcomeRow`
    — verifies every arg (workflow_id, status, timestamp,
    counts, owner, tagsJSON) reaches the SQL call.
  - `TestFlush_WorkflowFailedWithCounts_InsertsFailedRow` —
    status='failed', failed_job arg captured.
  - `TestFlush_WorkflowCompleted_MissingWorkflowID_NoInsert`
    — blank workflow_id never produces an INSERT.

- `internal/analytics/flush_test.go`:
  `TestUpsertSummaries_RoutesAllEventTypes` exec-call count
  bumped from 9 → 11 (two new workflow_outcomes inserts on
  workflow.{completed,failed}).

- `internal/analytics/migrations_test.go`:
  `TestLoadMigrations_ExpectedVersions` extended to 6 entries.

- `internal/api/handlers_analytics_unified_test.go`:
  - `TestAnalyticsMLRuns_EmptyResult_Returns200`
  - `TestAnalyticsMLRuns_DBError_Returns500`
  - `TestAnalyticsMLRuns_QuerySelectsFromWorkflowOutcomes` —
    guards against a future regression back to
    events-group-by.
  - `TestAnalyticsMLRuns_LimitClamped` — 10 000 → 500.

## Deferred

- **Dashboard ML Runs panel.** The REST endpoint is live and
  the E2E spec asserts against it, but the Analytics dashboard
  component doesn't yet render a dedicated panel for
  `/api/analytics/ml-runs`. Feature 40b picks that up — purely
  UX, no data-layer changes.
- **Tags on Workflow submission.** `WorkflowCompletedWithCounts`
  accepts a tags map but the Workflow struct doesn't yet carry
  Tags (only Jobs do). The coordinator passes nil for now; a
  future feature that adds `Workflow.Tags` can just populate
  the map without touching the event constructor.
- **Durations.** The `workflow_outcomes` table does not yet
  carry `started_at` or `duration_ms`. The first-job-submitted
  timestamp isn't in the workflow lifecycle's hot path today,
  and inferring it from the events table would reintroduce the
  expensive join the denormalised table is trying to avoid.
  Feature 40c: thread the workflow's `StartedAt` into the
  enriched event payload and add the `duration_ms` column.

## Acceptance criteria

1. `examples/ml-mnist/workflow.yaml` contains a 5-job DAG
   where `train_light` and `train_heavy` have no `depends_on`
   edge between them, and each targets a different runtime via
   `node_selector`.
2. The coordinator emits `workflow.completed` events carrying
   `job_count`, `success_count`, `failed_count`, and
   `owner_principal` on terminal transitions.
3. `SELECT * FROM workflow_outcomes WHERE workflow_id = $1`
   returns exactly one row per workflow run after
   `workflow.completed` fires.
4. `GET /api/analytics/ml-runs` returns that row as a JSON
   object with the typed `MLRunRow` fields.
5. `E2E_RECORD_MNIST_PARALLEL=1 E2E_VIDEO=1 npx playwright
   test ml-mnist-parallel-walkthrough` records a ~90–120 s
   video that goes DAG builder → Nodes → Jobs (parallel
   dispatch visible) → DAG detail → Models (winner badge) →
   Analytics assertion.
