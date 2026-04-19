# Feature: Job stdout/stderr secret scrubbing

**Priority:** P2
**Status:** Pending
**Affected files:**
`internal/logstore/` (new scrubber layer on the write path),
`internal/nodeserver/` (optional: reject submissions that wire
stdout-visible secrets),
`docs/SECURITY.md` (threat row).

## Problem

Feature 26 redacts secret env values on every coordinator REST path
and gates plaintext read-back behind
`POST /admin/jobs/{id}/reveal-secret`. But once a job is dispatched,
nothing stops the job itself from printing its own secret to
stdout:

```python
import os
print(f"Using HF_TOKEN={os.environ['HF_TOKEN']}")  # leaks to logstore
```

The coordinator's `logstore` currently persists stdout/stderr bytes
verbatim. Every operator with `GET /jobs/{id}/logs` permission can
then read the token out of the log viewer — which is the same
attack feature 26 redacts at the env-response layer. The spec's
"Not attempting" section acknowledged this; it's being promoted to
a planned item so the gap closes rather than drifting indefinitely.

The attack surface is operator-level, not attacker-level: a
malicious job script can leak its own token, and nothing the
coordinator does mitigates that. But *accidental* leaks ("debug
print left in"), which are the common case, can be caught with a
submit-time safety net on the write path of the log store.

## Current state

- `logstore.Store.Append(jobID, chunk)` writes verbatim bytes
  into BadgerDB under `logs/{jobID}/{seq}`. No content inspection.
- `GET /jobs/{id}/logs` returns all chunks concatenated in order.
  No redaction at response time.
- The WebSocket stream `/ws/jobs/{id}/logs` also emits verbatim.

The job's `SecretKeys + Env` are available on the `cpb.Job` record;
the logstore already knows the jobID for each chunk and could fan
out to look up the per-job list at write time.

## Design

Two sub-features, roughly in increasing order of cost and value:

### 1. Write-path scrubbing (the common case)

A decorator over `logstore.Store.Append` that, for each incoming
chunk, replaces every occurrence of any secret env VALUE with the
string `[REDACTED]`. Applied once at write, so every read path (HTTP
and WS) automatically sees the scrubbed form.

```go
type ScrubbingStore struct {
    inner  logstore.Store
    lookup func(jobID string) (secrets []string, ok bool) // reads from JobStore
}

func (s *ScrubbingStore) Append(ctx context.Context, jobID string, chunk []byte) error {
    if secrets, ok := s.lookup(jobID); ok && len(secrets) > 0 {
        for _, v := range secrets {
            chunk = bytes.ReplaceAll(chunk, []byte(v), []byte("[REDACTED]"))
        }
    }
    return s.inner.Append(ctx, jobID, chunk)
}
```

Chunks cross the wire in configurable sizes; secrets could land
astride a boundary. Two mitigations:

- **Use the job's Env values directly,** not a synthetic regex. An
  Env value is a single contiguous string at the source; boundary
  concerns only arise if the job rewrites it.
- **Accept the miss rate.** A boundary miss is rare and is always
  the job's own doing; the write-path scrubber is an accidental-
  leak catcher, not a determined-attacker mitigation.

### 2. Read-path redaction (belt-and-braces)

Same substitution, applied at `GET /jobs/{id}/logs` response-build
time and at WebSocket fan-out. Catches cases where the value was
already written before the job's SecretKeys list was known (edge
case during a mid-stream Submit that updates an existing record).

Not strictly necessary if write-path scrubbing is in place, but
cheap and defence-in-depth.

## Security plan

| Threat | Control |
|---|---|
| Job prints its own secret env value to stdout; operator reads via /ws/jobs/{id}/logs | Write-path scrubber replaces the VALUE bytes with "[REDACTED]" on every chunk that lands in the logstore. Response-path redactor is a second pass for already-written chunks. |
| Scrubber adds perf cost to every chunk | Only active when `len(job.SecretKeys) > 0` — regular jobs with no secrets short-circuit on the first check. Bounded: O(chunks × secrets), and secrets are capped at 32 per job. |
| Operator reveals secret via admin endpoint and pastes into a job env (secondary leak) | Out of scope — the admin who pastes a revealed value back into another job is responsible for handling it. Audit event shows the reveal. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `scrubStoredSecrets` pure helper + unit tests (inputs with and without secret boundaries). | — | Small |
| 2 | `ScrubbingStore` decorator + integration with coordinator wiring. | 1 | Small |
| 3 | Response-path redactor on `GET /jobs/{id}/logs`. | 1 | Small |
| 4 | WebSocket stream redaction on `/ws/jobs/{id}/logs`. | 1 | Small |
| 5 | SECURITY.md row + operator guidance (a short `docs/job-authoring.md` note: "do not echo your own `$HELION_TOKEN`"). | 2 | Trivial |

## Tests

- `TestScrubStoredSecrets_SingleValue` — input contains the secret
  value; output contains `[REDACTED]` exactly once.
- `TestScrubStoredSecrets_MultipleValues` — two secrets, each in
  two places.
- `TestScrubStoredSecrets_ValueNotPresent_NoMutation` — regression
  guard: a secret that the job never printed must not appear in
  the output.
- `TestScrubStoredSecrets_EmptyValue_Ignored` — zero-length secret
  must not cause a match-every-position pathology.
- `TestScrubbingStore_Append_UsesJobStoreLookup` — decorator
  consults the jobStore-backed lookup and applies scrubbing only
  when the result has secrets.
- Integration: submit a job with an env `{HF_TOKEN: "hf_sekret"}`,
  append a chunk via the logstore that contains `hf_sekret`,
  fetch via GET, assert the response body does NOT contain
  `hf_sekret` and DOES contain `[REDACTED]`.

## Deferred

- **Regex-based sensitive-string detection** (catch secrets the
  submitter didn't declare, e.g. AWS access key IDs by shape).
  Fancy regexes get fooled and false-positive on legitimate job
  output; out of scope for this slice.

## Implementation status

_Not started. Promoted from feature 26's "Not attempting" section
on 2026-04-19 so the gap has a planning target._
