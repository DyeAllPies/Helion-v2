# Feature: Principal model & auth-to-principal resolution

**Priority:** P1
**Status:** Implemented (2026-04-19).
**Affected files:**
`internal/principal/principal.go` (new — Principal type + Kind
enum + constructors + context helpers + IsAdmin + ParseID),
`internal/principal/principal_test.go` (new — 16 cases),
`internal/api/middleware.go` (authMiddleware resolves + stamps
a Principal; `resolvePrincipalFromClaims` maps role → Kind),
`internal/api/operator_cert.go` (clientCertMiddleware stamps a
KindOperator principal; authMiddleware preserves it),
`internal/api/handlers_analytics.go` (`actorFromContext` prefers
`principal.FromContext(ctx).DisplayName`; falls back to claims),
`internal/audit/logger.go` (new `Principal` + `PrincipalKind`
fields on Event; `Log` populates from ctx; new
`stampServiceIfMissing` helper for the lifecycle audit calls),
`internal/grpcserver/handlers.go` (Register / Heartbeat /
ReportResult / ReportServiceEvent stamp Node principals into
their ctx),
`dashboard/src/app/shared/models/index.ts` (AuditEvent gains
`principal` + `principal_kind` + AuditPrincipalKind type alias),
`dashboard/src/app/features/audit/audit-log.component.ts`
(principal-kind pill rendered before actor),
`internal/api/principal_integration_test.go` (new — 6
integration cases),
`docs/SECURITY.md` (new §5a),
`docs/ARCHITECTURE.md` (new §12a).

## Reconciliation (spec vs shipped)

- **Scope matches the spec.** Every implementation-order step
  (1–8) landed as described. Resolver precedence, service-
  principal vars, audit schema change, dashboard badge,
  documentation — all in place.
- **`actor` field shape preserved.** The spec hinted at a
  rename; shipped form keeps `Event.Actor` as the bare-string
  legacy shape so existing tests (`logger_test.go:245` on
  "system", `handlers_jobs_test.go:747` on "alice") pass
  unchanged. The new typed identity rides alongside in
  `Event.Principal` + `Event.PrincipalKind`. Post-feature-36 a
  follow-up can demote `Actor` to a computed alias.
- **`stampServiceIfMissing` helper added** (not in the spec)
  so coordinator-internal audit helpers
  (LogJobStateTransition / LogCoordinatorStart /
  LogCoordinatorStop) default-stamp the right service
  Principal when called with a plain `context.Background()`.
  Cleaner than mandating every call site plumb a principal
  through, and preserves the old literal `actor="system"`
  field on the event. Caller-stamped Principals (e.g.
  `ServiceDispatcher`) are respected — the helper only fires
  when ctx carries no Principal or Anonymous.
