> **Audience:** engineers + operators
> **Scope:** JWT, rate limiting, REST surface, Principal model, resource ownership, authorization policy, groups + shares.
> **Depth:** reference

# Security — authentication and authorization

Backend identity and policy: JWT lifecycle, per-subject and per-node rate
limits, the REST trust boundary, and the feature-35-to-38 identity stack
(Principal → Ownership → Authorization → Groups). For operator-facing auth
layers (browser mTLS, token ↔ cert-CN binding, WebAuthn), see
[operator-auth.md](operator-auth.md).

## 1. JWT

HS256 with 15-minute expiry (normal) or 10-year expiry (root, rotated on
every coordinator restart). JTI-based revocation via
`DELETE /admin/tokens/{jti}` with sub-second latency. Full reference,
including token-issuance examples and revocation flow, is in the
operator-oriented guide:
[operators/jwt.md](../operators/jwt.md).

## 2. Rate limiting

### Per-node submit limiter

Each node has an independent token-bucket rate limiter in the coordinator.

| Property | Value |
|---|---|
| Default rate | 10 jobs/s per node |
| Algorithm | Token bucket (allows short bursts up to the rate limit) |
| Configuration | `HELION_RATE_LIMIT_RPS` environment variable |
| gRPC status on limit hit | `ResourceExhausted` |
| Audit event | `rate_limit_hit` |

Applied at two levels:

1. gRPC unary interceptor — `Register` and `ReportResult` RPCs.
2. Heartbeat handler — streaming RPCs bypass unary interceptors; rate
   limit is checked per heartbeat message.

### Analytics API rate limiting

The `/api/analytics/*` endpoints have a per-subject limiter because their
queries (`PERCENTILE_CONT`, `ORDER BY` on `job_summary`) are expensive as
data grows.

| Property | Value |
|---|---|
| Rate | 5 queries/sec per JWT subject |
| Burst | 60 |
| Sustained cap | ~300 queries/min per subject |
| HTTP status on limit hit | `429 Too Many Requests` |
| Body | `{"error":"analytics query rate limit exceeded"}` |
| Keyed on | JWT `sub` claim |

Sizing: the dashboard loads 7 panels in parallel and polls every 2 s (dev)
/ 5 s (prod), ~3.5 rps steady per viewer. Burst 60 absorbs rapid
navigation; sustained 5 rps keeps polling headroom while staying well
under what a DoS attack would need. Rate-limited requests are rejected
BEFORE audit so abusive traffic doesn't flood the audit log. See
`internal/api/middleware.go:analyticsQueryAllow`.

### Registry API rate limiting

`/api/datasets` and `/api/models` share the same per-subject shape.
Writes are cheap BadgerDB puts, but an authenticated user could flood the
audit log or chew through disk.

| Property | Value |
|---|---|
| Rate | 2 req/s per JWT subject |
| Burst | 30 |
| HTTP status on limit hit | 429 |

Every mutation lands in the audit log with the subject as actor, the
dataset/model (name, version), and the URI for register events.

**Registry authorization model (current):** any authenticated user can
register, read, or delete any entry. No per-entry owner check — flat by
design; tightening to admin-only or owner-only delete is a deliberate
future step (tracked in feature 10 umbrella).

### Input validation (registry)

Rejected before any disk write:

- `name`, `version` — k8s-shaped (`[a-z0-9._-]`, bounded length).
- `uri` — scheme allowlist (`file://`, `s3://`). `http(s)://` rejected so
  a caller can't wire the registry into an SSRF chain on downstream
  consumers.
- `metrics` — `NaN` / `+Inf` / `-Inf` rejected. JSON can smuggle these
  via `1e400` which parses as `+Inf`.
- `tags` — k8s label bounds (32 entries, 63-char keys, 253-char values,
  printable-ASCII, no NUL).
- `source_dataset` — partial pointers (name without version or vice
  versa) rejected. A lineage pointer is either complete or absent.

URI existence is NOT checked at register time: the registry is
metadata-only, and coupling the two would require artifact-store
credentials on the coordinator. Deletion of a dataset also does not
cascade to models that reference it — lineage becomes soft.

### Load test

```bash
for i in {1..1000}; do helion-run echo "job $i" & done
wait
# First ~10 jobs succeed (burst); sustained rate limited to 10 jobs/s.
# Check audit:
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?type=rate_limit_hit"
```

## 3. REST API surface

### Authentication middleware

All endpoints except `/healthz` and `/readyz` require a valid JWT:

