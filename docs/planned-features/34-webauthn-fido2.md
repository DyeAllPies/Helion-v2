# Feature: WebAuthn / FIDO2 for dashboard operators

**Priority:** P2
**Status:** Pending
**Affected files:**
`internal/api/webauthn.go` (new — register + login + challenge
verification),
`internal/api/handlers_admin.go` (bind WebAuthn credentials to
admin tokens),
`dashboard/src/app/features/auth/` (new WebAuthn prompt flow),
`docs/SECURITY.md` (extend §9.6 — the hardware-bound-key story),
`docs/ops/webauthn-guide.md` (new — YubiKey / platform authenticator
onboarding).

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

_Not started. Promoted from feature 27's "what mTLS does NOT
solve" note — the compromised-browser-process concern is
addressable with hardware-bound keys. Promoted on 2026-04-19._