- **`ServiceCoordinator` principal added** (not enumerated in
  the spec's exact list but needed for coordinator lifecycle
  events that don't fit the per-loop names). Sits alongside
  `ServiceDispatcher`, `ServiceWorkflowRunner`,
  `ServiceRetryLoop`, `ServiceLogIngester`, `ServiceRetention`,
  `ServiceLogReconciler`.
- **No breaking changes to node-JWT path.** Role="node" on a
  JWT (legacy bootstrap path) resolves to `KindNode`, and
  `IsAdmin()` hard-refuses admin on Node kind even if Role is
  somehow "admin". This is the load-bearing defence behind
  feature 37's "nodes denied on REST actions" rule.
- **Claims context key kept.** Handlers still read
  `*auth.Claims` where they need role + JTI etc.; Principal
  lives alongside. Removing the Claims read is a feature
  37/38 concern.

## Problem

Helion today has at least four different ways to identify "who is
doing this":

- HTTP handlers read `claims.Subject` from the JWT.
- gRPC handlers read `nodeID` from the registered certificate.
- Feature 27's `clientCertMiddleware` stamps `operator_cn` into
  the context as a separate string.
- System-initiated actions (dispatch loop, workflow runner,
  retry loop, log ingester) identify themselves as the literal
  string `"system"` in audit events.

Every one of those is a string, and every downstream consumer has
to know which string format it's looking at. An audit log row
with `actor=alice` could be either a user, an operator cert CN,
or a node ID that happens to collide. The `handleGetJob` RBAC
check at [AUDIT L1](../../internal/api/handlers_jobs.go) compares
`claims.Subject != job.SubmittedBy` — that works only because
node tokens happen to never appear as SubmittedBy, which is
accidental, not enforced.

Adding feature 36 (resource ownership across jobs/workflows/etc.)
and feature 37 (authorization policy) needs a foundation: a
**single typed concept** that names who is acting, whether the
action came from the REST listener, the gRPC listener, or a
coordinator-internal goroutine.

The missing concept is **Principal**.

## Current state

- [`internal/auth/`](../../internal/auth/) holds `Claims` with
  `Subject` + `Role` + standard JWT fields. There is no type
  that unifies a Claims-derived identity with a cert-CN-derived
  identity or a node-ID-derived identity.
- [`internal/api/middleware.go`](../../internal/api/middleware.go)
  stores `*auth.Claims` in the context under `claimsContextKey`.
- [`internal/api/operator_cert.go`](../../internal/api/operator_cert.go)
  stores the client-cert CN in the context under `operatorCNKey`
  (feature 27).
- [`internal/grpcserver/handlers.go`](../../internal/grpcserver/handlers.go)
  consumes the `evt.NodeId` field directly from proto messages;
  the node registry enforces the node→cert binding at the TLS
  layer, but there's no type inside the coordinator that says
  "this RPC came from node X".
- Every call site that wants to stamp an audit event or an
  ownership field has to construct the right string for the
  right identity source, by hand.

## Design

### 1. The `Principal` type

New package `internal/principal/`:

```go
package principal

type Kind string

const (
    KindUser      Kind = "user"      // human via JWT
    KindOperator  Kind = "operator"  // human via client cert (feature 27)
    KindNode      Kind = "node"      // registered node, mTLS-auth'd
    KindService   Kind = "service"   // coordinator-internal actor
    KindJob       Kind = "job"       // workflow-issued job-role token
    KindAnonymous Kind = "anonymous" // no auth (dev mode)
)

type Principal struct {
    // ID is the globally-unique, stable handle. Format is
    // kind-dependent but always prefixed with the kind so a
    // reader never has to guess:
    //
    //   user:<jwt_subject>        — e.g. user:alice
    //   operator:<cert_cn>        — e.g. operator:alice@ops
    //   node:<node_id>            — e.g. node:gpu-01
    //   service:<service_name>    — e.g. service:dispatcher
    //   job:<workflow_id>         — e.g. job:wf-42
    //   anonymous                 — exact literal
    ID string

    // Kind is the enum above; derivable from ID by splitting on
    // ':' but stored for zero-allocation comparisons in the
    // authz hot path (feature 37).
    Kind Kind

    // DisplayName is free-form for UI / audit detail rendering.
    // Not used for identity decisions. Typically equals the
    // ID's suffix when no richer display info is available.
    DisplayName string

    // Role is the JWT role claim for Kind=user/operator/job
    // principals (admin / node / job). Node/service principals
    // ignore this field. Kept here so the admin-short-circuit
    // in authz doesn't have to dip back into the original
    // Claims struct; redundant by design.
    Role string
}
```

### 2. Resolution at every auth surface

One resolver per authentication surface. Each returns a
`*Principal` constructed from whatever the surface already
trusts:

```go
// internal/api/middleware.go
//
// resolvePrincipal is the single place that turns "whatever
// authenticated this request" into a Principal.
//
// Priority:
//
//   1. A verified client cert (feature 27) → KindOperator.
//   2. JWT claims → KindUser (admin/user role) or KindJob (job
//      role) or KindNode (node role).
//   3. Neither → KindAnonymous (only reachable when DisableAuth
//      is set in tests).
func (s *Server) resolvePrincipal(r *http.Request, claims *auth.Claims) *principal.Principal {
    // Cert-CN wins when feature 27 is in `on` mode because it's
    // a strictly stronger identity than the JWT alone. When
    // both are present the cert CN is the primary ID; the JWT
    // subject is kept as metadata for debugging.
    if cn := OperatorCNFromContext(r.Context()); cn != "" {
        return &principal.Principal{
            ID:          "operator:" + cn,
            Kind:        principal.KindOperator,
            DisplayName: cn,
            Role:        roleOr(claims, "admin"),
        }
    }
    if claims != nil {
        return &principal.Principal{
            ID:          principalIDForRole(claims.Role, claims.Subject),
            Kind:        kindForRole(claims.Role),
            DisplayName: claims.Subject,
            Role:        claims.Role,
        }
    }
    return principal.Anonymous()
}
```

gRPC-side (nodes):

```go
// internal/grpcserver/handlers.go
func nodePrincipal(nodeID string) *principal.Principal {
    return &principal.Principal{
        ID:          "node:" + nodeID,
        Kind:        principal.KindNode,
        DisplayName: nodeID,
    }
}
```

Coordinator-internal (dispatch loop, workflow runner, retry loop,
log ingester, analytics retention cron) each have a stable
service principal:

```go
var (
    ServiceDispatcher    = principal.Service("dispatcher")
    ServiceWorkflowRunner = principal.Service("workflow_runner")
    ServiceRetryLoop     = principal.Service("retry_loop")
    ServiceLogIngester   = principal.Service("log_ingester")
    ServiceRetention     = principal.Service("retention")
)
```

### 3. Context plumbing

A new context key:

```go
type principalKey struct{}

func NewContext(parent context.Context, p *Principal) context.Context
func FromContext(ctx context.Context) *Principal  // never nil; returns Anonymous() on miss
```

Every handler that used to read `claims.Subject` switches to
`principal.FromContext(ctx).ID`. The old `claimsContextKey` stays
for now — the JWT role check in `adminMiddleware` still needs it
until feature 37 replaces it.

### 4. Audit integration

`audit.Event` already has an `Actor string` field. We add a
parallel `Principal string` field (the full `kind:id` form) and
keep `Actor` for back-compat during migration. New events set
both; old code paths that still pass a bare subject fill only
`Actor`. Post-migration, `Actor` becomes a computed alias for
`Principal`'s suffix and a future slice deprecates it.

A new detail field `principal_kind` lets the dashboard's audit
view render a distinct badge per kind (user / operator / node /
service / job / anonymous).

## Security plan

| Threat | Control |
|---|---|
| A node-role token is used to submit a job, ends up with `Subject=node-01` as SubmittedBy, passes the `Subject == SubmittedBy` check | Principals carry Kind; the ownership check in feature 36 compares `job.OwnerPrincipal == p.ID` where `p.ID` includes the `kind:` prefix. A node principal's ID is `node:node-01`, which will never match a user principal's `user:node-01` even if the subject strings collide. |
| A new auth surface (e.g., WebAuthn from feature 34) identifies users under a different scheme and bypasses existing checks | resolvePrincipal is the single seam. Adding a new surface adds one case to the resolver; every authz check downstream sees a well-formed Principal regardless of origin. |
| An anonymous (dev-mode) request accidentally runs in production | `KindAnonymous` is distinct; feature 37's policy evaluator will refuse every non-trivial action for an anonymous principal. The current `DisableAuth` behaviour (which effectively allows anything) is pinned to tests only. |
| Coordinator-internal goroutine mislabels itself | Service principals are package-level vars constructed at startup. A call site that forgets to stamp one lands the request with `Anonymous`, which feature 37 denies — loud fail rather than silent over-authorisation. |

No new crypto surface. No new wire format (Principal is an
in-memory type derived from existing auth material). Audit event
schema gains one field; old consumers that ignore unknown fields
are unaffected.

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `internal/principal/` package with type + Kind enum + `Anonymous()` + `Service(name)` + `NewContext`/`FromContext` + tests. | — | Small |
| 2 | `resolvePrincipal` in `internal/api/middleware.go`. Plumb into `authMiddleware` so every JWT-gated handler gets a Principal in context. | 1 | Small |
| 3 | Node-side: `nodePrincipal(nodeID)` helper in `internal/grpcserver/`. Every RPC handler that already resolves the node ID stamps a Principal into the context. | 1 | Small |
| 4 | Coordinator-internal service principals: package-level vars, passed explicitly to the loops that previously hard-coded `"system"`. | 1 | Small |
| 5 | Replace `claims.Subject` reads with `principal.FromContext(ctx).ID` across `handlers_jobs.go`, `handlers_workflows.go`, `handlers_admin.go`, `handlers_registry.go`. NOT touching the ownership comparison yet — that's feature 36. Just the audit-actor reads. | 1–4 | Medium |
| 6 | Audit event schema: add `Principal` field to `audit.Event` + populate from context at every `audit.Log` call site. `Actor` kept as back-compat alias. | 5 | Small |
| 7 | Dashboard: `AuditEvent` TypeScript type gains `principal` + `principal_kind` fields; audit log view renders a kind badge. | 6 | Small |
| 8 | Docs: SECURITY.md §6 (new), ARCHITECTURE.md diagram. | 1–7 | Trivial |

Each step is an independent PR. Steps 2–6 can ship in any order
after 1; step 7 lands last because it reads schema from 6.

## Tests

- `TestPrincipal_IDFormats` — every Kind produces a well-formed
  `kind:suffix` ID; parsing round-trips.
- `TestPrincipal_Anonymous_HasStableID` — `Anonymous().ID ==
  "anonymous"` exact match.
- `TestPrincipal_Service_UsesServicePrefix` —
  `Service("dispatcher").ID == "service:dispatcher"`.
- `TestResolvePrincipal_CertCNBeatsJWT` — when both a verified
  cert CN and a JWT subject are present, the returned Principal
  is KindOperator with the cert CN as the ID suffix.
- `TestResolvePrincipal_JWTOnly` — cert-less request: resolver
  returns KindUser / KindJob / KindNode based on role.
- `TestResolvePrincipal_DisableAuth` — auth disabled in the
  test Server: resolver returns KindAnonymous, NOT nil.
- `TestAuditEvent_CarriesPrincipalKind` — a submit-job call made
  with a KindOperator principal produces an audit event whose
  `principal_kind == "operator"`.
- `TestFromContext_NeverReturnsNil` — regression guard: a
  context that was never populated returns `Anonymous()`, not
  nil, so handlers don't need to nil-check.

## Open questions

- **Should `Anonymous()` be a package-level singleton or a fresh
  struct each call?** Singleton is cheaper but accidentally
  mutating `DisplayName` on one call site would leak into all
  others. Resolved during implementation; default is singleton
  with an explicit "don't mutate" comment, fallback is fresh on
  each call if someone finds a mutation bug.

## Deferred

None. This slice is the smallest shippable foundation — split
any further and the slices no longer stand alone.

## Implementation status

_Not started. Planned 2026-04-19 as part of the IAM foundation
discussed in features 35–38._
