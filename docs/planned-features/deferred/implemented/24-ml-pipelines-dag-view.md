# Implemented: ML Pipelines DAG view (formerly deferred/24)

**Priority:** P3 (at the time of deferral)
**Status:** **Implemented** — moved out of the active backlog.
**Originating feature:** [feature 18 — ML dashboard module](../../implemented/18-ml-dashboard-module.md)
**Implementation landed in:** the commit that introduced the
`docs/planned-features/deferred/implemented/` folder — see the
working tree's `ml-pipeline` branch history for the exact SHA.

---

## Original deferral context

Feature 18's spec called for a Pipelines view: a workflow list filtered to those that produced a registered model, with a small DAG visualisation showing artifact flow on the edges (not just dependency arrows). Feature 18 shipped with the Datasets, Models, and Services views but **not** the Pipelines view.

The original deferral rationale was:

- A graph layout library (dagre-d3, cytoscape, mermaid) — meaningful bundle size and a learning-curve dependency the rest of the dashboard avoids.
- A new join in the data model: workflow → jobs → resolved artifact outputs → model registry entries. The data is all there, but stitching it client-side requires N+1 queries to `/api/jobs/{id}` for each workflow's job; a clean implementation needs a coordinator-side endpoint that materialises the join.
- Visualisation polish (edge labels for artifact names, hover tooltips for sizes/checksums, click-to-job navigation) — substantial UI work that, until step 19's iris demo lands, had no concrete user flow to anchor decisions against.

## What actually landed

The slice addressed the two structural concerns (join + lib) head-on and shipped a minimal-but-honest DAG view:

### Backend

- **`registry.ModelStore.ListBySourceJob(ctx, sourceJobID)`** —
  new method on the interface; `BadgerStore` implementation walks
  the model prefix and filters. Linear scan; acceptable at MVP
  scale, documented where to add a secondary index if model counts
  grow past the low thousands.
- **`cluster.BuildWorkflowLineage(ctx, wfID, workflows, jobs, models)`** —
  pure join helper in `internal/cluster/workflow_lineage.go`.
  Walks the workflow's `Jobs`, resolves each against the JobStore
  for live status + `ResolvedOutputs`, and joins against the
  model store by `SourceJobID`. Second pass emits `ArtifactEdge`
  records for `Inputs[i].From` references so the dashboard can
  differentiate pure `depends_on` arrows from artifact-flow arrows.
  `ModelReader` argument is allowed to be nil — a coordinator
  without the registry wired still serves lineage with empty
  `models_produced` slices.
- **`GET /workflows/{id}/lineage`** — HTTP handler in
  `internal/api/handlers_workflows_lineage.go`. 404 when workflows
  are not wired or the workflow ID doesn't exist; 500 on a
  registry-store error. Single round-trip — no N+1 fan-out from
  the dashboard.
- Six Go tests covering the happy path, not-found, nil-model-store
  graceful degradation, unstarted (empty-JobID) jobs, malformed
  `From` refs, and model-store error propagation; plus an
  `internal/registry/badger_test.go` case for `ListBySourceJob`.

### Frontend

- **`dashboard/src/app/features/ml/ml-pipelines.component.ts`** —
  list view at `/ml/pipelines`. Re-uses the existing
  `getWorkflows` API; each row has a "View DAG" link. The
  "filtered to those that produced a registered model" bit from
  the original spec was dropped in favour of showing the full
  workflow list — the operator clicks through to see lineage.
  A cheaper filter would need a new
  `GET /workflows?produced_model=true` coordinator endpoint; left
  as a trivially-addable follow-up.
- **`dashboard/src/app/features/ml/ml-pipeline-detail.component.ts`** —
  detail view at `/ml/pipelines/:id`. Fetches lineage in one
  request and renders the DAG with **mermaid**, imported
  dynamically (`await import('mermaid')`) inside `renderDag()`
  so the ~200 KiB library is code-split away from the main
  bundle — only users opening a DAG download it.
- `buildMermaidSpec` is an exported pure function (separate from
  the async render path) so its node/edge emission is unit-
  testable without jsdom+mermaid plumbing. Dependency edges
  render as solid arrows; artifact edges render as dashed arrows
  with `output → input` labels.
- 19 new dashboard tests: 7 for the list component (init, load,
  errors, pagination, status-chip mapping) and 12 for the detail
  component + mermaid-spec builder (init, route-id parsing,
  errors, missing-id short-circuit, byte formatting, status
  mapping, flowchart header, node emission, solid/dashed arrows,
  identifier sanitization, empty-edge + solo-job edge cases).

### Nav + routing

- `/ml/pipelines` + `/ml/pipelines/:id` added to `app.routes.ts`
  as lazy-loaded standalone components.
- Sidebar in `shell.component.ts` grew a "Pipelines" link
  (`account_tree` icon) alongside Datasets / Models / Services.

### Deltas from the original plan

1. No `GET /workflows?produced_model=true` filter — showing the
   full workflow list with a "View DAG" link is the simpler,
   honest MVP. Adding the filter later is a one-handler change;
   not blocking the DAG view on it avoids design-by-anticipation.
2. Rendering uses mermaid (dynamic import) instead of dagre-d3.
   Lower integration burden, designed for exactly this
   text-spec-to-SVG flow, and the code-split keeps the main bundle
   light.
3. The list view doesn't pre-check which workflows produced models.
   Users click to see. If that becomes painful, a backend filter is
   the fix.

## What stayed out

- **Edge hover tooltips for artifact size + sha256.** The output
  name + input name label on each artifact edge is clear enough;
  sizes appear on the per-job cards below the DAG.
- **Click-to-job navigation from DAG nodes.** The per-job cards
  below the DAG are linkable; the mermaid SVG itself is not
  interactive. mermaid supports click handlers but wiring them
  through Angular's zone has enough sharp edges that the slice
  deferred this to a follow-up if operators ask for it.
- **Workflow-level filter "only those that produced a model".**
  Needs a coordinator-side query endpoint. Trivial to add when
  motivated; see deltas above.

If any of the three becomes painful in real use, the next step is a
small PR — not a design review — and the relevant file to touch is
`ml-pipeline-detail.component.ts` (first two) or a new branch in
`handlers_workflows.go` (third).
