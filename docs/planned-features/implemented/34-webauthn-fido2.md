# Feature: WebAuthn / FIDO2 for dashboard operators

**Priority:** P2
**Status:** Implemented (2026-04-20)
**Affected files:**
`internal/webauthn/` (new package — types, MemStore, BadgerStore,
SessionStore, Tier),
`internal/api/handlers_webauthn.go` (new — six admin handlers for
register/login/list/revoke ceremonies),
`internal/api/server.go` (`SetWebAuthn` + `SetWebAuthnTier` wiring),
`internal/api/middleware.go` (tier enforcement + bootstrap-route
exemption),
`internal/auth/jwt.go` (`Claims.AuthMethod` + `TokenClaims` +
`GenerateTokenWithClaims`),
`internal/audit/logger.go` (six new webauthn event constants),
`cmd/helion-coordinator/main.go` (env-driven configuration),
`dashboard/src/app/shared/models/index.ts` (7 WebAuthn wire types),
`dashboard/src/app/core/services/api.service.ts` (6 WebAuthn methods),
`docs/SECURITY.md` (§9.12 + mTLS §9.6 cross-ref),
`docs/ops/operator-webauthn-guide.md` (new onboarding walk-through).

## Problem

Feature 27 ships optional browser mTLS. The spec called out a
remaining gap, flagged as unsolvable:

> **It does not protect against a compromised browser process.**
> If the attacker is running code inside the operator's Chrome,
> they can use the installed cert directly. mTLS is a
> network-boundary control, not an in-browser one.

This is a real hole. A malicious extension, a compromised
dependency loaded into a dashboard script (supply-chain attack),
or a remote-code-exec against the browser process can all use
the installed client cert to sign whatever the attacker wants.
The key sits in the browser's keychain; the browser is the
attacker's runtime.

**WebAuthn / FIDO2 fixes this.** The private key for a WebAuthn
credential lives in a hardware device (YubiKey, Apple Secure
Enclave, Windows Hello TPM) that requires physical user
interaction — a button press, fingerprint, or face scan — for
each signature. Malicious code running in the browser can ASK
for a signature, but the hardware refuses to sign without the
user's physical action. A compromised browser can no longer
silently authenticate requests.

WebAuthn pairs naturally with feature 27 (both move beyond
"pasted JWT") and with feature 33 (cert CN ↔ credential ID
binding gives true per-operator attribution).

## Current state

- No WebAuthn plumbing today.
- Feature 27's mTLS is the strongest identity primitive; a
  compromised browser defeats it.
- Go ecosystem has solid WebAuthn libraries
  (`github.com/go-webauthn/webauthn`).

## Design

### 1. Registration flow

Operator Alice:

1. Logs in via JWT (as today) or mTLS (feature 27).
2. Hits a new `/admin/webauthn/register-begin` endpoint, which
   returns a WebAuthn registration challenge.
3. Browser invokes `navigator.credentials.create(...)` — the
   user touches their YubiKey / authenticates with Touch ID.
4. Browser POSTs the attestation to
   `/admin/webauthn/register-finish`.
5. Coordinator verifies the attestation, stores
   `(credential_id, public_key, operator_cn, aaguid)` in
   BadgerDB under `webauthn/<credential_id>`.

### 2. Authentication flow

For every request that should require WebAuthn (configurable):

1. Dashboard requests `/admin/webauthn/login-begin`; coordinator
   returns a challenge.
2. Browser invokes `navigator.credentials.get(...)`; user
   touches the key.
3. Browser POSTs the assertion to `/admin/webauthn/login-finish`;
   coordinator verifies against the stored public key and mints
   a short-lived **WebAuthn-backed JWT** (claim
   `auth_method: webauthn`, TTL 15 minutes).

### 3. Gating (integrates with feature 27)

```
HELION_AUTH_WEBAUTHN_REQUIRED = off / warn / on   # same tier model as feature 27
```

