# Feature: Dry-run preflight for `POST /jobs` and `POST /workflows`

**Priority:** P1
**Status:** Pending
**Affected files:**
`internal/api/handlers_jobs.go`, `internal/api/handlers_workflows.go`,
`internal/api/handlers_jobs_test.go`,
`internal/api/handlers_workflows_test.go`,
`docs/ARCHITECTURE.md` (REST table — new query param row).

## Problem

A submitter has no way to validate a job or workflow spec without
committing to a run. Operators writing YAML by hand, CI linting a
workflow, and — most importantly — the feature 22 dashboard
submission UI all need the same thing: send the full body
through the coordinator's validator stack and get back 400
errors **without** persisting state or dispatching anything.

Today the only options are:
- Parse with `js-yaml` in the client (misses server-only rules
  like `validateNodeSelector` + `validateServiceSpec`).
- Submit the real job and cancel it (pollutes audit, wakes up
  dispatch, consumes rate-limit budget).
- Read the Go source and hand-mirror the rules (already drifts).

## Current state

- [`handleSubmitJob`](../../internal/api/handlers_jobs.go#L449)
  runs the request through validators then calls
  `JobStore.Put` + `s.dispatch.Enqueue`.
- [`handleSubmitWorkflow`](../../internal/api/handlers_workflows.go#L94)
  runs through per-job validators + DAG validation then calls
  `WorkflowStore.Put` + `s.workflowRunner.Start`.
- Both handlers pass through the full middleware stack (body
  cap, JWT auth, rate limit, audit).

## Design

Add a single boolean query parameter **`dry_run`** to both
endpoints. When `dry_run=true`:

1. **Validators run identically** to the real path. Same
   per-field bounds, same `validateNodeSelector`, same
   `validateServiceSpec`, same DAG cycle detection for
   workflows.
2. **No state is persisted.** `JobStore.Put`,
   `WorkflowStore.Put`, and any `registry` writes are skipped.
3. **No dispatch.** `s.dispatch.Enqueue` / `workflowRunner.Start`
   are skipped.
4. **Response body is identical** to the real 200 — the
   would-be `Job` / `Workflow` object with the generated ID — so
   clients can diff against what they expect.
5. **Audit emits a distinct event type**: `job.dry_run` /
   `workflow.dry_run` (not `job.submit` / `workflow.submit`)
   with the same actor + command + job ID fields. Rate limiter
   deducts from the same bucket as a real submit (a flood of
   dry-runs is still a flood).

The boolean is accepted in three forms for shell convenience:
`?dry_run=1`, `?dry_run=true`, `?dry_run=yes`. Empty / missing /
any other value → real submit path. No silent fallback: an
invalid value is rejected 400 so typos (`?dry-run=true` with a
dash) don't get a real submission by accident.

### Handler sketch

```go
func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
    // ... existing body-cap + decode + validate ...
    if validationErr != nil { writeError(...); return }

    dryRun, err := parseDryRunParam(r)
    if err != nil { writeError(w, 400, err.Error()); return }

    job := buildJobFromRequest(req, actor)

    if dryRun {
        s.audit.Log(ctx, "job.dry_run", actor, map[string]any{
            "job_id": job.ID, "command": job.Command,
        })
        writeJSON(w, "handleSubmitJob.dry_run", job)
        return
    }

    if err := s.jobs.Put(ctx, job); err != nil { ... }
    s.dispatch.Enqueue(job.ID)
    s.audit.Log(ctx, "job.submit", actor, map[string]any{...})
    writeJSON(w, "handleSubmitJob", job)
}
```

Same shape for `handleSubmitWorkflow`.

## Security plan

| Concern | Mitigation |
|---|---|
| Dry-run bypasses validation and returns a 200 that the attacker can use as oracle for what's allowed | Dry-run runs the **exact** validators the real path does. A valid dry-run response means "the real submit would also be accepted" — which is the whole point — not "I leaked a hidden ruleset". |
| Dry-run used to enumerate valid job IDs / probe rate limits | Same per-subject rate limit bucket as real submits; same audit trail (distinguished by event type). Any probing appears in the audit log. |
| Dry-run response contains secret env values from the request | When feature 26 lands (secret env vars), dry-run responses are subject to the same redaction rules as `GET /jobs/{id}`. Noted as a cross-feature dependency. |
| Dry-run confused with real submit by a sleepy reviewer | `job.dry_run` / `workflow.dry_run` event types are distinct from `job.submit` / `workflow.submit` — audit queries can filter either way. Response JSON includes `"dry_run": true` at the top level. |

## Implementation order

1. `parseDryRunParam(r)` helper + unit tests for `1`, `true`,
   `yes`, `""` (absent), `0`, `false`, `no`, `blah` (400).
2. Wire into `handleSubmitJob` (simpler — single job).
3. Wire into `handleSubmitWorkflow`.
4. ARCHITECTURE.md REST table update.

## Tests

- `TestHandleSubmitJob_DryRun_Validates_NotPersisted` — body
  passes validators → 200 with `"dry_run": true` + the would-be
  job; assert `JobStore.Put` never called + `Dispatch.Enqueue`
  never called. Audit fixture records exactly one
  `job.dry_run` event.
- `TestHandleSubmitJob_DryRun_RejectsBadBody` — body fails
  validators → 400, same error messages as the non-dry-run path,
  no audit event recorded (or recorded as a `job.dry_run_reject`
  — TBD).
- `TestHandleSubmitJob_DryRunParam_ValueForms` — table test over
  `1`, `true`, `yes`, `0`, `false`, `no`, `""`, `blah`. First
  three route to dry-run; next three + missing route to real
  submit; last raises 400.
- Mirror tests for `handleSubmitWorkflow`, including the DAG
  cycle detector: dry-run with a cycle → 400 reporting the cycle
  without persisting anything.
- Rate-limit test: 11 dry-runs in one second from the same
  subject → eleventh returns 429. Ensures dry-run doesn't have
  its own bigger bucket.

## Acceptance criteria

1. `curl -XPOST -H "Authorization: Bearer $TOK" \
     "http://localhost:8080/jobs?dry_run=true" \
     -d '{"command":"echo","args":["hi"]}'` returns 200 with a
   job body carrying `"dry_run": true`.
2. `GET /jobs/<that-id>` returns 404 (was never persisted).
3. Same body but `"command":""` returns 400.
4. `curl -XPOST "...workflows?dry_run=true" -d @cyclic.yaml`
   returns 400 reporting the cycle; `GET /workflows/<that-id>`
   is 404.
5. Audit log entry types `job.dry_run` and `workflow.dry_run`
   appear; there is NO matching `job.submit` / `workflow.submit`.

## Deferred

- **Dry-run for `POST /api/datasets` and `POST /api/models`.**
  Lower value — registry writes are emitted by in-workflow
  scripts, not operators. Add when there's demand.
