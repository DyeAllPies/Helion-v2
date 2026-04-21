# Feature: Priority Queues

**Priority:** P1
**Status:** Implemented
**Affected files:** `internal/proto/coordinatorpb/types.go`, `internal/cluster/priority.go`, `internal/cluster/dispatch.go`, `internal/cluster/job_submit.go`, `internal/api/handlers_jobs.go`

## Problem

All pending jobs are dispatched in arbitrary order (BadgerDB scan order). There is no way to express that some jobs are more important than others. In a busy cluster, a critical job waits behind hundreds of low-priority batch jobs.

## Current state

- No `priority` field on Job proto
- `DispatchLoop.dispatchPending()` calls `store.Pending()` which returns all pending jobs
- Dispatch order is determined by BadgerDB key iteration (effectively insertion order)
- No preemption mechanism

## Design

### Priority levels

Simple numeric priority with named tiers:

```protobuf
message Job {
  // ... existing fields ...
  uint32 priority = 16;  // 0 (lowest) to 100 (highest), default: 50
}
```

| Range | Name | Use case |
|-------|------|----------|
| 90-100 | Critical | Incident response, emergency fixes |
| 70-89 | High | User-facing production jobs |
| 40-69 | Normal | Default, standard workloads |
| 20-39 | Low | Batch processing, background jobs |
| 0-19 | Best-effort | Optional work, can be starved |

### Dispatch ordering

Replace flat pending list with priority-sorted dispatch:

```go
func (d *DispatchLoop) dispatchPending(ctx context.Context) {
    jobs := d.store.PendingByPriority() // sorted descending by priority, then by created_at (FIFO within same priority)

    for _, job := range jobs {
        // ... existing dispatch logic (backoff check, DAG eligibility, etc.) ...
    }
}
```

`PendingByPriority()` implementation options:
1. **BadgerDB secondary index** — store pending jobs with key `pending:<priority_inverted>:<created_at>:<id>` for natural sort order
2. **In-memory sort** — fetch all pending, sort in Go (simpler, fine for <10k pending jobs)

Start with option 2 (in-memory sort). Move to secondary index if pending queue exceeds 10k jobs regularly.

### Starvation prevention

Without safeguards, low-priority jobs can starve indefinitely. Two mechanisms:

#### 1. Age-based priority boost

Jobs that have been pending too long get their effective priority boosted:

```go
func effectivePriority(job Job) uint32 {
    age := time.Since(job.CreatedAt)
    boost := uint32(age.Minutes()) // +1 priority per minute pending
    eff := job.Priority + boost
    if eff > 100 {
        return 100
    }
    return eff
}
```

#### 2. Minimum dispatch ratio

Reserve a fraction of dispatch slots for lower priorities:

- 80% of dispatch capacity serves highest-priority jobs first
- 20% reserved for jobs that have been pending longest (regardless of priority)

This is configurable via `HELION_PRIORITY_RESERVE_PCT` (default: 20).

### Workflow priority

Workflows set a default priority for all their jobs. Individual jobs within a workflow can override:

```json
{
  "id": "pipeline-1",
  "priority": 70,
  "jobs": [
    { "name": "build", "priority": 90 },
    { "name": "test" },
    { "name": "deploy" }
  ]
}
```

Here `build` gets priority 90, while `test` and `deploy` inherit 70 from the workflow.

### Preemption (deferred)

True preemption (killing a low-priority running job to free capacity for a high-priority one) is complex and risky. Defer this entirely. The priority system only affects dispatch ordering, not running jobs.

## API changes

`POST /jobs` accepts optional `priority` (uint32, 0-100, default 50):

```json
{
  "id": "urgent-fix",
  "command": "deploy",
  "args": ["--hotfix"],
  "priority": 95
}
```

`GET /jobs` gains `sort=priority` query parameter.

## Implementation order

1. Proto: add `priority` field to Job
2. `job_submit.go`: validate priority range (0-100), default to 50
3. `PendingByPriority()`: in-memory sort (priority desc, created_at asc)
4. Dispatch loop: use sorted list
5. Age-based priority boost
6. Starvation reserve percentage
7. Dashboard: show priority in job list, color-code by tier

## Open questions

- Should priority be mutable after submission? (Deferred — `PATCH /jobs/{id}`)
- Per-user priority limits? (Deferred — requires user/tenant model)
- Minimum dispatch ratio / starvation reserve? (Deferred — age boost is sufficient for now)

## Implementation status

1. **Job priority field** — `Priority uint32` on Job (0-100, default 50 set in Submit).
2. **EffectivePriority** — `priority.go`: age-based boost (+1/min pending, capped at 100).
3. **PendingByPriority** — sorted descending by effective priority, FIFO within same priority.
4. **Dispatch loop** — uses `PendingByPriority()` instead of `List()` (random map order).
5. **API** — `POST /jobs` accepts optional `priority` (0-100, validated). `GET /jobs/{id}` returns priority in response.
6. **Workflow priority** — `Workflow.Priority` sets default for all jobs. `WorkflowJob.Priority` overrides per-job. Inherited in `WorkflowStore.Start()`.
7. **Dashboard** — PRI column in job list with color coding (red >=70, dim <30).
8. **Tests** — 7 priority unit tests, 3 API tests, 1 E2E integration test.
