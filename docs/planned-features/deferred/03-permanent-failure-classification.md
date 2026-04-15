# Deferred: Permanent failure classification

**Priority:** P2
**Status:** Deferred
**Originating feature:** [feature 02 — retry / failure policies](../02-retry-failure-policies.md)

## Context

Distinguish transient failures (node unreachable, OOM) from permanent failures (command not found, permission denied) to avoid wasting retries:

| Failure type | Retry? | Signal |
|-------------|--------|--------|
| Transient | Yes | Node unreachable, timeout, OOM |
| Permanent | No | Exit code 127 (not found), 126 (permission denied) |
| Unknown | Yes (with limit) | Non-zero exit code |

## Why deferred

All failures are currently retryable up to `max_attempts`. Classification requires inspecting exit codes and error messages from the runtime.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
