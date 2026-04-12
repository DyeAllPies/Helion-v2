# Feature: Job State Machine Improvements

**Priority:** P1
**Status:** Partial — 7 states exist, missing `scheduled` and `retrying`
**Affected files:** `proto/coordinator.proto`, `internal/cluster/job.go`, `internal/cluster/job_transition.go`

## Problem

The current state machine works but has gaps:

1. No `scheduled` state — jump from `pending` to `dispatching` conflates "queued" with "assigned to a node"
2. No `retrying` state — failed jobs cannot be distinguished from jobs awaiting retry
3. No `cancelled` state — no way to cancel a job through the API
4. No `skipped` state — needed for DAG workflows when upstream fails

## Current state machine

```
pending → dispatching → running → completed
                                → failed
                                → timeout
          dispatching → failed (dispatch RPC error)
any non-terminal → lost (crash recovery only)
```

Valid transitions (`job_transition.go:allowedTransitions`):
- pending → dispatching
- dispatching → running, failed
- running → completed, failed, timeout
- Any non-terminal → lost (via MarkLost)

## Proposed state machine

```
pending → scheduled → dispatching → running → completed
                                            → failed → retrying → pending
                                            → timeout → retrying → pending
                                            → cancelled
          scheduled → cancelled
pending → cancelled
pending → skipped (DAG: upstream failed)
any non-terminal → lost (crash recovery)
```

### New states

| State | Meaning | Entry condition |
|-------|---------|----------------|
| `scheduled` | Assigned to a node, awaiting dispatch RPC | Scheduler picked a target node |
| `retrying` | Failed, waiting for backoff to expire | Attempt < max_attempts |
| `cancelled` | Explicitly cancelled by user/system | API call or workflow cancellation |
| `skipped` | Not executed because upstream dependency failed | DAG dependency resolution |

### Updated transition table

```go
var allowedTransitions = map[cpb.JobStatus][]cpb.JobStatus{
    cpb.JOB_STATUS_PENDING:      {cpb.JOB_STATUS_SCHEDULED, cpb.JOB_STATUS_CANCELLED, cpb.JOB_STATUS_SKIPPED},
    cpb.JOB_STATUS_SCHEDULED:    {cpb.JOB_STATUS_DISPATCHING, cpb.JOB_STATUS_CANCELLED, cpb.JOB_STATUS_PENDING},
    cpb.JOB_STATUS_DISPATCHING:  {cpb.JOB_STATUS_RUNNING, cpb.JOB_STATUS_FAILED},
    cpb.JOB_STATUS_RUNNING:      {cpb.JOB_STATUS_COMPLETED, cpb.JOB_STATUS_FAILED, cpb.JOB_STATUS_TIMEOUT, cpb.JOB_STATUS_CANCELLED},
    cpb.JOB_STATUS_FAILED:       {cpb.JOB_STATUS_RETRYING},
    cpb.JOB_STATUS_TIMEOUT:      {cpb.JOB_STATUS_RETRYING},
    cpb.JOB_STATUS_RETRYING:     {cpb.JOB_STATUS_PENDING},
}
```

Terminal states (no outgoing transitions): `completed`, `cancelled`, `skipped`, `lost`.

### Proto changes

```protobuf
enum JobStatus {
  JOB_STATUS_UNSPECIFIED = 0;
  JOB_STATUS_PENDING = 1;
  JOB_STATUS_DISPATCHING = 2;
  JOB_STATUS_RUNNING = 3;
  JOB_STATUS_COMPLETED = 4;
  JOB_STATUS_FAILED = 5;
  JOB_STATUS_TIMEOUT = 6;
  JOB_STATUS_LOST = 7;
  JOB_STATUS_SCHEDULED = 8;   // new
  JOB_STATUS_RETRYING = 9;    // new
  JOB_STATUS_CANCELLED = 10;  // new
  JOB_STATUS_SKIPPED = 11;    // new
}
```

### Job cancellation

New API endpoint: `POST /jobs/{id}/cancel`

- Transitions job to `cancelled` from any non-terminal state
- If job is `running`, sends `Cancel` RPC to the assigned node
- If job is `pending` or `scheduled`, transitions immediately
- If job is already terminal, returns 409 Conflict

### Scheduled vs dispatching

Splitting these states provides better observability:

- `scheduled` — coordinator has picked a node but hasn't sent the RPC yet
- `dispatching` — RPC is in flight

This lets operators distinguish "waiting for dispatch slot" from "network call in progress" and helps debug slow dispatches.

## Migration

The new states use proto values 8-11, which are backward-compatible:
- Old coordinators ignore unknown enum values
- New coordinators handle both old (without scheduled/retrying) and new states
- Existing jobs in BadgerDB keep their current states — no data migration needed

## Implementation order

1. Proto: add new enum values (backward compatible)
2. Transition table: add new allowed transitions
3. Cancel API endpoint + node cancel RPC integration
4. Scheduled state: insert between pending and dispatching in dispatch loop
5. Retrying state: integrate with retry policy (see 02-retry-failure-policies.md)
6. Skipped state: integrate with DAG support (see 01-workflow-dag.md)
7. Dashboard: update job status display with new states
