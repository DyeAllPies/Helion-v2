# Feature: Resource ownership on every stateful type

**Priority:** P1
**Status:** Implemented (2026-04-19)
**Parent slice:** builds on [feature 35 — principal model](./35-principal-model.md)
**Affected files:**
`internal/proto/coordinatorpb/types.go`
(`OwnerPrincipal` field on `Job`, `Workflow`, `WorkflowJob`,
`ServiceSpec` embedded identity),
`internal/registry/` (dataset + model types gain
`OwnerPrincipal`),
`internal/cluster/service_registry.go` (service endpoints gain
`OwnerPrincipal`),
`internal/api/handlers_jobs.go` +
`internal/api/handlers_workflows.go` +
`internal/api/handlers_registry.go` (submit/register paths stamp
the authenticated principal as owner),
`internal/cluster/workflow_submit.go` (workflow-materialised
jobs inherit the workflow's owner),
`internal/cluster/persistence_jobs.go` (backfill legacy records
on load — one-time),
`internal/cluster/job_transition.go` (owner is immutable;
transitions cannot change it),
`dashboard/src/app/shared/models/index.ts` (response types gain
`owner_principal` field),
`docs/SECURITY.md` (ownership contract),
`docs/ARCHITECTURE.md` (field on each persisted type).

## Problem

Today's Helion has one field for "who owns this" — `SubmittedBy`
on `cpb.Job`. Every other persisted resource (workflows,
datasets, models, service endpoints) has NOTHING:

- `cpb.Workflow` — no owner field; the
  [AUDIT L1](../../../internal/api/handlers_jobs.go) job-level RBAC
  check has no workflow equivalent, so any authenticated user
  can read any workflow regardless of who submitted it.
- `registry.Dataset` / `registry.Model` — `CreatedBy` is
  audit-only, never consulted for access control. Any caller
  who knows the (name, version) can fetch either.
- `cluster.ServiceRegistry` — no owner on the in-memory map.
  Every operator sees every service endpoint.

Every future authorization decision (feature 37) needs a common
"who owns this" field it can compare against the caller's
principal. Without that field, the policy engine has nothing to
evaluate on.

Adding a dozen bespoke permission fields is not the answer —
that reproduces today's ad-hoc shape at scale. Adding ONE field
in a consistent place on every stateful type gives feature 37 a
single invariant to reason about.

## Current state

| Type | Owner-like field today | Used for access control? |
|---|---|---|
| `cpb.Job` | `SubmittedBy string` (JWT sub string) | Yes — `handleGetJob` compares `claims.Subject == job.SubmittedBy`. |
| `cpb.Workflow` | none | No. |
| `cpb.WorkflowJob` (child of Workflow) | none | No. |
| `registry.Dataset` | `CreatedBy string` | No (audit only). |
| `registry.Model` | `CreatedBy string` | No (audit only). |
| `cluster.ServiceEndpoint` | none | No. |

BadgerDB stores each type as JSON. Adding a new optional field
is a zero-cost migration: old records deserialise with the new
field at its zero value.

## Design

### 1. One field, same name, everywhere

Every stateful persisted type gains exactly one new field:

```go
// cpb.Job, cpb.Workflow, registry.Dataset, registry.Model,
// cluster.ServiceEndpoint, etc.
type Job struct {
    ...
    // OwnerPrincipal is the fully-qualified principal ID of
    // whoever created this resource (feature 35 format:
    // "kind:suffix", e.g. "user:alice" or "operator:alice@ops").
    //
    // Empty on records created before feature 36 shipped; see
    // `legacyOwner` below for the back-compat rule.
    //
    // Immutable after creation. Job transitions, workflow
    // starts, and retry attempts all preserve the original
    // owner — an operator whose job gets retried by the
    // coordinator's retry loop does not lose ownership to
    // `service:retry_loop`.
    OwnerPrincipal string `json:"owner_principal,omitempty"`
}
```

`SubmittedBy` on `cpb.Job` is kept as a deprecated alias that
equals `strings.TrimPrefix(OwnerPrincipal, "user:")` — no migration
of existing code readers, just a doc note that the new field is
authoritative.

### 2. Stamping at create time

Submit handlers resolve the Principal (feature 35) and stamp the
ID onto the resource before persistence:

```go
// handleSubmitJob
p := principal.FromContext(r.Context())
job.OwnerPrincipal = p.ID  // "user:alice" / "operator:alice@ops"
```

Workflow-materialised jobs inherit the parent workflow's owner:

```go
// cluster/workflow_submit.go, inside Start()
job := &cpb.Job{
    ...
    OwnerPrincipal: wf.OwnerPrincipal,  // child inherits
}
```

Registry endpoints do the same for datasets + models.

Service endpoints (feature 17) inherit from the Job that created
them:

```go
// cluster/service_registry.go
func (r *ServiceRegistry) Record(..., ownerPrincipal string) { ... }
```

### 3. Immutability

Ownership cannot be transferred after creation in this slice.
No `POST /admin/jobs/{id}/chown` endpoint. Every code path that
mutates a Job/Workflow/Dataset/Model record (state transitions,
retries, workflow-job materialisation, cancellation) MUST
preserve `OwnerPrincipal`.

A new test guard, `TestJobTransition_PreservesOwnerPrincipal`,
goes in each of the places state changes today. The tests are
repetitive on purpose — one per transition path — because this
is the kind of invariant that gets silently regressed by an
innocent-looking rewrite.

If delegation becomes a need later, feature 38's share mechanism
is the right path. Ownership transfer is a larger-scope
operation that we are not yet sure we need.

### 4. Legacy record handling

Every BadgerDB record created before feature 36 ships will load
with `OwnerPrincipal == ""`. Two safe choices for the policy
engine (feature 37) to make:

**(a) Treat empty as a synthetic `legacy:` principal.**
Feature 37 denies everything for non-admins on `legacy:` owned
resources. Existing behaviour (AUDIT L1: non-admin gets 403 on
a legacy Job) is preserved.

**(b) Backfill on load.** When the JobStore loads a legacy Job,
synthesise `OwnerPrincipal = "user:" + SubmittedBy` for Jobs
that have SubmittedBy (the old behaviour), and `"legacy:"` for
everything else. The synthesised value is persisted on the next
mutation (transition, retry, etc.) via Save. Over time, all
legacy records get rewritten on first touch.

**We go with (b).** It keeps the number of deny-all codepaths
tiny and converges on clean data as a cluster runs. (a) is the
fallback for workflows/datasets/models, which don't have
`SubmittedBy`, so those genuinely need the `legacy:` sentinel.

### 5. Response shape

`JobResponse` / `WorkflowResponse` / `DatasetResponse` /
`ModelResponse` each gain an `owner_principal` field in the
wire JSON. The dashboard surfaces it on detail views so an
operator can tell at a glance whose resource they're looking
at.

`SubmittedBy` stays on the wire for one release for
back-compat; deprecate in release notes.

### 6. Audit

Every create event (`job_submit`, `workflow_submit`,
`dataset.registered`, `model.registered`) gains a
`resource_owner` detail field. A reviewer scanning the audit
log for "who owns what" can filter on this instead of reading
the body of each event.

## Security plan

| Threat | Control |
|---|---|
| An attacker with a leaked JWT creates a job, ownership stamp uses the wrong identity (e.g., the impersonating JWT's subject), later policy checks pass | Principal resolution (feature 35) is the single source of truth; the ownership stamp reads the Principal from context. A leaked JWT gives the attacker that subject's principal — same blast radius as "the attacker has the JWT". No new surface. |
| A malicious node registers a job via the gRPC dispatch path with a forged SubmittedBy | Node RPCs cannot create jobs today (JobStore.Submit is reached only from the REST handlers + workflow Start path, both of which resolve a Principal from the REST-side authenticated context, not from gRPC). Feature 36 preserves that invariant: the stamping line is inside `handleSubmitJob`/`handleSubmitWorkflow`, never in a gRPC handler. |
| A workflow-spawned job is given the wrong owner because the workflow was somehow submitted without a Principal | The workflow handler is behind `authMiddleware`; a workflow with empty `OwnerPrincipal` means auth was disabled (dev mode). Feature 37 treats empty-owner resources as the synthetic `legacy:` principal — non-admins denied — so a broken auth chain in prod fails closed. |
| Ownership change via a cancel → re-submit trick | Transitions preserve the owner. A user can cancel their own job and submit a new one with the same ID; the new job gets a new owner, but it's a different Badger record (new serial / new created_at). No ownership is carried from the old to the new job. |
| Legacy record without SubmittedBy AND without OwnerPrincipal gets default-allow somewhere | `legacyOwner` sentinel is a string that no valid principal ID will ever equal. Feature 37's policy evaluator refuses every action by non-admins on `legacy:`-owned resources. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | Add `OwnerPrincipal` field to `cpb.Job`, `cpb.Workflow`, `cpb.WorkflowJob`, `registry.Dataset`, `registry.Model`, `cluster.ServiceEndpoint`. Zero test churn because the field is additive. | feature 35 | Small |
| 2 | Stamp in every submit handler: `handleSubmitJob`, `handleSubmitWorkflow`, `handleRegisterDataset`, `handleRegisterModel`, and `cluster.ServiceRegistry.Record` call sites. | 1 | Small |
| 3 | Propagation in `cluster.WorkflowStore.Start` — each materialised job carries the workflow's owner. | 1 | Trivial |
| 4 | Persistence backfill: `BadgerJSONPersister.LoadAllJobs` rewrites empty `OwnerPrincipal` to `"user:" + SubmittedBy` on legacy records that have SubmittedBy; leaves `legacy:` on others. One saved migration for workflow + dataset + model load paths too. | 1 | Small |
| 5 | Add `OwnerPrincipal` to every response type (`JobResponse`, `WorkflowResponse`, etc.). Dashboard gains a badge on detail views. | 2–4 | Small |
| 6 | Audit event detail gains `resource_owner` on every create event type. | 2 | Trivial |
| 7 | Preserve-owner invariant tests on every transition/retry/start path. Table-driven. | 2–4 | Medium |
| 8 | Docs: SECURITY.md contract, ARCHITECTURE.md schema tables. | 1–7 | Trivial |

## Tests

Additive:

- `TestSubmitJob_StampsOwnerPrincipal` — `Submit` under a
  KindOperator principal stores `OwnerPrincipal ==
  "operator:<cn>"`.
- `TestSubmitJob_AnonymousOwner` — dev-mode submit under
  `Anonymous()` stores `OwnerPrincipal == "anonymous"`. Feature
  37 will deny actions on these, but the stamp itself is
  correct.
- `TestSubmitWorkflow_StampsOwnerPrincipal` + workflow-job
  inheritance: every materialised `cpb.Job` carries the
  workflow's owner.
- `TestRegisterDataset_StampsOwnerPrincipal` + model parallel.
- `TestServiceEndpoint_InheritsJobOwner` — a service job's
  endpoint record has the same OwnerPrincipal as the job.

Load + migration:

- `TestLoadLegacyJob_BackfillsFromSubmittedBy` — a Job persisted
  before feature 36 with `SubmittedBy == "alice"` loads with
  `OwnerPrincipal == "user:alice"`.
- `TestLoadLegacyWorkflow_UsesLegacySentinel` — a Workflow
  with no SubmittedBy equivalent loads with `OwnerPrincipal
  == "legacy:"`.

Invariant guards (regression):

- `TestJobTransition_PreservesOwner` — running → completed:
  owner unchanged.
- `TestJobRetry_PreservesOwner` — even when the retry loop is
  the caller of Submit, the owner is the original submitter's
  principal, not `service:retry_loop`.
- `TestWorkflowStart_ChildJobInheritsOwner` — every job in
  `wf.Jobs` carries `wf.OwnerPrincipal`.
- `TestWorkflowCancel_PreservesOwner` — cancelling a workflow
  or its children leaves the owner field alone.
- `TestDatasetAlreadyExistsError_PreservesOwner` — a 409 on
  re-register does not overwrite the existing record's owner.

## Open questions

- **Should `SubmittedBy` be removed in this slice?** No. Keep for
  one release as a back-compat alias; remove in a follow-up
  once external consumers (CLI, deploy scripts) have rolled
  over to `owner_principal`. Removing now breaks feature 21
  tests that assert the field exists.

## Deferred

- **Ownership transfer endpoint.** `POST /admin/resources/{kind}/{id}/chown`
  to change an owner. Out of scope for this slice; the share
  mechanism in feature 38 handles most real-world delegation
  cases without the invariant-breaking complexity of transfer.
- **Multi-owner resources.** A job with two simultaneous owners.
  Not obviously useful; revisit if a team-scoped use case
  appears.

## Implementation status

_Implemented 2026-04-19._

### What shipped

- `OwnerPrincipal` field added to `cpb.Job`, `cpb.Workflow`,
  `cpb.ServiceEndpoint`, `registry.Dataset`, `registry.Model`.
  Every persisted stateful type now carries an authoritative
  owner.
- `handleSubmitJob`, `handleSubmitWorkflow`,
  `handleRegisterDataset`, `handleRegisterModel` stamp the
  caller's principal via `principal.FromContext(ctx).ID` at
  create time. `SubmittedBy` / `CreatedBy` remain populated for
  one release as a back-compat alias.
- `WorkflowStore.Start` propagates the workflow's
  `OwnerPrincipal` to every materialised child `cpb.Job` and
  synthesises a back-compat `SubmittedBy` from the owner via
  `principal.SubjectFromID`.
- `grpcserver.ReportServiceEvent` captures the owning job's
  `OwnerPrincipal` during the existing node-mismatch fetch and
  plumbs it onto `services.Upsert` so the in-memory
  `ServiceEndpoint` inherits owner on first `ready` event.
- Legacy records are backfilled on load:
  - `persistence_jobs.LoadAllJobs` synthesises
    `OwnerPrincipal = "user:<SubmittedBy>"` when the stamp is
    missing; empty SubmittedBy falls through to the
    `principal.LegacyOwnerID` sentinel (`"legacy:"`) so
    feature 37's policy evaluator fails closed.
  - `persistence_workflows.LoadAllWorkflows` and the registry
    Dataset/Model loaders apply the same pattern against their
    legacy proxy fields (`CreatedBy` for registry; no proxy
    for Workflow → always `legacy:`).
  - The backfill is in-memory only — we do not rewrite Badger
    entries. The next state-transition SaveJob / SaveWorkflow
    naturally persists the synthesised value.
- Response shapes: `api.JobResponse`, `WorkflowResponse`,
  `DatasetResponse`, `ModelResponse` expose `owner_principal` as
  a top-level field. The `SubmittedBy` / `CreatedBy` aliases
  stay on the wire.
- Audit events for create paths (`job_submit`, `job_dry_run`,
  `workflow_dry_run`, `dataset.registered`, `dataset.dry_run`,
  `model.registered`, `model.dry_run`) include a
  `resource_owner` detail alongside `actor`. Reviewers can
  distinguish "who did it" (actor) from "who owns the target
  resource" (resource_owner), which matters when service
  principals later drive state transitions on user-owned
  resources.

### Deviations from plan

- **WorkflowJob.OwnerPrincipal was deliberately skipped.** The
  spec called for it on every stateful type, but
  `WorkflowJob` is a DAG-declaration shape that materialises
  into a `cpb.Job` at Start() time; the Job carries its own
  `OwnerPrincipal`. Adding the field on the template would
  introduce two sources of truth per child and no authz surface
  consults the template version — feature 37 evaluates
  ownership against the materialised Job.
- **No `/chown` endpoint, no multi-owner support.** Deferred
  per the Open questions section — feature 38's share mechanism
  covers real-world delegation without breaking the
  owner-immutability invariant.
- **Dashboard field wiring is transport-ready but not rendered.**
  The dashboard app today doesn't surface `submitted_by` in the
  rendered job/workflow views; adding `owner_principal` to the
  TS models without a matching UI would be dead weight. The
  backend JSON already carries `owner_principal` so a future
  dashboard slice can consume it on-demand.

### Tests added

- `internal/cluster/owner_principal_test.go`
  - `TestOwnerPrincipal_JobSubmitPersistsAndSurvivesTransitions`
    — happy path through dispatching/running/completed.
  - `TestOwnerPrincipal_JobCancelPreservesOwner` — cancel path.
  - `TestOwnerPrincipal_WorkflowStartInheritsOnChildJobs` —
    workflow→child materialisation.
  - `TestOwnerPrincipal_LegacyBackfill_SubmittedBySynthesisesUser`.
  - `TestOwnerPrincipal_LegacyBackfill_MissingFieldsYieldsSentinel`.
  - `TestOwnerPrincipal_WorkflowLegacyBackfill_MissingFieldsYieldsSentinel`.
- `internal/principal/principal_test.go`
  - `TestPrincipal_OwnerFromLegacy` — covers empty subject →
    sentinel, bare subject → user:<sub>, email-shaped,
    colon-containing subject edge case.
