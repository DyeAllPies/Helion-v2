# Deferred: Mutable priority after submission

**Priority:** P2
**Status:** Deferred
**Originating feature:** [feature 05 — priority queues](../05-priority-queues.md)

## Context

Allow changing a pending job's priority via `PATCH /jobs/{id}`:

```json
PATCH /jobs/{id}
{ "priority": 95 }
```

Only permitted for jobs in `pending` or `scheduled` state.

## Why deferred

Requires a new API endpoint and validation that only non-dispatched jobs can be modified.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