- `off` — current behaviour.
- `warn` — log requests that didn't present a WebAuthn token.
- `on` — admin-role endpoints refuse tokens where
  `auth_method != webauthn`.

### 4. Binding to operator cert (pairs with feature 33)

When a WebAuthn credential is registered, optionally bind it
to the active mTLS cert CN. Then:

- WebAuthn-minted JWT carries `required_cn: alice@ops`
  (feature 33).
- Dashboard must present: mTLS cert + WebAuthn assertion + JWT.
- Attacker needs: (1) cert private key, (2) YubiKey with
  user's physical touch, (3) valid JWT.

That's three factors on three different substrates.

### 5. Storage schema

```
webauthn/credentials/<credential_id>   -> CredentialRecord
webauthn/by-operator/<cn>/<cred_id>    -> pointer (for list-by-operator)
```

Credentials never expire on their own; admin revokes via a
parallel `DELETE /admin/webauthn/credentials/<id>` endpoint
(audited).

## Security plan

| Threat | Control |
|---|---|
| Malicious browser extension signs requests with the operator's mTLS cert | WebAuthn key is in hardware; extension can only INVOKE signing, the signature still requires user touch. |
| Supply-chain attack on a dashboard dependency | Same: any code path that tries to authenticate without user touch is refused by the authenticator. |
| User trains themselves to ignore the touch prompt | Out of scope — can't prevent behavioural mistakes. WebAuthn is a step up, not an absolute. |
| YubiKey lost / stolen | Admin revokes the credential (same surface as feature 31's cert revocation, different key namespace). A lost YubiKey without a PIN is still somewhat protected by FIDO2 user-verification attestation. |
| WebAuthn public keys exfiltrated from BadgerDB | Public keys are, well, public — no secret to leak. The attacker still can't mint assertions without the hardware device. |
| Attacker registers their own YubiKey to Alice's account | Registration is gated by adminMiddleware + must carry Alice's existing JWT + (optionally) Alice's cert CN. An attacker with Alice's JWT is already in; WebAuthn is the thing that prevents that from happening. Registration is a one-time elevation — recommend admins register multiple hardware keys so losing one doesn't mean account recovery. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | Add `go-webauthn/webauthn` dep + wire `webauthn.Config` at coordinator boot. | — | Small |
| 2 | Storage schema + `CredentialStore` + unit tests. | 1 | Medium |
| 3 | `/admin/webauthn/register-begin` + `register-finish`. | 2 | Medium |
| 4 | `/admin/webauthn/login-begin` + `login-finish` → WebAuthn-minted JWT. | 2 | Medium |
| 5 | `HELION_AUTH_WEBAUTHN_REQUIRED` env + authMiddleware enforcement tier. | 4 | Small |
| 6 | Dashboard credentials-registration wizard. | 3 | Medium |
| 7 | Dashboard login flow prompts for WebAuthn assertion. | 4 | Medium |
| 8 | (When feature 33 is live) Optional cert-CN binding on the credential record. | feature 33 | Small |
| 9 | Ops guide: Chrome / Safari / Firefox / YubiKey registration walk-through. | 3-7 | Small |

## Tests

Backend:

- `TestRegister_BeginProducesChallenge` — `register-begin` returns
  a challenge with the configured RPID + RPName.
- `TestRegister_FinishStoresCredential` — finish a simulated
  attestation; the credential lands in BadgerDB.
- `TestLogin_WrongCredentialID_Rejected` — login-finish with a
  credential_id that isn't stored → 401.
- `TestLogin_ReplayAttack_Rejected` — signCount in the second
  assertion must exceed the first; otherwise 401 (FIDO2
  replay-protection requirement).
- `TestAuthMiddleware_WebAuthnRequired_PlainJWT_Returns401` —
  `on` mode; plain JWT without `auth_method: webauthn` → 401.

Dashboard:

- `TestWebAuthnRegister_PromptsUser` — component calls
  `navigator.credentials.create` with the server's challenge.
- `TestWebAuthnLogin_UserCancels_RendersError` — operator
  dismisses the touch prompt; the UI renders "authentication
  cancelled" without swallowing the error.

## Acceptance criteria

1. Admin Alice registers her YubiKey via the dashboard wizard;
   a credential record lands in BadgerDB.
2. A subsequent login from a fresh browser session requires
   Alice to touch the YubiKey; the resulting JWT carries
   `auth_method: webauthn`.
3. With `HELION_AUTH_WEBAUTHN_REQUIRED=on`, a bearer-only JWT
   (no WebAuthn) is refused at 401 on every admin endpoint.
4. Alice loses her YubiKey; another admin revokes her
   credential via `DELETE /admin/webauthn/credentials/<id>`;
   subsequent login attempts with the old device fail.
5. Audit log carries `webauthn_registered` +
   `webauthn_authenticated` + `webauthn_revoked` events.

## Deferred

- **Passkeys (cross-device FIDO2 credentials).** Apple/Google
  passkey sync adds UX but also syncs the credential across the
  user's iCloud/Google accounts — which reintroduces a
  "compromise the cloud, compromise the credential" story. Out
  of scope until that sync story is deployable with per-tenant
  controls.
- **User-verification requirement (PIN or biometric on the
  authenticator).** Can set `UserVerification: required` on the
  options; defer until the operator-guide path is stable.

## Implementation status

_Implemented 2026-04-20._ Promoted from feature 27's "what mTLS
does NOT solve" note on 2026-04-19; shipped one day later.

### What shipped

- **`internal/webauthn` package** (new). Wraps
  `github.com/go-webauthn/webauthn` v0.16.5 with Helion-shaped
  storage + session + tier primitives:
  - `CredentialRecord` — embeds `webauthnlib.Credential`,
    adds `UserHandle []byte`, `OperatorCN string`,
    `BoundCertCN string`, `Label string`, `RegisteredAt`,
    `RegisteredBy string`, `LastUsedAt time.Time`.
  - `CredentialStore` interface with two impls:
    - `MemStore` — RWMutex-guarded map.
    - `BadgerStore` — shared coordinator DB; primary
      `webauthn/credentials/<b64url_cred_id>` + reverse
      `webauthn/by-operator/<b64url_user_handle>\x1f<b64url_cred_id>`.
  - `SessionStore` — TTL-indexed, keyed on
    `(subject, purpose)` with `Put`/`Pop`/`Sweep`; default
    5-minute TTL matches WebAuthn spec recommendations.
  - `Tier` enum (`TierOff` / `TierWarn` / `TierOn`) + parser
    for `HELION_AUTH_WEBAUTHN_REQUIRED`.
  - `UserHandleFor(subject)` = SHA-256(subject), giving a
    stable 32-byte user handle without leaking the raw
    subject to the authenticator.
  - `verifyNotReplay(oldCount, newCount)` — enforces the
    FIDO2 sign-counter invariant (new > old) with the
    spec-blessed "both zero → skip check" accommodation.
- **`auth.Claims.AuthMethod`** (new, `omitempty`). Set to
  `"webauthn"` on WebAuthn-minted JWTs; absent on
  bearer/cert-only flows.
- **`auth.TokenClaims` struct + `GenerateTokenWithClaims`.**
  Existing `GenerateToken` / `GenerateTokenWithCN` delegate to
  it, so 30+ call sites stayed source-compatible. Carries
  subject, role, TTL, optional `RequiredCN` (feature 33),
  optional `AuthMethod` (feature 34).
- **Six admin handlers** at `/admin/webauthn/...`:
  - `POST register-begin` — creates a webauthnlib
    `BeginRegistration` ceremony; returns challenge +
    stashes SessionData keyed on `(subject, "register")`.
  - `POST register-finish` — verifies the attestation,
    persists a `CredentialRecord` (including optional
    `bind_to_cert_cn` carried via `stashRegisterMetadata`).
  - `POST login-begin` / `POST login-finish` — full
    assertion ceremony; `login-finish` mints a 15-minute
    JWT with `auth_method: "webauthn"` and carries the
    credential's `BoundCertCN` forward as `required_cn`
    (feature 33 pairing). Updates sign-counter +
    `LastUsedAt`.
  - `GET /admin/webauthn/credentials` — admin listing:
    credential_id, operator_cn, label, AAGUID,
    registered_at/by, bound_cert_cn, last_used_at.
  - `DELETE /admin/webauthn/credentials/{id}` — admin
    revocation; audited.
- **Tier enforcement in `adminMiddleware`.**
  `HELION_AUTH_WEBAUTHN_REQUIRED=on` refuses any admin
  request whose JWT lacks `auth_method: "webauthn"` with
  401. `warn` audits (`EventWebAuthnRequired`) without
  blocking. The four bootstrap ceremony routes
  (`register-begin`/`register-finish`/`login-begin`/`login-finish`)
  are exempted so operators can register their first key
  under an `on` policy; `isWebAuthnBootstrapPath(path)` is
  the gate.
- **Audit events.** `EventWebAuthnRegistered`,
  `EventWebAuthnRegisterReject`, `EventWebAuthnAuthenticated`,
  `EventWebAuthnLoginReject`, `EventWebAuthnRevoked`,
  `EventWebAuthnRequired`.
- **Coordinator boot (`cmd/helion-coordinator/main.go`).**
  Reads `HELION_WEBAUTHN_RPID`, `HELION_WEBAUTHN_DISPLAY`,
  `HELION_WEBAUTHN_ORIGINS`. Missing RPID or ORIGINS ⇒
  feature stays disabled cleanly (no fatal); malformed
  `HELION_AUTH_WEBAUTHN_REQUIRED` is fatal so typos don't
  silently downgrade the tier.
- **Dashboard wire.**
  `WebAuthnRegisterBeginRequest/Response`,
  `WebAuthnLoginBeginResponse`, `WebAuthnLoginFinishResponse`,
  `WebAuthnCredentialItem`, `WebAuthnCredentialsListResponse`,
  `WebAuthnRevokeRequest` TypeScript interfaces +
  `ApiService.webauthnRegisterBegin/Finish`,
  `webauthnLoginBegin/Finish`, `listWebAuthnCredentials`,
  `revokeWebAuthnCredential` methods + 6 HttpTestingController
  specs. Dashboard suite is 366/366 green.
- **SECURITY.md §9.12 (new)** — endpoints table, tier
  semantics (off/warn/on + bootstrap exemption), safety
  properties, configuration env vars, BadgerDB layout,
  known limitations. §9.6 mTLS section + the threat table
  row ("Compromised browser process") updated to reflect
  the shipped mitigation.
- **Operator guide** (`docs/ops/operator-webauthn-guide.md`,
  new) — YubiKey + platform-authenticator registration,
  curl-driven ceremonies, tier rollout staging, recovery
  playbook.

### Deviations from plan

- **Dashboard login UI deferred.** The TypeScript wire +
  API methods shipped so a follow-up slice can wire the
  component, but the existing bearer-JWT login page was
  not rewritten into a WebAuthn step-up. Today operators
  drive `register-*` / `login-*` ceremonies via `curl` or
  the companion CLI; tier stays `off` or `warn` until
  that UX lands. Documented in SECURITY.md §9.12 "Known
  limitations".
- **Attestation MDS (metadata service) verification
  deferred.** go-webauthn can verify attestation format
  signatures but we do not yet pin AAGUIDs to an
  allow-list from the FIDO MDS blob. Accepted for v1 —
  the threat model is "operator's browser is hostile", not
  "operator plugs in a fake YubiKey".
- **User-verification (`UserVerification: required`)
  deferred.** Current ceremony accepts `preferred`, which
  matches most common YubiKey configurations. Bumping to
  `required` forces a PIN/biometric on every touch; moved
  to the follow-up guide so operators can opt in per
  deployment.
- **Passkeys (cross-device FIDO2 credentials) deferred** —
  cloud-sync story reintroduces a "compromise the cloud,
  compromise the credential" path that we don't want in
  v1. Matches the spec's own "Deferred" call-out.
- **Full register-finish verification test left to
  manual walk-through.** Simulating a real FIDO2
  authenticator inside Go unit tests would require
  reimplementing the library's COSE+CBOR attestation
  parsing. Handler tests cover admin-gating, session
  TTL, list/revoke, and tier enforcement; the full
  attestation round-trip is exercised via the operator
  guide's physical YubiKey walk-through.

### Tests added

- `internal/webauthn/webauthn_test.go` — matrix tests
  against both `MemStore` and `BadgerStore`:
  - `TestCredentialStore_CreateGetList` — round-trip +
    `ListByOperator` + `List` parity across both stores.
  - `TestCredentialStore_Delete_RemovesReverseIndex`
    (BadgerStore) — reverse index is cleaned up on delete.
  - `TestCredentialStore_UpdateSignCount_Monotonic` —
    sign-counter writes are persisted + replay-safe via
    `verifyNotReplay`.
  - `TestSessionStore_PutPopTTL` — session data is
    single-use and expires after TTL.
  - `TestSessionStore_Sweep` — expired sessions are
    garbage-collected.
  - `TestUserHandleFor_Deterministic` — user handle is a
    stable SHA-256 of the subject.
  - `TestParseTier` — case-insensitive `off`/`warn`/`on`
    parsing + error on typos.

- `internal/api/handlers_webauthn_test.go` — 14
  integration tests against a real webauthnlib config
  (RPID=`localhost`):
  - `TestRegisterBegin_ProducesChallenge` — challenge
    shape + RPID + RPName match configured values.
  - `TestRegisterBegin_NonAdmin_Forbidden` — user-role
    JWT → 403.
  - `TestRegisterFinish_StaleSession_BadRequest` —
    missing/expired session → 400 + audit reject event.
  - `TestLoginBegin_ProducesChallenge` — assertion
    challenge + allow-list of registered credentials.
  - `TestLoginBegin_NoCredentials_Ok` — empty allow-list
    still returns 200 (authenticators present fallback
    UI).
  - `TestListCredentials_Admin_Ok` / `_NonAdmin_Forbidden`
    / `_EmptyList_OkEmpty`.
  - `TestRevokeCredential_Admin_Ok` / `_NonAdmin_Forbidden`
    / `_NotFound_404`.
  - `TestWebAuthnTier_Off_BearerTokenAllowed` — tier off
    is back-compat.
  - `TestWebAuthnTier_Warn_BearerTokenAllowedWithAudit` —
    warn audits but passes.
  - `TestWebAuthnTier_On_BearerTokenDenied` — bearer JWT
    (no `auth_method`) → 401.
  - `TestWebAuthnTier_On_WebAuthnMintedTokenAllowed` —
    token with `auth_method: "webauthn"` bypasses the
    gate.
  - `TestWebAuthnTier_On_BootstrapRoutesAlwaysAllowed` —
    register/login ceremonies stay bearer-callable even
    on tier `on`.

- `internal/auth/jwt_test.go` (extended):
  - `TestGenerateTokenWithClaims_StampsAuthMethod` —
    WebAuthn-minted tokens round-trip the `auth_method`
    claim through Validate.
  - `TestGenerateTokenWithClaims_OmitsAuthMethodWhenEmpty`
    — back-compat; unset claim is absent from the JSON.

- `dashboard/src/app/core/services/api.service.spec.ts`:
  - `webauthnRegisterBegin()` / `webauthnRegisterFinish()`
    — POST shapes.
  - `webauthnLoginBegin()` / `webauthnLoginFinish()` —
    POST shapes.
  - `listWebAuthnCredentials()` — GET.
  - `revokeWebAuthnCredential()` — DELETE.

All dashboard tests pass (366/366). Go suite on the
touched packages (`internal/webauthn`, `internal/api`,
`internal/auth`, `internal/audit`) is green under
`go test -race -count=1`.
