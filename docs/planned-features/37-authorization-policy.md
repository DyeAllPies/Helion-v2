# Feature: Authorization policy engine + middleware

**Priority:** P1
**Status:** Pending
**Parent slice:** depends on [feature 35 — principal model](35-principal-model.md) and [feature 36 — resource ownership](36-resource-ownership.md)
**Affected files:**
new `internal/authz/` package (Action enum + Allow evaluator +
DenyError + per-resource rules),
`internal/api/middleware.go` (adminMiddleware becomes a thin
`authz.Allow(ActionAdmin, systemResource)` shim),
`internal/api/handlers_jobs.go` (replace the
`claims.Subject != job.SubmittedBy` check),
`internal/api/handlers_workflows.go` (NEW: no per-workflow RBAC
today — this slice adds it),
`internal/api/handlers_registry.go` (NEW: dataset + model
get/list/delete RBAC),
`internal/api/handlers_services.go` (service endpoint RBAC),
`internal/api/handlers_admin.go` (reveal-secret + revoke-node +
issue-token + issue-op-cert — admin-scoped),
`internal/audit/logger.go` (`EventAuthzDeny` event),
`docs/SECURITY.md` (new §8 on the policy model),
`docs/ARCHITECTURE.md` (decision table).

## Problem

After features 35 + 36 land, every request carries a
`*principal.Principal` in its context and every resource has an
`OwnerPrincipal`. The missing piece is the **decision layer**:
who is allowed to read/write/cancel/delete/reveal what?

Today's decisions are ad-hoc and inconsistent:

- **Jobs (`GET /jobs/{id}`):** admin OR `claims.Subject ==
  job.SubmittedBy`. Any other mutation (`cancel`, `reveal-secret`)
  checks admin-only or reuses the subject comparison.
- **Workflows:** no per-workflow check. Every authenticated user
  can read / cancel every workflow.
- **Datasets + Models:** no per-resource check. Every
  authenticated user can read, delete, and register (subject to
  rate limits).
- **Services (feature 17):** no per-service check beyond the
  admin-middleware gate that some paths use.
- **Admin endpoints** (revoke-node, issue-token, reveal-secret,
  operator-cert issue): `adminMiddleware` with a binary
  admin-or-not check. Works for true admin actions but
  conflates "is admin" with "can perform action X on resource
  Y" — a distinction feature 38 (groups) will want to split.

The code doesn't have a single place to read the policy from, so
every time a new endpoint or resource lands, someone writes the
check from scratch. That's how features 22–28 each ended up with
slightly different authorisation code paths.

## Current state

- `adminMiddleware` in `internal/api/middleware.go` checks
  `claims.Role != "admin"` and returns 403. Applied to
  `/admin/*` routes.
- `handleGetJob` reads `job.SubmittedBy` directly and compares.
- Every other handler has either no RBAC or a bespoke check.
- There is no `authz` package, no `Action` enum, no policy
  table.
- Audit events do not call out authorisation denials vs
  authentication failures (401 vs 403 are logged indistinctly
  under `auth_failure`).

## Design

### 1. Single evaluator

New package `internal/authz/`:

```go
package authz

type Action string

const (
    ActionRead    Action = "read"      // GET /jobs/{id}, /workflows/{id}, etc
    ActionList    Action = "list"      // GET /jobs
    ActionWrite   Action = "write"     // POST /jobs (create)
    ActionCancel  Action = "cancel"    // POST /jobs/{id}/cancel, DELETE /workflows/{id}
    ActionDelete  Action = "delete"    // DELETE /api/datasets/.../{name}/{version}
    ActionReveal  Action = "reveal"    // POST /admin/jobs/{id}/reveal-secret
    ActionAdmin   Action = "admin"     // coordinator-wide admin actions
)

// Resource names "what is being accessed". Kind is a compile-time
// constant ("job" / "workflow" / "dataset" / ...); ID identifies
// the instance; OwnerPrincipal is the feature-36 stamp. Shares
// is populated by feature 38 and nil until then.
type Resource struct {
    Kind           string
    ID             string
    OwnerPrincipal string
    Shares         []Share // feature 38; nil today
}

// Allow returns nil iff principal p is permitted to perform
// action on res. Non-nil errors are always *DenyError.
//
// Thread-safe. No I/O. Pure function of (p, action, res).
func Allow(p *principal.Principal, action Action, res *Resource) error
```

Policy (the v1 rules, before groups):

```
1. Nil or anonymous principal: deny everything except ActionList
   on public resources (there are none today; explicit deny).

2. Kind=user or Kind=operator with Role=admin: allow everything.
   Admin is the break-glass principal.

3. Kind=node: allowed ONLY on node-internal actions (dispatch
   ack, log stream, service-event report). Never allowed on
   REST-surface actions regardless of OwnerPrincipal. This is
   enforced because a compromised node should not be able to
   stand up a fake job via the REST API using its mTLS identity.

4. Kind=service: allowed on coordinator-internal actions tagged
   with the same service name. `service:retry_loop` can cancel
   a job the retry logic owns; `service:dispatcher` can
   transition jobs it's dispatching. This is scoped per action
   via the Resource.Kind and a per-kind action table.

5. Kind=job: the workflow-scoped token can access ONLY jobs
   that belong to the same workflow. Role-scoped submits from
   feature 33 reuse this check with a required_cn binding too.

6. Kind=user / Kind=operator without admin: allowed iff
   p.ID == res.OwnerPrincipal. This is the owner check.

7. Kind=anonymous: deny.

8. Everything else: deny.
```