```
Authorization: Bearer <token>
```

On missing or invalid token: `401 Unauthorized`.

### Security endpoints

| Endpoint | Auth | Description |
|---|---|---|
| `POST /admin/nodes/{id}/revoke` | Required | Revoke a node certificate |
| `GET /audit` | Required | Query audit log |
| `GET /healthz` | None | Liveness probe |
| `GET /readyz` | None | Readiness probe — 200 after BadgerDB open + node registered |

## 4. Principal model (feature 35)

Every authenticated request, every gRPC call from a registered node, and
every coordinator-internal loop carries a typed `*principal.Principal` in
its Go context. The Principal is the single identity primitive that
features 36–38 evaluate against.

### Kinds

| Kind | ID format | How it gets stamped |
|---|---|---|
| `user` | `user:<jwt_subject>` | `authMiddleware` after JWT validation for a non-node, non-job role. |
| `operator` | `operator:<cert_cn>` | `clientCertMiddleware` after verifying a client certificate (feature 27). Wins over JWT resolution — cert is strictly stronger. |
| `node` | `node:<node_id>` | gRPC handlers after the node ID is known (`Register`, `Heartbeat` on first message, `ReportResult`, `ReportServiceEvent`). mTLS handshake verified the cert. |
| `service` | `service:<name>` | Coordinator-internal loops (`dispatcher`, `workflow_runner`, `retry_loop`, `log_ingester`, `retention`, `log_reconciler`, `coordinator`). Audit helpers default-stamp `service:coordinator`. |
| `job` | `job:<jwt_subject>` | JWT with `role=job` — workflow-scoped tokens minted by `submit.py`. |
| `anonymous` | `anonymous` | Dev-mode when `Server.DisableAuth` is set. Feature 37 denies non-trivial actions on anonymous. |

### Safety properties

- **IDs are prefix-qualified.** A node registered as `alice` produces
  `node:alice`; a user with the same JWT subject produces `user:alice`.
  Collisions across kinds are impossible.
- **Cert wins over JWT.** When `HELION_REST_CLIENT_CERT_REQUIRED` is
  active and a verified client cert is present, `authMiddleware` does
  NOT overwrite the operator principal with a user principal derived
  from the accompanying JWT. Cert CN stays primary.
- **Node never admin.** `Principal.IsAdmin()` returns `false` for
  `KindNode` regardless of `Role` — guard against a compromised node
  forging a node-JWT with `role=admin`. Same for `KindService` and
  `KindAnonymous`.
- **`FromContext` never returns nil.** A context without a Principal
  reads back as `Anonymous()`; handlers never need a nil check.
- **Audit events carry both forms.** `Event.Actor` stays the legacy bare
  string for back-compat; `Event.Principal` + `Event.PrincipalKind`
  carry the typed identity.

### What feature 35 does NOT do

- **Not authorization.** The Principal names *who* is acting; feature
  37's policy engine decides *what* they may do.
- **Not re-verification of auth material.** Feature 35 reads what the
  auth surface already trusts. A compromised JWT still produces a
  well-formed Principal — blast radius is identical to "the attacker
  has the JWT".
- **No persistence.** Principals are derived from request-scoped auth
  material. Feature 38 adds a lookup to enrich `Principal.Groups`.

See [`internal/principal/principal.go`](../../internal/principal/principal.go)
for the types and
[planned-features/implemented/35-principal-model.md](../planned-features/implemented/35-principal-model.md)
for the slice reconciliation.

## 5. Resource ownership (feature 36)

Every persisted stateful type carries a single authoritative owner field
— `OwnerPrincipal`, formatted as the feature-35 principal ID that
created it (`user:alice`, `operator:alice@ops`, `service:workflow_runner`,
or the `legacy:` sentinel for pre-feature-36 records).

### Types with an owner

| Type | Stamped by |
|---|---|
| `cpb.Job` | `handleSubmitJob` stamps `principal.FromContext(ctx).ID` at create. Workflow-materialised jobs inherit `Workflow.OwnerPrincipal`. |
| `cpb.Workflow` | `handleSubmitWorkflow` stamps `principal.FromContext(ctx).ID`. |
| `registry.Dataset` / `registry.Model` | `handleRegisterDataset` / `handleRegisterModel`. |
| `cpb.ServiceEndpoint` | gRPC `ReportServiceEvent` reads the owning `cpb.Job`'s `OwnerPrincipal` and passes it to `services.Upsert` on first `ready`. |

