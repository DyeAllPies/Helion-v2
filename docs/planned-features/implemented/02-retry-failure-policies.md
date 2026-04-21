# Feature: Retry + Failure Policies

**Priority:** P0
**Status:** Implemented
**Affected files:** `internal/proto/coordinatorpb/types.go`, `internal/cluster/retry.go`, `internal/cluster/job_retry.go`, `internal/cluster/job.go`, `internal/cluster/dispatch.go`, `internal/api/handlers_jobs.go`

## Problem

Today, failed dispatches re-enter the pending pool and retry on the next tick (~1s) with no limit. This causes:

1. **Thundering herd** — all failed jobs retry simultaneously on the same interval
2. **Infinite retry** — a consistently failing job retries forever, wasting resources
3. **No per-job control** — users cannot set retry count, backoff, or timeout per job
4. **No distinction** between transient failures (node down) and permanent failures (bad command)

## Current state

- `DispatchLoop.dispatchPending()` dispatches all pending jobs each tick
- Failed dispatch transitions job to `failed` immediately (no retry)
- `RecoveryManager` handles crash recovery (re-dispatches non-terminal jobs) but not runtime retries
- No `retry_count`, `max_retries`, or `backoff` fields on Job proto

## Design

### Retry policy (per-job, optional)

```protobuf
message RetryPolicy {
  uint32 max_attempts = 1;       // default: 1 (no retry). Total attempts, not retries.
  BackoffStrategy backoff = 2;   // default: EXPONENTIAL
  uint32 initial_delay_ms = 3;   // default: 1000 (1s)
  uint32 max_delay_ms = 4;       // default: 60000 (60s)
  bool jitter = 5;               // default: true (add random jitter to delay)
}

enum BackoffStrategy {
  BACKOFF_NONE = 0;       // fixed delay (initial_delay_ms every time)
  BACKOFF_LINEAR = 1;     // delay increases by initial_delay_ms each attempt
  BACKOFF_EXPONENTIAL = 2; // delay doubles each attempt (capped at max_delay_ms)
}
```

### Job-level retry tracking

Add fields to the `Job` proto:

```protobuf
message Job {
  // ... existing fields ...
  RetryPolicy retry_policy = 13;
  uint32 attempt = 14;            // current attempt number (1-indexed)
  google.protobuf.Timestamp retry_after = 15; // earliest time to retry
}
```

### State machine changes

Add a `retrying` state between `failed` and `pending`:

```
pending → dispatching → running → completed
                                → failed → retrying → pending (re-dispatch)
                                         → failed (max attempts reached)
                                → timeout → retrying → pending
                                          → timeout (max attempts reached)
```

New transitions:
- `failed → retrying` — when `attempt < max_attempts`
- `timeout → retrying` — when `attempt < max_attempts`
- `retrying → pending` — when `now >= retry_after` (dispatch loop checks this)

### Backoff calculation

```go
func nextDelay(policy RetryPolicy, attempt uint32) time.Duration {
    base := time.Duration(policy.InitialDelayMs) * time.Millisecond
    switch policy.Backoff {
    case BACKOFF_NONE:
        delay = base
    case BACKOFF_LINEAR:
        delay = base * time.Duration(attempt)
    case BACKOFF_EXPONENTIAL:
        delay = base * time.Duration(1<<(attempt-1))
    }
    if delay > time.Duration(policy.MaxDelayMs)*time.Millisecond {
        delay = time.Duration(policy.MaxDelayMs) * time.Millisecond
    }
    if policy.Jitter {
        delay += time.Duration(rand.Int63n(int64(delay) / 4)) // 0-25% jitter
    }
    return delay
}
```

### Dispatch loop changes

```go
func (d *DispatchLoop) dispatchPending(ctx context.Context) {
    jobs := d.store.Pending()
    now := time.Now()

    for _, job := range jobs {
        // Skip jobs in backoff window
        if job.RetryAfter != nil && now.Before(job.RetryAfter.AsTime()) {
            continue
        }
        // ... existing dispatch logic ...
    }
}
```

### Failure classification (future enhancement)

Eventually distinguish failure types to decide retry behavior:

| Failure type | Retry? | Example |
|-------------|--------|---------|
| Transient | Yes | Node unreachable, timeout, OOM |
| Permanent | No | Command not found, permission denied |
| Unknown | Yes (with limit) | Non-zero exit code |

For now, all failures are retryable up to `max_attempts`. Permanent failure classification can be added later by inspecting exit codes and error messages.

## API changes

`POST /jobs` request body gains optional `retry_policy`:

```json
{
  "id": "build-123",
  "command": "make",
  "args": ["build"],
  "retry_policy": {
    "max_attempts": 3,
    "backoff": "EXPONENTIAL",
    "initial_delay_ms": 2000,
    "max_delay_ms": 30000,
    "jitter": true
  }
}
```

`GET /jobs/{id}` response includes `attempt` and `retry_after`.

## Implementation order

1. Proto changes (RetryPolicy message, new Job fields)
2. Backoff calculation (pure function, easy to unit test)
3. State machine: add `retrying` state + transitions
4. Dispatch loop: skip jobs in backoff window
5. Job transition: on failure, check retry policy and set `retry_after`
6. API: accept `retry_policy` on job submission
7. Audit: log retry events

## Defaults

- If no `retry_policy` specified: `max_attempts = 1` (no retry, current behavior)
- Workflow jobs inherit a default policy from the workflow if not specified per-job
