# Deferred: ML Pipelines DAG visualization

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 18 — ML dashboard module](../18-ml-dashboard-module.md)

## Context

Feature 18's spec calls for a Pipelines view: a workflow list filtered to those that produced a registered model, with a small DAG visualisation showing artifact flow on the edges (not just dependency arrows). Feature 18 shipped with the Datasets, Models, and Services views but **not** the Pipelines view.

## Why deferred

The DAG visualisation pulls in real complexity that the rest of feature 18 doesn't:

- A graph layout library (dagre-d3, cytoscape, mermaid) — meaningful bundle size and a learning-curve dependency the rest of the dashboard avoids.
- A new join in the data model: workflow → jobs → resolved artifact outputs → model registry entries that point back at the source job. The data is all there, but stitching it client-side requires N+1 queries to `/api/jobs/{id}` for each workflow's job; a clean implementation needs a coordinator-side endpoint that materialises the join (`GET /api/workflows/{id}/lineage`) so the dashboard isn't fan-out-heavy.
- Visualisation polish (edge labels for artifact names, hover tooltips for sizes/checksums, click-to-job navigation) — substantial UI work that, until step 19's iris demo lands, has no concrete user flow to anchor decisions against.

The other three views (Datasets / Models / Services) cover the operator's day-to-day need: "what's registered" / "what's serving" / "what broke". The Pipelines view is the ML-team-PM view: "show me the value chain of this model." It's worth building, but it's worth building well, and the current backend doesn't expose the lineage join cheaply enough to do it without N+1 fan-out.

## Revisit trigger

- Step 19 (iris end-to-end demo) lands and the iris workflow becomes the obvious smoke test for the Pipelines view design.
- Or: an operator asks "which workflow produced this model in production?" frequently enough that the registry's `source_job_id` plus a manual `GET /api/jobs/{id}` becomes painful — that's the signal the join needs a first-class endpoint and a UI to consume it.

When triggered, the implementation order should be:

1. Coordinator: add `GET /api/workflows/{id}/lineage` that walks the workflow's jobs, resolves their `ResolvedOutputs`, and joins against `/api/models?source_job_id=...` (or extends the model store with a `ListBySourceJob` accessor).
2. Dashboard: add `MlPipelinesComponent` consuming that endpoint with a graph layout library of choice.
3. Add the Pipelines link to the shell nav alongside Datasets / Models / Services.
