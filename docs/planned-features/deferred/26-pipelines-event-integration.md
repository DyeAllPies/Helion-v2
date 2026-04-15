# Deferred: Pipelines view event integration

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 18 — ML dashboard module](../implemented/18-ml-dashboard-module.md)

## Context

Feature 18's step-3 and step-4 follow-ups added two distinct events on the coordinator side and called for **dashboard integration on the Pipelines view**:

- `ml.resolve_failed` — emitted when the dispatch loop's artifact resolver fails. The original spec said: "add a filter + column to the Pipelines view so the dashboard can surface them at a glance."
- `job.unschedulable` (with `reason`) — extended with a stable reason field. The original spec said: "The dashboard's Pipelines row can then render three distinct badge colours / tooltips keyed off `reason`."

The events themselves are emitted, audited, and visible on the existing `/events` view. The **Pipelines-view-side surfacing** is what's deferred:

- No badge on the Pipelines list row indicating "this workflow has a resolve failure" or "this workflow has unschedulable jobs."
- No filter on the Pipelines list to narrow to broken pipelines.
- No tooltip on the Pipelines DAG nodes showing the unschedulable reason.

## Why deferred

The events are recorded; what's missing is a dashboard query path that takes a workflow ID and returns "do any of this workflow's jobs have an open `ml.resolve_failed` or `job.unschedulable` event?" The naive implementation walks `/audit?workflow_id=X` per workflow on the list page — N+1 fan-out, exactly the pattern the lineage endpoint was designed to avoid.

The clean implementation is one of:

1. **Server-side join into the lineage response.** Extend `WorkflowLineage.Jobs[i]` with an optional `pending_diagnostic` field populated from a quick scan of recent `ml.resolve_failed` / `job.unschedulable` events. Adds a per-workflow audit-store scan to the lineage handler — fine if bounded, expensive otherwise.
2. **Server-side workflow-state denormalisation.** Track `pending_diagnostics` per workflow in BadgerDB, updated on event emit. Faster reads, more state to keep consistent.
3. **Client-side WebSocket subscription.** The dashboard subscribes to the `/ws/events` stream (already exists from feature 06) and holds a per-workflow diagnostic map in memory. Reactive, cheap on the server, but the dashboard has to be open to know — closed-tab notifications don't survive.

Option (1) is the simplest extension to the existing lineage handler and the easiest to test. Option (3) is the lowest-server-cost option for an always-open ops dashboard. The choice depends on how operators actually use the Pipelines view (open + watch vs. dip in to triage), and that signal will come from step 19 (iris demo) when the view starts seeing real workflows.

## Revisit trigger

- Step 19 (iris end-to-end demo) ships and operators using it report "I see ml.resolve_failed in the events feed but I have to manually correlate to the workflow."
- Or: the registry-event volume from `/events` becomes loud enough that operators stop watching it, and they need a per-workflow summary instead.

When triggered, default to option (1) — extend the lineage response. It's the smallest scope change and the integration tests live next door to the existing lineage tests.