Feature 38 inserts rule 6b — "or p matches one of res.Shares" —
between rules 6 and 7.

### 2. Per-resource action tables

Every handler that performs a mutation or read calls `Allow`
with the (action, resource) for THAT endpoint. A cheat-sheet
table codifies which endpoint maps to which action:

| Endpoint | Action | Resource.Kind |
|---|---|---|
| `GET /jobs/{id}` | ActionRead | job |
| `GET /jobs` | ActionList | job |
| `POST /jobs` | ActionWrite | job |
| `POST /jobs/{id}/cancel` | ActionCancel | job |
| `GET /workflows/{id}` | ActionRead | workflow |
| `GET /workflows` | ActionList | workflow |
| `POST /workflows` | ActionWrite | workflow |
| `DELETE /workflows/{id}` | ActionCancel | workflow |
| `GET /api/datasets/{name}/{version}` | ActionRead | dataset |
| `POST /api/datasets` | ActionWrite | dataset |
| `DELETE /api/datasets/...` | ActionDelete | dataset |
| `POST /api/models`, GET, DELETE | analogous | model |
| `GET /api/services/{job_id}` | ActionRead | service |
| `POST /admin/jobs/{id}/reveal-secret` | ActionReveal | job |
| `POST /admin/nodes/{id}/revoke` | ActionAdmin | system |
| `POST /admin/tokens` | ActionAdmin | system |
| `DELETE /admin/tokens/{jti}` | ActionAdmin | system |
| `POST /admin/operator-certs` | ActionAdmin | system |

`ActionAdmin` against `Kind=system` is a special case the policy
evaluator shortcircuits on (admin short-circuit from rule 2).

### 3. List endpoints

`ActionList` is subtler: the policy engine can't evaluate "every
job" in one call; the handler has to filter the returned list.
Two options:

**(a) Handler filters post-fetch.** Load all matching jobs from
the store, run `Allow(ActionRead, job)` per row, return the
subset. Simple but wasteful at scale.

**(b) Scope push-down.** JobStore gains a `ListFor(principal
*Principal) ([]Job, ...)` method that applies the scope in the
query (WHERE owner_principal IN (...)).

**We ship (a) in this slice** because the JobStore is BadgerDB
(no native WHERE), and the expected list sizes on realistic
clusters (< 1000 active jobs) make the filter-in-memory approach
fine. (b) is a follow-up when someone hits the performance cliff
— file it in deferred/.

### 4. Middleware shim

`adminMiddleware` stays as a convenience wrapper for routes that
are purely admin:

```go
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        p := principal.FromContext(r.Context())
        if err := authz.Allow(p, authz.ActionAdmin, authz.SystemResource()); err != nil {
            // ... emit EventAuthzDeny ...
            writeError(w, http.StatusForbidden, "forbidden")
            return
        }
        next(w, r)
    }
}
```

Non-admin handlers (jobs, workflows, registry, services) call
`Allow` inline with a resource they've already loaded from the
store — they can't do the check in middleware because the
resource ID comes from the URL + the resource body comes from a
store fetch.

### 5. Audit

Every deny fires `EventAuthzDeny`:

```json
{
  "type": "authz_deny",
  "principal": "user:alice",
  "action": "reveal",
  "resource_kind": "job",
  "resource_id": "j-42",
  "resource_owner": "operator:bob@ops",
  "reason": "not_owner"
}
```

The analytics auth-events panel (feature 28) adds a filter for
`authz_deny`. An operator watching for probes sees every `403`
the coordinator has emitted with enough context to triage.

### 6. Error response shape

403 responses carry a stable short error code + a free-form
message. Dashboard surfaces the code in the error banner so
localisation / i18n works downstream.

```json
{"error": "forbidden", "code": "not_owner"}
```

Codes: `not_owner`, `admin_required`, `node_not_allowed`,
`job_scope_mismatch`, `anonymous_denied`.

## Security plan

| Threat | Control |
|---|---|
| A user reads another user's workflow — previously unchecked | Per-workflow RBAC via `authz.Allow(ActionRead, workflowResource)`. First time this endpoint has any check. |
| A node principal uses a leaked node mTLS cert to submit a job via the REST listener | Rule 3: `Kind=node` is denied on REST actions regardless of role. Applies even if the node happens to have a valid operator-like JWT attached. |
| A service principal can silently cancel any job it touches | Service principals are scoped per action. `service:retry_loop` can `ActionCancel` on jobs, but not `ActionReveal`. The per-kind action table is compile-time; a future service adding an action goes through a table edit + code review. |
| List endpoints leak metadata even when individual reads are denied | `handleListJobs` filters per-row through `Allow(ActionRead)` before serialising. The total count is the filtered count; pagination is computed after filter. |
| A policy bug silently allows too much | Deny-log every 403 via `EventAuthzDeny`. An audit dashboard panel shows count-over-time; a sudden drop in deny events (after a new deploy) is an alert. |
| Evaluator panics on unknown `Kind` | `Allow` uses a switch on `principal.Kind` with a default that returns DenyError. Unknown kind fails closed. |
| `nil *Principal` panics the evaluator | `Allow(nil, ...)` returns a DenyError, not a panic. Callers that pass nil (should be impossible after feature 35 wires context properly) get a 403. |

