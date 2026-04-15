# Deferred: Pipelines list filter — workflows that produced a registered model

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 18 — ML dashboard module](../implemented/18-ml-dashboard-module.md)

## Context

Feature 18's Pipelines view was specified as "a workflow list **filtered to those that produced a registered model**, with a small DAG visualisation showing artifact flow on edges." The DAG view shipped (deferred/24/implemented) but the **filter** is not implemented — `/ml/pipelines` shows the full workflow list and the operator clicks through to each one to see lineage.

## Why deferred

The naive client-side implementation needs N round-trips: fetch the workflow list, then call `/workflows/{id}/lineage` per workflow to check whether any of its jobs has a non-empty `models_produced` slice. That's exactly the N+1 pattern the lineage endpoint was designed to avoid for the detail view.

The clean implementation is a coordinator-side query parameter:

```
GET /workflows?has_registered_model=true
```

Which would walk the registry's models, collect distinct `source_job_id` values, look up the workflow ID for each, and return only workflows whose IDs are in that set. Cost is one model-store scan + one workflow-store filter per request, identical in shape to the existing `/workflows` paginate logic.

The reason this isn't done: the unfiltered list works, the value-add is "less scrolling for ops users with many workflows," and there is no operator yet asking for it. Shipping the filter without an operator's pain signal is design-by-anticipation.

## Revisit trigger

- An operator running a real cluster reports the unfiltered Pipelines list is unhelpful at scale (a working ML cluster will produce many model-less infra workflows that crowd out the train→register→serve pipelines).
- Or: step 19 (iris end-to-end demo) lands and the demo writeup wants to point the reader at "the pipelines that produced a model" — that's the natural moment to add the filter.

When triggered, the implementation is roughly:

1. Add `has_registered_model` query param to `handleListWorkflows`.
2. Inside the handler, when the flag is set, walk `models.ListBySourceJob` for each running workflow's job IDs (or invert: walk all models, collect source_job_ids, intersect).
3. Surface the filter as a checkbox on the Pipelines list view.

Three-line backend change + two-line frontend change once the primitive is there.
