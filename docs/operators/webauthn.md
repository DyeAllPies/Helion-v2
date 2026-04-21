> **Audience:** operators
> **Scope:** Register hardware authenticators and run the step-up login flow (feature 34).
> **Depth:** runbook

# Operator WebAuthn / FIDO2 Guide (feature 34 — hardware-bound auth)

This guide walks an operator through registering a hardware
authenticator (YubiKey, Windows Hello TPM, Apple Touch ID /
Secure Enclave) against the coordinator, exercising the login
ceremony, and validating that
`HELION_AUTH_WEBAUTHN_REQUIRED=on` refuses admin requests that
don't carry a WebAuthn-minted JWT.

See [security/operator-auth.md § 3](../security/operator-auth.md#3-webauthn--fido2-feature-34) for the threat model and
safety properties this guide backs. Pairs with feature 27
(browser mTLS) and feature 33 (per-operator cert-CN binding).

## Prerequisites

- An admin JWT for the coordinator (`HELION_TOKEN`).
- A hardware authenticator:
  - **YubiKey 5** series (USB-A / USB-C / NFC / Lightning) — works
    cross-platform.
  - **Apple** Touch ID / Face ID on macOS 13+ or iPhone/iPad.
  - **Windows Hello** on Windows 10/11 with a TPM 2.0 device.
  - **Android** 7+ phone acting as a platform authenticator.
- A browser with WebAuthn support: Chrome 67+, Safari 14+,
  Firefox 60+, Edge 79+.
- Coordinator deployed with `HELION_WEBAUTHN_RPID` and
  `HELION_WEBAUTHN_ORIGINS` set (see §1 below).

## 1. Configure the coordinator

WebAuthn needs three env vars at coordinator boot:

```sh
# Relying Party ID — the public domain the dashboard serves from.
# MUST match the hostname the browser is connected to when the
# ceremony runs. "localhost" is fine for local dev.
export HELION_WEBAUTHN_RPID="dashboard.helion.internal"

# Human-readable label shown to the authenticator UI.
export HELION_WEBAUTHN_DISPLAY="Helion Coordinator"

# Comma-separated list of allowed origins. Authenticator will
# refuse ceremonies from any other origin — this is the main
# phishing defence.
export HELION_WEBAUTHN_ORIGINS="https://dashboard.helion.internal"

# Enforcement tier — starts at off for rollout.
export HELION_AUTH_WEBAUTHN_REQUIRED=off   # off | warn | on
```

Restart the coordinator. If `HELION_WEBAUTHN_RPID` or
`HELION_WEBAUTHN_ORIGINS` are unset, WebAuthn stays disabled
cleanly — the ceremony endpoints return `503` and admin
middleware treats the tier as `off`.

Verify the feature is live:

```sh
curl -fsS -H "Authorization: Bearer $HELION_TOKEN" \
     https://coord.helion.internal:8080/admin/webauthn/credentials
# → {"credentials": []}
```

A `503` here means the env vars weren't picked up; check the
coordinator log for `webauthn disabled: RPID or ORIGINS missing`.

## 2. Register a hardware key (admin-gated)

Registration is a two-round ceremony: `register-begin` returns a
challenge; the browser passes it to the authenticator; the
attestation comes back via `register-finish`. A short SessionData
lives server-side between the two (5-minute TTL).

### 2a. Begin the ceremony

```sh
curl -fsS \
  -H "Authorization: Bearer $HELION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"operator_cn":"alice@ops","label":"alice-yubikey-5c","bind_to_cert_cn":"alice@ops"}' \
  https://coord.helion.internal:8080/admin/webauthn/register-begin
```

Response shape:

```json
{
  "publicKey": { "challenge": "...", "rp": {...}, "user": {...}, ... }
}
```

`operator_cn` is the human-readable binding; `label` is operator
metadata (which key is this?); `bind_to_cert_cn` is optional and
locks the credential to a specific mTLS cert CN (feature 33).

### 2b. Finish via the browser

Today the dashboard does not yet render a registration wizard —
the TypeScript wire is shipped
(`ApiService.webauthnRegisterBegin/Finish`) but the component is
a follow-up slice. Until then, drive the ceremony from a small
helper page or from the browser DevTools console on the
dashboard origin:

```js
// On the dashboard origin (https://dashboard.helion.internal)
const begin = await fetch('/admin/webauthn/register-begin', {
  method: 'POST',
  headers: {
    'Authorization': 'Bearer <admin-jwt>',
    'Content-Type': 'application/json',
  },
  body: JSON.stringify({ operator_cn: 'alice@ops', label: 'alice-yubikey-5c' }),
}).then(r => r.json());

// Browser coerces the challenge + user_id from base64url strings.
const options = begin.publicKey;
options.challenge = Uint8Array.from(atob(options.challenge.replace(/-/g,'+').replace(/_/g,'/')), c => c.charCodeAt(0));
options.user.id   = Uint8Array.from(atob(options.user.id.replace(/-/g,'+').replace(/_/g,'/')),   c => c.charCodeAt(0));
for (const c of (options.excludeCredentials ?? [])) {
  c.id = Uint8Array.from(atob(c.id.replace(/-/g,'+').replace(/_/g,'/')), c => c.charCodeAt(0));
}

const cred = await navigator.credentials.create({ publicKey: options });
// Touch the YubiKey / Touch ID / Windows Hello NOW.

// Finish the ceremony.
const attestation = {
  id: cred.id,
  rawId: btoa(String.fromCharCode(...new Uint8Array(cred.rawId))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,''),
  type: cred.type,
  response: {
    attestationObject: btoa(String.fromCharCode(...new Uint8Array(cred.response.attestationObject))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,''),
    clientDataJSON:    btoa(String.fromCharCode(...new Uint8Array(cred.response.clientDataJSON))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,''),
  },
};
await fetch('/admin/webauthn/register-finish', {
  method: 'POST',
  headers: {
    'Authorization': 'Bearer <admin-jwt>',
    'Content-Type': 'application/json',
  },
  body: JSON.stringify({ attestation }),
}).then(r => r.json());
```

Success response:

```json
{
  "credential_id": "aB1c...",
  "operator_cn":  "alice@ops",
  "aaguid":       "ee882879-721c-4913-9775-3dfcce97072a"
}
```

That's the key's permanent identifier (AAGUID is the YubiKey model,
not a serial). The audit log picks up an
`webauthn_registered` event with `subject` + `operator_cn` +
`aaguid`.

### 2c. Register backup keys

**Register at least two hardware keys per operator.** Losing a
single YubiKey without a backup requires admin-triggered
credential rotation + a fresh registration round. The spec's
acceptance criteria call this out explicitly.

## 3. Authenticate via WebAuthn

The login ceremony mirrors registration, with
`/admin/webauthn/login-begin` + `/admin/webauthn/login-finish`.
`login-finish` mints a **fresh 15-minute JWT** whose `auth_method`
claim is `"webauthn"`. Use that JWT for every admin request that
you want to run under the hardware-backed identity.

```sh
curl -fsS \
  -H "Authorization: Bearer $HELION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"subject":"alice@ops"}' \
  https://coord.helion.internal:8080/admin/webauthn/login-begin
```

Returns a challenge + the operator's registered credential IDs.
Pass through `navigator.credentials.get({publicKey: ...})` in the
browser, touch the key, POST the assertion to
`/admin/webauthn/login-finish`:

```json
{
  "token": "eyJhbG...",
  "expires_at": "2026-04-20T15:12:00Z",
  "auth_method": "webauthn",
  "required_cn": "alice@ops"
}
```

Decoded, the token's claims include `auth_method: "webauthn"`.
Compare with a bearer-minted token:

```sh
# Decode payload (dev-only; never log real tokens):
python3 -c 'import base64,json,sys; p = sys.argv[1].split(".")[1]; p += "=" * (-len(p)%4); print(json.dumps(json.loads(base64.urlsafe_b64decode(p)), indent=2))' "$TOKEN"
```

## 4. Exercise the enforcement tier

Stage the rollout in three steps.

### 4a. `warn` — observe, don't block

```sh
export HELION_AUTH_WEBAUTHN_REQUIRED=warn
```

Every admin request without `auth_method: "webauthn"` emits an
`EventWebAuthnRequired` audit entry but still succeeds. Watch
the audit tail for a week — this surfaces every tool that
hasn't been rotated to WebAuthn-minted tokens yet:

```sh
curl -fsS -H "Authorization: Bearer $HELION_TOKEN" \
  https://coord.helion.internal:8080/admin/audit?event=webauthn_required \
  | jq '.events[] | {subject, path, remote}'
```

### 4b. `on` — refuse

```sh
export HELION_AUTH_WEBAUTHN_REQUIRED=on
```

Admin endpoints now require a WebAuthn-minted JWT. Bearer-only
tokens get a 401 with the same body shape as a signature
failure (deliberate — no information leak about which layer
rejected). The WebAuthn **bootstrap** routes
(`register-begin`/`register-finish`/`login-begin`/`login-finish`)
stay bearer-callable so new operators can still register their
first key under `on`.

Smoke test:

```sh
# Bearer JWT (auth_method=absent) against a regular admin route:
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $HELION_TOKEN" \
  https://coord.helion.internal:8080/admin/tokens
# Expect: 401

# WebAuthn-minted token from step 3:
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $WEBAUTHN_TOKEN" \
  https://coord.helion.internal:8080/admin/tokens
# Expect: 200
```

## 5. Revoke a lost key

A lost YubiKey is an admin-driven event:

```sh
curl -fsS -X DELETE \
  -H "Authorization: Bearer $WEBAUTHN_TOKEN" \
  https://coord.helion.internal:8080/admin/webauthn/credentials/<credential_id>
```

This emits `webauthn_revoked` in the audit log. The
credential is removed from BadgerDB (both the primary and
`by-operator` reverse index), so subsequent `login-begin`
requests no longer include it in the allow-list.

If the lost key was the operator's only registered credential,
they cannot self-login under `on`. Another admin re-registers a
replacement key on their behalf — registration is gated by
`adminMiddleware`, so the recovery path is the same as initial
onboarding, from a trusted operator.

## 6. Binding to mTLS (feature 33 pairing)

Set `bind_to_cert_cn` on the registration request to stamp the
credential's `BoundCertCN`. The WebAuthn-minted JWT then carries
`required_cn: <that CN>`, so a request that presents this token
MUST also present an mTLS cert whose CN matches. Three factors
on three different substrates:

1. The operator's bearer JWT (something they have — the one-time
   bootstrap ticket, revokable via feature 31).
