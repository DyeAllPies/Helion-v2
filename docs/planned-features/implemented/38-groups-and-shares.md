# Feature: Groups and resource shares (delegation)

**Priority:** P2
**Status:** Implemented (2026-04-19)
**Parent slice:** depends on [feature 35](./35-principal-model.md),
[feature 36](./36-resource-ownership.md), and
[feature 37](./37-authorization-policy.md).
**Affected files:**
new `internal/groups/` package (Group type + storage),
`internal/principal/principal.go` (Principal gains a
`Groups []string` field populated at resolve time),
`internal/authz/authz.go` (Allow grows a group-expansion step
before falling through to deny; `Share` type gains list
semantics),
new admin endpoints:
  `POST /admin/groups`, `GET /admin/groups`,
  `POST /admin/groups/{name}/members`,
  `DELETE /admin/groups/{name}/members/{principal}`,
  `POST /admin/resources/{kind}/{id}/share`,
  `GET /admin/resources/{kind}/{id}/shares`,
  `DELETE /admin/resources/{kind}/{id}/share/{grantee}`,
`internal/audit/logger.go` (new event types: `group_created`,
`group_member_added/removed`, `resource_shared`,
`resource_share_revoked`),
`dashboard/src/app/features/admin/` new component for group +
share management (admin-role-guarded),
`docs/SECURITY.md` (new §8.1 on delegation model).

## Problem

Once features 35–37 land, Helion has:

- Every actor is a typed `Principal` (user / operator / node /
  service / job / anonymous).
- Every resource has an `OwnerPrincipal`.
- A single policy evaluator that allows admin-or-owner by default.

That covers most use cases. What it does not cover:

- **Teams.** "Anyone on `ml-research` can read Alice's workflow."
  Today we'd have to make every team member a co-owner, which
  the ownership model (feature 36) rejects as a valid state
  (one owner per resource, immutable).
- **Per-resource delegation.** "Alice is on vacation; Bob needs
  to cancel her running workflow." Today the answer is "tell an
  admin to do it" — every delegation routes through the
  break-glass admin role.
- **Scoped shares.** "Alice shares her trained model's dataset
  with the inference team but not her training workflow." A
  share mechanism lets her grant ActionRead on the dataset
  without exposing anything else she owns.

Without groups and shares, operators push delegation into the
admin role, which:

