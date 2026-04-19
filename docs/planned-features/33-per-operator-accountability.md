# Feature: Per-operator accountability (JWT ↔ cert CN binding)

**Priority:** P2
**Status:** Pending
**Affected files:**
`internal/auth/token.go` (new claim: `required_cn`),
`internal/api/middleware.go` (authMiddleware cross-checks the
token's `required_cn` against the request's verified operator CN),
`internal/api/handlers_admin.go` (`POST /admin/tokens` gains an
optional `bind_to_cert_cn` field),
`internal/api/handlers_admin.go` (audit on binding enforcement
failures),
`docs/SECURITY.md` (extend §9.6 + §4).

## Problem

Feature 27's mTLS raises the authentication bar (an attacker needs
BOTH a JWT AND a client-cert keypair). But the audit story is
still weak:

- Every operator's JWT carries `role: admin` (or `node`, or
  `job`) and `subject: <user>`. There is no hard binding between
  the JWT subject and the cert CN.
- An admin can mint tokens for themselves and hand them around.
  A leaked token used with someone else's browser still gets in.
  The audit record shows the JWT subject, but the cert CN (added
  in feature 27) may not match — and we don't currently reject
  that mismatch; we just record it.
- `operator_cn` is a useful audit detail but not an enforcement
  primitive.

The missing piece: when admin Alice issues a JWT for Bob,
optionally **bind** that JWT to Bob's specific cert CN, so the
JWT is useless in any other browser even if stolen.

## Current state

- `auth.Claims` has `Subject`, `Role`, `JTI`, standard JWT fields.
  No cert-CN claim.
- `authMiddleware` validates the JWT signature + JTI revocation
  state + expiry + role. No cross-check with `operator_cn`.
- feature 27 stamps `operator_cn` on audit events but the
  middleware does not reject mismatches — a leaked token used
  from any operator's browser still succeeds.

## Design

### 1. Optional `required_cn` claim

```go
// internal/auth/token.go
type Claims struct {
    jwt.RegisteredClaims
    Subject     string `json:"sub"`
    Role        string `json:"role"`
    JTI         string `json:"jti"`
    RequiredCN  string `json:"required_cn,omitempty"` // feature 33
}
```

When `RequiredCN` is empty, the token behaves exactly as today
(back-compat). When non-empty, the middleware enforces:

```
if claims.RequiredCN != "" && claims.RequiredCN != OperatorCNFromContext(ctx) {
    writeError(w, 401, "token bound to cert CN %q does not match request cert %q")
    emit audit "token_cert_cn_mismatch"
    return
}
```

### 2. Token-issuance opt-in

`POST /admin/tokens` gains an optional `bind_to_cert_cn` field:

```json
{
  "subject": "alice",
  "role": "admin",
  "ttl_hours": 8,
  "bind_to_cert_cn": "alice@ops"
}
```

When set, the resulting JWT carries `required_cn: alice@ops`. The
admin issuing the token explicitly locks it to a specific
operator cert CN.

### 3. Default-on for admin dashboards (hardening)

After rollout stabilises, the dashboard's "login" flow (which
pastes a JWT) can require that the token's `required_cn` match
the cert CN the dashboard's client-cert middleware saw on the
underlying TLS connection. The dashboard refuses to start a
session if the claim is missing — forcing the safer binding as
the default for interactive use.

### 4. Migration story

Phase in per environment:

- **Phase 1:** ship the claim + enforcement; issuance is opt-in.
  No existing tokens break.
- **Phase 2:** rotate known admin tokens with `bind_to_cert_cn`
  set; deprecate bearer-only admin workflows.
- **Phase 3:** dashboard login path refuses to accept a token
  without `required_cn` when the coordinator is in
  `HELION_REST_CLIENT_CERT_REQUIRED=on`.

## Security plan

| Threat | Control |
|---|---|
| Admin Alice's JWT leaks; attacker uses Bob's cert-mTLS session | `required_cn` in the JWT doesn't match Bob's cert CN → 401 + audit event. Attacker also needs Alice's cert to succeed. |
| Admin Alice mints a token for Bob but forgets `bind_to_cert_cn` | Opt-in; admin carries the responsibility. Dashboard UI prompts for the binding when on `HELION_REST_CLIENT_CERT_REQUIRED=on`. |
| Claim-tampering: attacker edits `required_cn` in a signed token | JWT signature protects all claims; modifying any claim invalidates the signature and authMiddleware rejects the token. |
| Feature 27 is off; `required_cn` claim is meaningless | When the request has no verified CN, a non-empty `required_cn` fails-closed: 401. The admin either turns on mTLS or doesn't set the claim. |
| Node-role tokens affected by this (breaks node → coordinator RPCs) | `required_cn` is opt-in; node bootstrapping pipelines do not set it. `adminMiddleware`'s existing role check keeps node tokens on the non-admin surface. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `RequiredCN` claim + `TokenManager` wire. Back-compat: absent claim = unchanged behaviour. | — | Small |
| 2 | `authMiddleware` cross-check + `token_cert_cn_mismatch` audit event. | 1 | Small |
| 3 | `POST /admin/tokens` accepts `bind_to_cert_cn`. | 1 | Small |
| 4 | Dashboard issue-token form gets a `bind_to_cert_cn` field. | 3 | Small |
| 5 | (Phase 3) Dashboard login refuses unbound tokens when coordinator is in `on` mode. | 4 | Small |
| 6 | SECURITY.md + operator-cert-guide updates. | 1-5 | Trivial |

## Tests

- `TestAuthMiddleware_RequiredCN_MatchesCertCN_Succeeds` — JWT
  carries `required_cn: alice`; request arrives with verified CN
  `alice` → 200.
- `TestAuthMiddleware_RequiredCN_MismatchesCertCN_Returns401` —
  JWT says `alice`, cert says `bob` → 401 + audit event.
- `TestAuthMiddleware_RequiredCN_NoCert_Returns401` — JWT has
  `required_cn: alice`, request arrives cert-less → 401.
- `TestAuthMiddleware_RequiredCNEmpty_NoChange` — JWT has no
  `required_cn`; cert CN unchecked (back-compat).
- `TestIssueToken_WithBinding_StampsClaim` — admin issues a token
  with `bind_to_cert_cn: alice@ops`; decoded claim contains it.
- `TestAuditEntry_CertCNMismatch` — a mismatch attempt produces
  `token_cert_cn_mismatch` with both the claim CN and the
  actual cert CN in detail.

## Acceptance criteria

1. An admin issues a token via
   `POST /admin/tokens` with `bind_to_cert_cn: alice@ops`.
2. Alice imports her P12 into her browser; pastes the token into
   the dashboard; a subsequent `POST /jobs` succeeds (cert CN
   matches).
3. Bob imports HIS P12; tries to paste Alice's token into HIS
   browser; `POST /jobs` fails at 401 with error body
   mentioning the CN mismatch.
4. `GET /audit?type=token_cert_cn_mismatch` shows the attempt.
5. Legacy tokens without `required_cn` still work unchanged.

## Deferred

- **Multiple CNs per token.** A power-admin token that binds to
  a SET of CNs. YAGNI — single CN matches the dashboard flow; a
  power-admin that accesses from multiple workstations mints one
  token per workstation.
- **CN-glob matching** (`alice@*` matches both `alice@ops` and
  `alice@dev`). Nice ergonomics; falls into "security-via-glob
  is risky" — defer until we have real operator data.

## Implementation status

_Not started. Promoted from feature 22's + feature 27's deferred
items on 2026-04-19._