2. The mTLS cert private key (something they have — keyed to the
   workstation).
3. The YubiKey with physical touch (something they hold +
   something they do).

Compromise any one of them and the attacker still doesn't get
admin authority.

## 7. Known limitations

- **Dashboard login UI is deferred** — drive ceremonies via the
  DevTools snippet above or the companion CLI. The Angular
  step-up component is a follow-up slice.
- **Attestation MDS verification deferred** — we accept any
  well-formed attestation. If you need to pin AAGUIDs to an
  approved-vendor list, export the AAGUID from the
  `GET /admin/webauthn/credentials` response and maintain an
  allow-list at the tier-enforcement layer.
- **User-verification (`UserVerification: required`) is not
  enforced** — current ceremony uses `preferred`. Deployments
  that need PIN/biometric on every touch should customise
  webauthnlib's `SessionData.UserVerification` in
  `internal/api/handlers_webauthn.go` (one-line change).
- **Passkeys (cross-device FIDO2) not supported** — deliberate;
  cloud-sync reintroduces a "compromise the cloud, compromise
  the credential" path.

## 8. Troubleshooting

| Symptom | Likely cause |
|---|---|
| `register-begin` returns 503 | `HELION_WEBAUTHN_RPID` or `HELION_WEBAUTHN_ORIGINS` unset at coordinator boot. |
| `register-finish` returns 400 "stale session" | The browser took longer than 5 minutes between begin/finish, or the subject in the JWT differs between the two calls. |
| `navigator.credentials.create` throws `NotAllowedError` | The RPID doesn't match the page's hostname, or the user dismissed the touch prompt. Verify the origin in `HELION_WEBAUTHN_ORIGINS`. |
| `login-finish` returns 401 "sign count replay" | Authenticator returned a sign-counter ≤ the stored value. On YubiKeys this is almost always cloned / tampered hardware. Revoke the credential and investigate. |
| Tier `on` refuses the admin who just registered | The JWT used was the bearer-minted one, not the freshly minted WebAuthn token. Re-run step 3 and use the response's `token` field. |

## 9. Cross-references

- [feature 27 — browser mTLS](../planned-features/implemented/27-browser-mtls.md)
- [feature 31 — cert revocation](../planned-features/implemented/31-cert-revocation-crl-ocsp.md)
- [feature 33 — per-operator accountability](../planned-features/implemented/33-per-operator-accountability.md)
- [feature 34 spec (implemented)](../planned-features/implemented/34-webauthn-fido2.md)
- [security/](../security/) — see `operator-auth.md § 1` (mTLS) and `§ 3` (WebAuthn)