1. concentrates too much blast radius on the admin token set,
2. loses attribution (audit shows "admin did it" not "Bob
   acting under alice-delegation"),
3. slows down day-to-day work for anything that crosses
   ownership boundaries.

## Current state

- Feature 35 delivered `*principal.Principal` in context.
  `Principal.Groups` is a field declared but not populated — it
  waits for this slice to add the lookup.
- Feature 37 delivered `authz.Allow`. The `Resource.Shares`
  field was added; the evaluator's rule 6b ("or a matching
  share") is currently a no-op that always returns false.
- No group storage, no share storage, no management endpoints.

This slice fills those gaps.

## Design

### 1. Group storage

```go
// internal/groups/
//
// A Group is a named, flat collection of principal IDs. Groups
// do not nest in v1 — "group of groups" membership is a
// recursion-risk we are not opting into without a concrete
// use case.
//
// Group names are admin-issued and live under a distinct
// namespace so a "group:ml-research" reference is
// unambiguous vs "user:ml-research" etc.

type Group struct {
    Name       string    `json:"name"`       // e.g. "ml-research"
    Members    []string  `json:"members"`    // principal IDs
    CreatedAt  time.Time `json:"created_at"`
    CreatedBy  string    `json:"created_by"` // principal ID of creator
    UpdatedAt  time.Time `json:"updated_at"`
}

type Store interface {
    Create(ctx context.Context, g Group) error  // ErrGroupExists on conflict
    Get(ctx context.Context, name string) (*Group, error)
    List(ctx context.Context) ([]Group, error)
    AddMember(ctx context.Context, name, principalID string) error
    RemoveMember(ctx context.Context, name, principalID string) error
    Delete(ctx context.Context, name string) error

    // GroupsFor returns the list of group names the principal is
    // a member of. Used at principal-resolution time (feature 35)
    // to populate Principal.Groups.
    GroupsFor(ctx context.Context, principalID string) ([]string, error)
}
```

Backed by BadgerDB under a new `groups/` prefix. Keys:

- `groups/{name}` → JSON Group record.
- `groups/members/{principal_id}/{group_name}` → empty value,
  serves as a reverse index for O(1) `GroupsFor` lookups. Both
  writes and deletes keep the two indices in sync in a single
  Badger transaction.

### 2. Resource shares

Each shareable resource gains a `Shares []Share` field. Stored
alongside `OwnerPrincipal` (feature 36) on `cpb.Job`,
`cpb.Workflow`, `registry.Dataset`, `registry.Model`.

```go
type Share struct {
    // Grantee is the principal ID the share is granted to.
    // May be a user principal ("user:bob") or a group reference
    // ("group:ml-research"). The two namespaces cannot collide
    // because of the typed prefix.
    Grantee string

    // Actions is the permitted action set on this resource for
    // this grantee. Enumerated (read/cancel/reveal/...); no
    // wildcards to keep the policy evaluator deterministic.
    Actions []authz.Action

    // GrantedBy is the principal ID of whoever added the share
    // (owner OR admin). Used for audit, not for decisions.
    GrantedBy string

    // GrantedAt is the timestamp the share was recorded.
    GrantedAt time.Time
}
```

Only the resource **owner** (or admin) can add / revoke shares.
This stays true even as shares pile up: a grantee who was given
read-share cannot re-share onward. Transitive delegation is a
slippery slope; revisit if teams ask.

### 3. Evaluator integration

`authz.Allow` gains one step between rule 6 (owner check) and
rule 7 (deny):

```go
// Rule 6b: Resource shares.
for _, s := range res.Shares {
    if matchesGrantee(p, s.Grantee) && contains(s.Actions, action) {
        return nil
    }
}

// matchesGrantee: exact principal match OR (grantee is
// "group:X" and p is in group X).
func matchesGrantee(p *principal.Principal, grantee string) bool {
    if p.ID == grantee {
        return true
    }
    if strings.HasPrefix(grantee, "group:") {
        groupName := strings.TrimPrefix(grantee, "group:")
        for _, g := range p.Groups {
            if g == groupName {
                return true
            }
        }
    }
    return false
}
```

Feature 35's `resolvePrincipal` is extended: after constructing
the Principal, it calls `groups.Store.GroupsFor(p.ID)` and
populates `p.Groups`. Lookup is O(1) via the reverse index; cost
is one Badger read per authenticated request. Measure; if it
becomes a hot-path bottleneck, cache per-principal for the
token's remaining TTL.

### 4. Management API

All admin-gated (`ActionAdmin` via feature 37):

```
POST   /admin/groups                                 {name}
GET    /admin/groups
POST   /admin/groups/{name}/members                  {principal_id}
DELETE /admin/groups/{name}/members/{principal}
DELETE /admin/groups/{name}                          # hard-delete
```

For shares, the **owner** (not only admin) can share their own
resources:

```
POST   /admin/resources/{kind}/{id}/share            {grantee, actions}
GET    /admin/resources/{kind}/{id}/shares
DELETE /admin/resources/{kind}/{id}/share/{grantee}
```

The admin-path prefix is a naming convention; the policy on
these endpoints is:

- `POST .../share` — allowed iff the caller is the resource
  owner OR has ActionAdmin.
- `GET .../shares` — allowed iff caller has ActionRead on the
  resource (same rule as reading the resource itself).
- `DELETE .../share/{grantee}` — same as POST.

Grants are idempotent: re-sharing the same (grantee, action)
pair returns 200 + the existing record. Revokes are idempotent:
removing an absent share returns 200.

### 5. Audit

New event types, each carrying principal + resource + action:

```
group_created           details: {name, created_by}
group_deleted           details: {name, deleted_by}
group_member_added      details: {name, principal_id, added_by}
group_member_removed    details: {name, principal_id, removed_by}
resource_shared         details: {resource_kind, resource_id, grantee, actions, granted_by}
resource_share_revoked  details: {resource_kind, resource_id, grantee, revoked_by}
```

The analytics auth-events panel (feature 28) renders each of
these as its own type so a dashboard viewer can answer "who
gained access to this workflow in the last 24h?" without
scanning raw audit keys.

### 6. Dashboard

A new `/admin/groups` route (admin-only) with two panels:

- **Groups** — list, create, delete; members column with
  add/remove buttons.
- **Resource shares** — filter by resource kind, show shares
  with grantee + actions + granted_by.

Per-resource detail views (job, workflow, dataset, model) gain
a "Shared with" section when the caller is the owner or admin.

## Security plan

| Threat | Control |
|---|---|
| A grantee re-shares a resource onwards, escalating access | Re-share endpoint checks owner-or-admin. Grantees cannot share. Test: `TestShare_GranteeCannotReshare`. |
| A deleted group leaves dangling shares referencing it | Group delete revokes every share whose Grantee is `group:{name}` in the same transaction. If the delete fails mid-way the scan retries on next admin request; deny-by-absent behaviour is safe. |
| A principal ID collides with a group name (user:ml-team vs group:ml-team) | The prefix namespace (`user:`, `operator:`, `group:`) makes collision impossible. The evaluator's `matchesGrantee` branches on the prefix. |
| An admin accidentally grants a dangerous action (e.g. ActionReveal on every job) | Every share emits `resource_shared` with full details; the analytics panel shows velocity. A retroactive review can spot unusual grants. |
| A user enumerates other users' resources by fuzzing share IDs | Enumeration needs the resource ID; `GET /admin/resources/{kind}/{id}/shares` runs through `Allow(ActionRead, resource)` first. A non-owner who doesn't have an existing share gets 403 before the share list leaks. |
| Group-lookup latency on every authenticated request | `GroupsFor` is O(1) via reverse index. Bench-measured in tests; budget is < 0.5 ms per request at the coordinator's current auth throughput. |
| A compromised share database lets an attacker read everyone's data | The share store is BadgerDB — same trust boundary as the rest of coordinator state. A compromised coordinator is already a full-cluster compromise (SECURITY.md §9.6). No new attack surface, just more valuable data in an already-protected store. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `internal/groups/` package with Store interface + BadgerDB impl + reverse index + tests. | features 35, 36, 37 | Medium |
| 2 | Add `Groups []string` to `Principal`; populate from Store in `resolvePrincipal`. | 1 | Small |
| 3 | Add `Shares []Share` to `cpb.Job`, `cpb.Workflow`, `registry.Dataset`, `registry.Model`. Persistence round-trips for free. | feature 36 | Small |
| 4 | `authz.Allow` rule 6b — matches grantee (including `group:` prefix). Extend the table-driven evaluator test from feature 37. | feature 37, 2, 3 | Small |
| 5 | Admin endpoints for group CRUD. Audit events. | 1 | Medium |
| 6 | Owner-or-admin endpoints for share CRUD on each resource kind. | 3, 5 | Medium |
| 7 | Dashboard admin/groups page + per-resource Shared-with panel. | 5, 6 | Medium |
| 8 | Docs: SECURITY.md §8.1, operator guide for delegation. | 1–7 | Trivial |

Each step can land independently once 1–3 are in. The
dashboard lags the backend by one release.

## Tests

Backend evaluator:

- `TestAllow_GroupShare_AllowsMember` — resource shared with
  `group:ml-team`, principal is a member → Allow returns nil.
- `TestAllow_GroupShare_RejectsNonMember` — same share,
  principal is NOT in ml-team → DenyError.
- `TestAllow_DirectShare_AllowsGrantee` — share with
  `user:bob`, Bob's principal → Allow.
- `TestAllow_ShareActionScoped` — share grants only
  `ActionRead`; Bob attempts `ActionCancel` → DenyError.
- `TestAllow_GranteeCannotReshare` — Bob has a read-share on
  Alice's workflow; Bob calls `POST .../share` → 403.

Group store:

- `TestGroups_CreateGetList_RoundTrip`.
- `TestGroups_AddRemoveMember_PopulatesReverseIndex`.
- `TestGroups_Delete_RevokesDanglingShares`.
- `TestGroupsFor_FastLookup` — 1000 groups, principal in 5 →
  lookup returns exactly those 5 and no others.

Share store / endpoints:

- `TestShareResource_OwnerOrAdmin` — non-owner + non-admin
  gets 403. Owner + admin both succeed.
- `TestShareResource_Idempotent` — same (grantee, actions)
  posted twice → 200 both times; one record.
- `TestRevokeShare_Idempotent` — revoke a non-existent share
  → 200.
- `TestShareResource_AuditEmit` — every create + revoke
  produces the matching audit event.

Integration:

- `TestWorkflowSharedWithGroup` — Alice creates a workflow,
  shares with `group:ml-team`, Bob (ml-team member) reads it
  successfully.
- `TestWorkflowShareRevoke_KicksExistingAccess` — revoke →
  Bob's subsequent `GET` returns 403 within the current JWT
  TTL (share check runs per request; the revoke takes effect
  immediately).

## Open questions

- **Nested groups?** No in v1. "ml-research contains
  ml-training and ml-inference" is out of scope; users can
  add all three groups as separate shares. Revisit if a
  deployment asks.
- **TTL on shares?** Not in v1. An auto-expiring share would
  be useful for "covering while on vacation" but needs a
  background expiry cron and UX affordance. Revisit as a
  follow-up.
- **Share counts per resource cap?** Cap at 32 per resource;
  beyond that an admin creates a group instead. Documented +
  enforced in the share endpoint.

## Deferred

- **Hierarchical groups.** `group:ml-research` contains
  `group:ml-training`. Implementable but scope-creep.
- **Auto-expiring shares.** Share with a TTL. Needs a cron.
- **Audit-scoped shares.** "Bob can read Alice's resources
  but only the audit log for them." Attribute-level; large
  scope creep.
- **Share-by-role.** "Anyone with `role:ml-reviewer` can
  read." Roles are already a JWT concept; conflating
  RBAC-by-role with ABAC-by-share is confusing. Either a
  group membership OR a direct share covers the use case.

## Implementation status

_Implemented 2026-04-19._

### What shipped

- New `internal/groups/` package: `Group` struct, `Store`
  interface, BadgerDB-backed impl with a reverse-index key
  layout (`groups/members/<principal_id>\x1f<group>`) for O(1)
  `GroupsFor` lookups. MemStore fake for tests. Name +
  principal-ID validators reject path-traversal and unprefixed
  identifiers at the boundary.
- New `authz.Share` type + `ValidateShare` validator. Grantee
  supports `user:`, `operator:`, `group:`, `service:`, `job:`
  prefixes (not `anonymous`, not `ActionAdmin`). Per-resource
  cap of `MaxSharesPerResource = 32`.
- `Shares []authz.Share` added to `cpb.Job`, `cpb.Workflow`,
  `registry.Dataset`, `registry.Model`. Persistence
  round-trips for free (additive JSON field).
- `authz.Allow` rule 6b — `matchesGrantee` + action-scope
  check. Runs AFTER the owner check so happy-path (owner
  reading own resource) stays a single comparison. Legacy-
  sentinel short-circuit stays BEFORE rule 6b so shares on a
  `legacy:`-owned record are inert for non-admins.
- `authMiddleware` populates `Principal.Groups` via
  `groups.Store.GroupsFor()` on every authenticated request.
  Store failures log at Warn (do not block auth); a missed
  lookup just makes `group:<name>` shares inert until recovery.
- Group CRUD endpoints (admin-only):
  `POST/GET/DELETE /admin/groups`, member add/remove,
  `GET /admin/groups/{name}`. Every mutation emits a
  `group_*` audit event.
- Share CRUD endpoints (owner-or-admin):
  `POST/GET/DELETE /admin/resources/{kind}/share?id=<id>`.
  Supports `job`, `workflow`, `dataset`, `model` kinds.
  Share mutations idempotent (last-writer-wins on same
  grantee). Every create / revoke emits a `resource_shared` /
  `resource_share_revoked` audit event; non-owner attempts
  emit `authz_deny` with the same context.
- `JobStore.UpdateShares`, `WorkflowStore.UpdateShares`,
  `registry.DatasetStore.UpdateDatasetShares`,
  `registry.ModelStore.UpdateModelShares` — targeted mutation
  primitives that preserve every other field of the record
  (ownership-preservation invariant from feature 36).
- Handlers updated to pass resource `Shares` into the
  `authz.Resource` construction so rule 6b can fire on
  read/cancel/delete paths across jobs, workflows, datasets,
  and models.
- Coordinator wiring: `SetGroupsStore(groupspkg.NewBadgerStore
  (persister.DB()))` in `cmd/helion-coordinator/main.go`.

### Deviations from plan

- **No dashboard UI in this slice.** Backend API + audit
  wiring is complete; the admin-dashboard `/admin/groups`
  route and the per-resource "Shared with" panel are a
  follow-up. The HTTP endpoints are stable, so the UI can
  ship independently.
- **ServiceEndpoint doesn't carry its own Shares.** Service
  endpoints inherit the owning Job's `OwnerPrincipal` (feature
  36) and the share check runs against the Service resource
  kind with shares inherited from the job at handler-load
  time. Adding a separate Shares field on `cpb.ServiceEndpoint`
  would double-track access for no gain.