### Safety properties

- **Immutable after creation.** Every state transition, retry, and
  cancel path preserves `OwnerPrincipal`. `service:retry_loop` re-driving
  a failed user job does NOT transfer ownership to the retry loop —
  the loop is the actor in audit, but the owner stays the original
  submitter. Guarded by
  `TestOwnerPrincipal_JobSubmitPersistsAndSurvivesTransitions`.
- **Legacy fail-closed.** Records persisted before feature 36 backfill
  on load: `SubmittedBy=<sub>` → `user:<sub>`; missing both legacy
  proxies → `principal.LegacyOwnerID` (`legacy:`). Feature 37 treats
  `legacy:`-owned resources as admin-only.
- **Audit distinguishes actor from owner.** Create-path events
  (`job_submit`, `workflow_dry_run`, `dataset.registered`,
  `model.registered`) include `resource_owner` alongside `actor`.
- **Back-compat aliases.** `SubmittedBy` on `cpb.Job` and `CreatedBy` on
  registry types stay on the wire for one release.

### What feature 36 does NOT do

- **Not authorization.** Provides the field feature 37 compares against
  the Principal. Existing RBAC checks (`claims.Subject ==
  job.SubmittedBy`) remain until feature 37 lands.
- **No ownership transfer.** A `/chown`-style endpoint is deferred;
  feature 38's shares cover real delegation without breaking
  immutability.
- **No multi-owner.** Revisit if a team-scoped use case appears.

Spec: [planned-features/implemented/36-resource-ownership.md](../planned-features/implemented/36-resource-ownership.md).

## 6. Authorization policy (feature 37)

Every authz decision funnels through one function:
`authz.Allow(principal, action, resource)`. The evaluator is pure,
table-driven, and fails closed on every unexpected input. Handlers that
mutate or read a resource load it from the store, construct an
`*authz.Resource`, and call `Allow` before serving. Denials produce a
403 with a stable machine-readable `code` and emit `authz_deny`.

### Actions

| Action | Endpoint examples |
|---|---|
| `read` | `GET /jobs/{id}`, `/workflows/{id}`, `/api/datasets/.../{name}/{version}` |
| `list` | `GET /jobs`, `/workflows`, `/api/datasets`, `/api/models`, `/api/services` |
| `write` | `POST /jobs`, `/workflows`, `/api/datasets`, `/api/models` |
| `cancel` | `POST /jobs/{id}/cancel`, `DELETE /workflows/{id}` |
| `delete` | `DELETE /api/datasets/...`, `DELETE /api/models/...` |
| `reveal` | `POST /admin/jobs/{id}/reveal-secret` |
| `admin` | `POST /admin/nodes/{id}/revoke`, `/admin/tokens`, `/admin/operator-certs` |

### Rule precedence

1. `nil` Principal → deny (`nil_principal`).
2. `Kind=user` or `Kind=operator` with `Role=admin` → allow every action
   (break-glass).
3. `Kind=node` → deny on every REST action. A compromised node's
   mTLS-derived JWT cannot stand up fake jobs or read user-owned
   workflows via REST. Nodes still act on the internal gRPC surface.
4. `Kind=service` → narrow per-service allow-list in
   `internal/authz/rules.go`. `service:retry_loop` can cancel jobs it's
   retrying; `service:dispatcher` can read/cancel jobs it's dispatching.
   A new service needing an action goes through code review.