Authentication (feature 35) is the input; authorisation is this
slice. Both must agree: a request that's wrongly authenticated
is a feature-35 bug, a request that's wrongly authorised is a
feature-37 bug. Audit distinguishes the two via event type.

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `internal/authz/` — type + Allow + DenyError + per-kind rules. Table-driven unit tests. | features 35, 36 | Medium |
| 2 | `adminMiddleware` rewritten as an `ActionAdmin` call. | 1 | Trivial |
| 3 | `handleGetJob` migrated from `claims.Subject != SubmittedBy` to `Allow(ActionRead, ...)`. Existing AUDIT L1 test becomes the regression guard. | 1 | Small |
| 4 | Workflow RBAC: `handleGetWorkflow` + `handleCancelWorkflow` gain `Allow`. First time these endpoints have any check — expect client-side breakage in test suites that submit + read across different JWTs. | 1 | Medium |
| 5 | Registry RBAC: dataset + model get/list/delete gain `Allow`. Keep `handleListDatasets` filtering in-memory. | 1 | Medium |
| 6 | Service-endpoint RBAC (feature 17 surface). | 1 | Small |
| 7 | Reveal-secret + other admin endpoints that today use `adminMiddleware` keep working (ActionAdmin passes through). | 2 | Trivial |
| 8 | Audit integration: `EventAuthzDeny` constant + emit on every deny path. Analytics panel from feature 28 picks it up for free. | 1 | Small |
| 9 | Dashboard: 403 response parsing surfaces the `code` field; error banner localises. | 1–8 | Small |
| 10 | Docs: SECURITY.md §8, ARCHITECTURE.md decision table. | 1–9 | Trivial |

## Tests

Evaluator:

- Table-driven `TestAllow` with cases for every combination of
  (Kind × Role × Action × owner-match/mismatch). ~40 rows.
- `TestAllow_NilPrincipal_Denied` — defensive: nil in, deny out.
- `TestAllow_UnknownKind_Denied` — unknown kind fails closed.
- `TestAllow_AdminShortCircuit` — user/operator with Role=admin
  allows everything regardless of ownership.
- `TestAllow_NodeDeniedOnRESTActions` — Kind=node is refused on
  Write/Read/Cancel/Delete; only internal actions (defined by
  a follow-up table on service/node resources) are allowed.
- `TestAllow_Legacy_OwnerIsLegacySentinel` — resource with
  `OwnerPrincipal == "legacy:"` allows only admin.

Handler integration:

- `TestHandleGetJob_NotOwner_Returns403` — already exists for
  AUDIT L1; reused.
- `TestHandleGetWorkflow_NotOwner_Returns403` — new.
- `TestHandleCancelWorkflow_NotOwner_Returns403` — new.
- `TestHandleGetDataset_NotOwner_Returns403` — new.
- `TestHandleListJobs_FiltersOutUnowned` — call `GET /jobs`
  with Alice's token; assert the list does not contain Bob's
  jobs.
- `TestAuthzDeny_EmitsAuditEvent` — every 403 produces an
  `authz_deny` event carrying the principal, action, and
  resource kind.

Regression:

- Existing AUDIT L1 test (`TestGetJob_ForbiddenForNonOwner`)
  continues to pass; just the implementation underneath
  changed.
- Existing admin-gated endpoint tests
  (`TestRevokeNode_NotAdmin_403`, etc.) pass through the new
  middleware unchanged.

## Open questions

- **Should `Allow` return a typed DenyError or just a sentinel
  `ErrForbidden`?** Typed — the code carries the reason so
  audit + dashboard can render it. Resolved.

- **Node + service principal matrix — where lives the per-kind
  action allow-list?** In `authz/rules.go` as a
  `map[string][]Action` keyed by Kind and resource Kind. Short
  enough to read during review. Not operator-configurable in
  this slice — a future feature could expose it for custom
  service principals.

## Deferred

- **Scope push-down on list endpoints.** Filter-in-memory
  scales to tens of thousands of resources; beyond that a
  JobStore-level scope query becomes worthwhile. Deferred
  until a deployment hits the cliff.
- **Per-attribute policies.** "Alice can read jobs but only
  the status field, not the env." Attribute-level policies are
  a serious scope creep; revisit if multiple deployments ask.
- **Policy rule file.** An admin-editable YAML/JSON policy
  loaded at boot. Today rules are Go code; a config file
  expands blast radius of a policy edit significantly.

## Implementation status

_Not started. Planned 2026-04-19 as part of the IAM foundation
discussed in features 35–38._