- **Group delete does NOT sweep dangling shares on resources.**
  Shares referencing a deleted group become inert (principal
  Groups list can no longer contain the name) so feature 37's
  rule 6b never matches them. Hard cleanup is left to admins
  who iterate shares via the share endpoints. A "sweep on
  delete" pass would require scanning every resource kind,
  which is scope-creep.

### Tests added

- `internal/groups/groups_test.go` — matrix-tested against
  MemStore AND BadgerStore:
  - CRUD round-trip
  - Duplicate / invalid-name / invalid-principal rejection
  - AddMember / RemoveMember idempotency + reverse-index sync
  - Delete cleans reverse index
  - GroupsFor prefix-scan isolation (user:a vs user:ab)
- `internal/authz/authz_test.go` (new table rows):
  - `TestAllow_DirectShare_AllowsGrantee`
  - `TestAllow_ShareActionScoped`
  - `TestAllow_GroupShare_AllowsMember`
  - `TestAllow_GroupShare_RejectsNonMember`
  - `TestAllow_ShareOnLegacyOwner_StillDeniedForNonAdmin`
  - `TestValidateShare_RejectsMalformed`
  - `TestValidateShare_AcceptsValid`
- `internal/api/shares_integration_test.go`:
  - `TestShares_WorkflowSharedWithGroup_MemberCanRead` —
    end-to-end: create group, submit workflow, share with
    group, member reads, revoke, next read fails.
  - `TestShares_NonOwner_CannotShare` — escalation-via-share
    refused with 403 + audit.
  - `TestShares_DirectShare_AllowsGrantee`
  - `TestShares_ReadShare_DoesNotGrantCancel`
  - `TestShares_CreateEmitsAuditEvent` — resource_shared event
    shape.
  - `TestShares_GroupLifecycle_EndToEnd` — admin create +
    member add/remove + delete.
  - `TestShares_RejectsBeyondCap` — 33rd share 400s.
