# Deferred: Workflow re-run from failed step

**Priority:** P2
**Status:** Deferred
**Originating feature:** [feature 01 — workflow / DAG](../01-workflow-dag.md)

## Context

Allow restarting a failed workflow from the step that failed, skipping already-completed jobs:

```
POST /workflows/{id}/retry?from=test
```

## Why deferred

Requires tracking per-job completion state history and selective re-dispatch.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
