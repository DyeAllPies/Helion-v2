# Feature: Job stdout/stderr secret scrubbing

**Priority:** P2
**Status:** Implemented (2026-04-20)
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

_Implemented 2026-04-20._

### What shipped

- `internal/logstore/scrub.go` — pure `Scrub(chunk, secrets)`
  helper + `ScrubbingStore` decorator + `SecretsLookup`
  function type. Empty-value DoS guard, zero-allocation
  fast path when no secret occurs in the chunk, idempotent
  double-scrub.
- Coordinator wiring (`cmd/helion-coordinator/main.go`):
  the raw `BadgerLogStore` is wrapped in `ScrubbingStore`
  with a JobStore-backed `SecretsLookup`. Same lookup is
  plumbed into `grpcserver.WithSecretsLookup` so the
  feature-28 `events.Bus → PG` mirror publishes redacted
  bytes too — PG cannot become a side-channel around the
  Badger scrub.
- `internal/api/handlers_logs.go` — response-path
  redactor on `GET /jobs/{id}/logs`. Applied as a second
  pass against chunks that landed before the decorator was
  wired (rolling-deploy edge case). Idempotent against
  already-scrubbed content.
- `internal/api/handlers_logs.go` also closes a
  feature-37 regression: `GET /jobs/{id}/logs` now gates
  on `authz.Allow(ActionRead, jobResource)`. Pre-29 the
  endpoint had no per-job RBAC and any authenticated
  caller could read any job's logs — a pre-existing gap
  from before features 36/37, not a new weakness, but the
  log-read path was the one remaining unchecked leaf now
  that features 36/37 had closed the rest of the
  per-resource surface.
- `internal/grpcserver/handlers.go` — `StreamLogs` scrubs
  once at handler-ingress, feeds the redacted bytes to
  both the Badger append and the bus publish. The
  `ScrubbingStore` decorator on the Badger side remains as
  defence-in-depth (idempotent re-scrub).

### Deviations from plan

- **WebSocket `/ws/jobs/{id}/logs` redaction was not wired.**
  The WS stream handler is a stub that returns
  `"not yet implemented"` with close code 1001 — there is no
  live byte path to redact. A future slice that implements
  real-time streaming will need to call the same
  `logstore.Scrub` on every frame before fan-out; the helper
  is already public.

### Tests added

- `internal/logstore/scrub_test.go`:
  - `TestScrub_SingleValue_ReplacedOnce`
  - `TestScrub_MultipleValues_AllReplaced`
  - `TestScrub_ValueNotPresent_NoMutation` — zero-allocation
    guard on the hot path.
  - `TestScrub_EmptyValue_Ignored` — empty-value DoS guard.
  - `TestScrub_EmptyChunk_NoPanic`
  - `TestScrub_EmptySecrets_PassThrough`
  - `TestScrub_Idempotent`
  - `TestScrub_OverlappingValues_DeterministicWinner`
  - `TestScrubbingStore_Append_UsesLookup` — decorator
    consults the JobStore-backed lookup and scrubs iff the
    job declares secrets.
  - `TestScrubbingStore_NilLookup_Passthrough` — dev-mode
    wiring without a JobStore does not panic.
  - `TestScrubbingStore_Get_PassesThrough` — read-side
    scrubbing is the response-layer's job.
- `internal/api/handlers_logs_test.go`:
  - `TestLogs_ScrubbingStore_RedactsSecretOnAppend` — end-
    to-end write path through the decorator.
  - `TestLogs_ResponsePath_RedactsLegacyChunks` — response-
    path pass catches entries written directly to the raw
    store.
  - `TestLogs_EndToEnd_SubmitChunkGet` — submit +
    append + GET: secret never appears in the response.
  - `TestLogs_NoSecrets_PassesThroughVerbatim` — regular
    jobs are unaffected.
  - `TestLogs_RBAC_NonOwnerForbidden` — feature-37
    regression guard: non-owner 403.