5. `Kind=job` → workflow-scoped tokens may only read jobs belonging to
   the same workflow (the token's subject IS the workflow ID). No
   write/cancel/delete.
6. `Kind=user` or `Kind=operator` (non-admin) → allow iff
   `p.ID == res.OwnerPrincipal` (owner check).
7. `Kind=anonymous` → deny everywhere.
8. Unknown kind → deny (`unknown_kind`).

Resources with `OwnerPrincipal == "legacy:"` are admin-only. Resources
of `Kind=system` require admin.

### Denial codes

403 body:

```json
{"error": "forbidden", "code": "not_owner"}
```

Codes: `nil_principal`, `nil_resource`, `anonymous_denied`,
`not_owner`, `legacy_owner_admin_only`, `node_not_allowed`,
`service_not_allowed`, `job_scope_mismatch`, `admin_required`,
`system_non_admin_action`, `unknown_kind`.

### Audit emission

Every deny emits `EventAuthzDeny` with the deny code, attempted action,
resource kind + id + owner, requesting principal, and request path. A
sudden drop in `authz_deny` volume after a deploy is an alert signal —
the policy engine may have silently widened. Distinct from
`EventAuthFailure` (401 — bad/missing JWT).

### List-endpoint filtering

`list` endpoints (jobs, workflows, datasets, models, services) fetch
the full matching set and filter per-row through
`authz.Allow(ActionRead)` before paginating. A non-admin caller sees
only their own resources; admin sees everything. Per-row denials do
NOT audit — the filter is expected behaviour, not a security event.

A scope-push-down (store-level owner filter) is deferred until
deployments hit the filter-in-memory cliff (>10k active resources).

### DisableAuth + dev mode

`Server.DisableAuth()` stamps a synthetic `dev-admin` Principal on
every request (`KindUser` with `Role=admin`). Keeps the authz path
identical between dev and prod — no bypass branches inside the
evaluator — and produces an unambiguous audit signal
(`principal == user:dev-admin-disableauth`) if DisableAuth ever leaks
into a prod binary.

Spec: [planned-features/implemented/37-authorization-policy.md](../planned-features/implemented/37-authorization-policy.md).

## 7. Groups and resource shares (feature 38)

Feature 37 gives admin-or-owner. Feature 38 adds two orthogonal
delegation primitives that widen access without concentrating blast
radius on the admin role:

  1. **Groups** — named, flat collections of Principal IDs. A group
     `ml-team` with members `user:alice`, `user:bob`,
     `operator:carol@ops` is a single identifier.
  2. **Shares** — per-resource grants naming a grantee (direct principal
     OR `group:<name>` reference) and an enumerated Action set.

### Safety properties

- **Non-transitive.** A grantee with `ActionRead` cannot re-share.
  Share-mutation endpoints require owner OR `ActionAdmin`.
- **Typed namespace.** `user:`, `operator:`, `group:`, etc. are
  prefix-qualified; a principal ID cannot collide with a group name.
- **Flat groups.** v1 does NOT support groups-of-groups — recursion
  risk without a concrete use case.
- **Action-scoped.** `[read]` does NOT grant cancel / delete / reveal.
  Rule 6b checks `containsAction(share.Actions, action)` before
  allowing.
- **Legacy-sentinel still wins.** Resources with owner `legacy:` stay
  admin-only regardless of shares — legacy fail-closed runs BEFORE
  rule 6b.
- **Admin cannot share `ActionAdmin`.** `ValidateShare` rejects any
  share whose Actions include `ActionAdmin`. Admin is a kind-level
  role, not a per-resource capability.
- **Per-resource cap.** `MaxSharesPerResource = 32`; beyond that
  returns 400 with a hint to use a group grantee.
- **Name validation.** Group names `[a-zA-Z0-9._-]{1,64}` and must
  not start with `.` (defence against path-traversal key shapes).
- **Admin-only management.** Group create/delete/member-edit require
  `ActionAdmin`. Share-mutation requires owner-or-admin.

### Management API

```
POST   /admin/groups                          {name}
GET    /admin/groups                          -> [Group...]
GET    /admin/groups/{name}                   -> Group
DELETE /admin/groups/{name}
POST   /admin/groups/{name}/members           {principal_id}
DELETE /admin/groups/{name}/members/{id...}

POST   /admin/resources/{kind}/share?id=<id>
       body: {grantee, actions}
GET    /admin/resources/{kind}/shares?id=<id>
DELETE /admin/resources/{kind}/share?id=<id>&grantee=<id>
```

Resource kinds: `job`, `workflow`, `dataset`, `model`. Registry
resource ids ride in `id` as `name/version`.

Share mutations are idempotent:

- POST same `(grantee, actions)` twice → single record.
- POST same grantee with different actions → actions replaced
  (last-writer-wins).
- DELETE an absent grantee → 204.

### Audit events

`group_created`, `group_deleted`, `group_member_added`,
`group_member_removed`, `resource_shared`,
`resource_share_revoked`. Share-escalation attempts emit
`authz_deny` with rich context.

### Principal resolution

`authMiddleware` populates `Principal.Groups` at every authenticated
request via `GroupsFor(p.ID)` — O(1) via reverse-index prefix scan.
Store failures log at Warn and leave `Groups` nil (inert `group:`
shares until the store recovers). Deployments without a groups store
skip the lookup; direct principal shares still work.

Spec: [planned-features/implemented/38-groups-and-shares.md](../planned-features/implemented/38-groups-and-shares.md).
