# Feature: ML Dashboard Module

**Priority:** P1
**Status:** Pending
**Affected files:** `dashboard/src/app/features/ml/` (new module).
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## Dashboard: ML module

New lazy-loaded Angular module at `dashboard/src/app/features/ml/`:

- **Datasets** view — list, tag filter, register-via-upload modal, delete.
- **Models** view — list, lineage column (links to source job + dataset),
  metrics column.
- **Pipelines** view — workflow list filtered to those that produced a
  registered model, with a small DAG visualisation showing artifact flow
  on edges (not just dependency arrows).
- **Services** view — currently-serving models, upstream URL, last health
  status.

Reuses the existing auth-guard, JWT interceptor, error banner, and date
range patterns from the analytics module.

### Step-3 follow-up to pick up during this step

The [step-3](13-ml-workflow-artifact-passing.md) dispatch-time resolver fails closed on
`ErrResolveUpstreamNotCompleted` / `ErrResolveOutputMissing` — the
downstream job transitions to Failed with an `"artifact resolution:
..."` error. These are currently `slog.Warn`-logged but not audited,
so an operator cannot answer "which of today's ML pipelines broke
because an upstream output went missing?" without reading raw
coordinator logs. Emit distinct audit events from
`DispatchLoop.dispatchPending` whenever `ResolveJobInputs` returns
non-nil, and add a filter + column to the Pipelines view so the
dashboard can surface them at a glance:

| Event | Actor | Target | Details |
|---|---|---|---|
| `ml.resolve_failed` | `coordinator` | `job:<job_id>` | `{workflow_id, upstream, output_name, reason}` |

One emit site in `dispatch.go` plus an `AuditEventType` constant; a
small badge on the Pipelines row keyed off the event.

### Step-4 follow-up to pick up during this step

The `job.unschedulable` event today carries `job_id` +
`unsatisfied_selector` — enough for an operator to see *what* didn't
match, but not *why*. The dashboard's Pipelines view would benefit
from distinguishing the three causes so a user can act on them
differently:

| Diagnostic | Meaning | Operator action |
|---|---|---|
| `no_healthy_node` | zero nodes in the registry are healthy right now | wait / investigate registry |
| `no_matching_label` | healthy nodes exist but none advertise the requested labels | add a node with the right labels / relax the selector |
| `all_matching_unhealthy` | nodes with matching labels exist but are all stale | restart the affected nodes |

Extend `JobUnschedulable` with a `reason` field and populate it from
`DispatchLoop.dispatchPending` using three paths: the existing
`ErrNoHealthyNodes` branch, the existing `ErrNoNodeMatchesSelector`
branch, and a new check that walks *all* registered nodes (healthy
or not) and reports `all_matching_unhealthy` when at least one
*unhealthy* node matches the selector. The dashboard's Pipelines
row can then render three distinct badge colours / tooltips keyed
off `reason`.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| New client surface | Inherits dashboard's existing security contract (§9): in-memory JWT, auth interceptor, auth guard on `/ml`, CSP same-origin. Artifact links open through a signed-URL endpoint, never as raw `s3://` URIs | — |

Threat additions handled here:

| Threat | Mitigation |
|---|---|
| Dashboard leaking artifact URIs via `Referer` / access log | Signed-URL-first access pattern; URIs only rendered inside authenticated session state, never as GET-request query strings |
