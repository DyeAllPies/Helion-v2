> **Audience:** engineers + operators
> **Scope:** Operator-facing auth layers — browser mTLS, token ↔ cert-CN binding, WebAuthn / FIDO2.
> **Depth:** reference

# Security — operator authentication

Three complementary layers harden the dashboard → coordinator path beyond the
base JWT. Stacking all three means a successful impersonation needs: a valid
bearer JWT, the operator's cert private key, physical possession of the
operator's hardware authenticator, AND a user-presence gesture.

## 1. Browser mTLS (feature 27)

After feature 23 shipped TLS 1.3 + hybrid-PQC on the REST listener, the
dashboard → coordinator path is protected against lateral interception. What
remains: a leaked JWT is still the full access story. Feature 27 adds an
optional client-certificate check so an attacker who steals a JWT (clipboard,
screenshare, compromised extension) also needs the operator's client-cert
private key to submit requests.

### Enforcement tiers

Selected at coordinator boot via `HELION_REST_CLIENT_CERT_REQUIRED`:

| Tier | Value | Behaviour |
|------|-------|-----------|
| off | `off` / unset / `0` / `no` | Default. No client-cert check. |
| warn | `warn` | Every cert-less request is served AND emits an `operator_cert_missing` audit event. Used for staged rollouts. |
| on | `on` / `1` / `yes` / `required` | Cert-less requests refused at 401. `/healthz` and `/readyz` remain exempt. |

Malformed values are fatal at coordinator startup — a typo must not
silently weaken security below the default.

### Safety properties

- **CA is shared with node mTLS.** The coordinator's existing CA signs both
  node and operator certs. Operator certs carry `ExtKeyUsage = ClientAuth`
  ONLY — a leaked operator cert cannot be re-used to stand up a fake
  server. Node certs keep both `ClientAuth` and `ServerAuth` because nodes
  act as both.
- **Admin role is subject to the same check.** No escape hatch for admin;
  admin tokens + cert-less = 401 in `on` mode.
- **Issuance is admin-mediated.** `POST /admin/operator-certs` is
  admin-only, rate-limited 1 / 10 s, audit-before-response fail-closed on
  audit-sink failure. Mints a fresh ECDSA P-256 client cert + PKCS#12
  bundle encrypted with an operator-supplied password. Every issuance
  writes `operator_cert_issued` with CN + serial + fingerprint.
- **The CLI `helion-issue-op-cert`** wraps that HTTP path for operator
  convenience.
- **`operator_cn` stamped on audit events.** When a verified client cert
  is present, every subsequent audit event (job submits, reveal-secret,
  cert issuance itself) carries `operator_cn` alongside the JWT subject.
  Enables attribution beyond "an admin token did something".
- **Nginx-proxy mode.** If Nginx terminates TLS in front of the
  coordinator, set `ssl_verify_client on` on Nginx and forward
  `X-SSL-Client-Verify`, `X-SSL-Client-S-DN`, `X-SSL-Client-Fingerprint`.
  The coordinator accepts those headers **only from loopback**
  (`127.0.0.1`, `::1`); any non-loopback peer carrying them is treated as
  cert-less to prevent header smuggling.
- **Read-once response.** `POST /admin/operator-certs` returns the private
  key + P12 once. The server does not retain either; a lost response means
  the operator requests a fresh issuance (new serial).

### Header-forgery defence

| Threat | Mitigation |
|---|---|
| Attacker forges `X-SSL-Client-Verify: SUCCESS` to bypass mTLS | Coordinator honours those headers ONLY from loopback (127.0.0.1 / ::1). Any non-loopback peer carrying those headers is treated as if no cert was presented. |

### What mTLS does NOT solve

- **In-browser compromise** — see [§ 3 WebAuthn](#3-webauthn--fido2-feature-34).
- **Revocation** — see [crypto.md § Operator-cert revocation](crypto.md#4-operator-cert-revocation-feature-31).
- **Cert issuance UX** — a dashboard-based admin issuance action is
  [feature 32](../planned-features/implemented/32-web-cert-issuance-ui.md).
- **Per-operator accountability** — see [§ 2](#2-token--cert-cn-binding-feature-33).

Operator guide: [operators/cert-rotation.md](../operators/cert-rotation.md).
Spec: [planned-features/implemented/27-browser-mtls.md](../planned-features/implemented/27-browser-mtls.md).

## 2. Token ↔ cert-CN binding (feature 33)

Feature 27 ships two layers — mTLS client certs AND JWT bearer tokens —
but before feature 33 they were independent: an admin's leaked JWT used
from ANY operator's browser (even one with a valid but different cert)
succeeded as long as the JWT signature verified. The cert CN landed on
audit events but was never enforced against the token.

Feature 33 adds an optional binding between the two.

### `required_cn` JWT claim

The JWT payload gains an optional `required_cn` field:

```json
{
  "sub": "alice",
  "role": "admin",
  "jti": "…",
  "exp": 1714608000,
  "required_cn": "alice@ops"
}
```

- Empty / absent → unbound, legacy behaviour.
- Non-empty → `authMiddleware` enforces that the request arrived with a
  verified operator client cert whose `Subject.CommonName` equals the
  claim exactly.

Any mismatch (different CN OR cert-less request) produces a 401 with
body `"authentication failed"` — deliberately the SAME response shape a
signature-validation failure produces, so an attacker probing with a
stolen token cannot distinguish "wrong CN" from "wrong signature" via
response timing or body.

### Issuing a bound token

```bash
curl -X POST https://helion.example.com/admin/tokens \
  -H "Authorization: Bearer $ADMIN_JWT" \
  -H "Content-Type: application/json" \
  -d '{"subject":"alice","role":"admin","ttl_hours":8,"bind_to_cert_cn":"alice@ops"}'
```

Response:

```json
{
  "token": "eyJ…",
  "subject": "alice",
  "role": "admin",
  "ttl_hours": 8,
  "bound_to_cert_cn": "alice@ops"
}
```

Admins rotating known-unbound tokens into the bound shape re-issue with
`bind_to_cert_cn` set; the old unbound JWT remains valid until its JTI is
revoked or its TTL expires.

### Safety properties

- **Signature-protected binding.** The `required_cn` claim is part of the
  signed JWT payload; an attacker cannot flip it to match their own CN
  without invalidating the signature. Guarded by
  `TestGenerateTokenWithCN_JWTSignatureProtectsBinding`.
- **Audit every mismatch.** Every refused request emits
  `EventTokenCertCNMismatch` with `subject`, `required_cn`, `observed_cn`
  (empty for cert-less), `remote`, `path`, `jti`. A spike indicates a
  leaked token being probed from unauthorised browsers.
- **Fail-closed for cert-less.** A bound token used against an endpoint
  reached with NO client cert fails the binding check; `observed_cn` is
  recorded as `""` so reviewers can distinguish "right cert, wrong CN"
  from "no cert at all".
- **Back-compat preserved.** The legacy
  `GenerateToken(subject, role, ttl)` still works and produces unbound
  tokens. Existing callers are unchanged.
- **Admin responsibility.** Setting `bind_to_cert_cn` is opt-in on every
  mint. An admin who forgets the flag produces an unbound token — the
  dashboard's future Phase-3 gate (refuse unbound tokens when the
  coordinator is in `on`) is the natural hardening step.

### Known limitations

- **No multi-CN bindings.** Each token binds to at most one CN. An
  operator accessing from two workstations mints two tokens.
- **No CN-glob matching.** `alice@*` rejected — security-via-glob is
  risky. Operators with ops + dev certs mint per-CN tokens.
- **Phase 3 dashboard gate is future work.** The dashboard's login
  component does not yet refuse bearer-only tokens when the coordinator
  runs in `on` mode. Wiring that requires exposing the tier via a
  discovery endpoint; out of scope.

Spec: [planned-features/implemented/33-per-operator-accountability.md](../planned-features/implemented/33-per-operator-accountability.md).

## 3. WebAuthn / FIDO2 (feature 34)

Features 27 + 31 + 33 raise authentication to cert-mTLS + token-CN
binding. The remaining hole: the browser itself is still a trusted
runtime. A malicious extension, a compromised dashboard dependency
(supply-chain), or a remote-code-exec against the browser process can
all silently authenticate using the operator's imported cert.

Feature 34 closes that hole by moving the signing key off the browser
and into a hardware authenticator — YubiKey, Apple Secure Enclave,
Windows Hello TPM — that requires physical user interaction (button
press, fingerprint, face scan) for every signature. Malicious
in-browser code can ASK for a signature; the hardware refuses without
user touch.

### Endpoints

```
POST   /admin/webauthn/register-begin    — start registration
POST   /admin/webauthn/register-finish   — store attested credential
POST   /admin/webauthn/login-begin       — start assertion ceremony
POST   /admin/webauthn/login-finish      — mint WebAuthn-backed JWT
GET    /admin/webauthn/credentials       — list all registered credentials
DELETE /admin/webauthn/credentials/{id}  — revoke a credential
```

All six are admin-only via `adminMiddleware`. Register- and login-
ceremony routes are EXEMPT from the
`HELION_AUTH_WEBAUTHN_REQUIRED=on` enforcement tier — otherwise a fresh
operator could never register their first key and `login-begin` itself
would be blocked by its own requirement.

### Enforcement tier

`HELION_AUTH_WEBAUTHN_REQUIRED` mirrors the feature-27 cert-tier shape:

- `off` (default) — admin surface accepts any valid JWT.
- `warn` — admin requests on non-bootstrap admin endpoints emit
  `EventWebAuthnRequired` if the token lacks `auth_method: "webauthn"`,
  but are still served.
- `on` — those requests refused 401 with audit.

Staged rollout: flip to `warn` first, identify operators still on
bearer-only via the audit log, harden them one by one, then flip to `on`.

### Safety properties

- **Hardware-bound signatures.** The private key never leaves the
  authenticator. Every assertion requires a fresh user-presence signal;
  a compromised browser cannot forge them.
- **Replay-resistance.** Authenticators monotonically bump a signCount;
  `UpdateSignCount` refuses any assertion whose counter doesn't strictly
  advance (spec §7.2). Authenticators that stay at 0 (passkeys, some
  platform authenticators) are accommodated via a zero-stays-zero
  exception.
- **Challenge-bound.** Each `begin` call produces a fresh random
  challenge; the session is single-use (Pop removes it) with a 5-minute
  TTL. A replayed `finish` against a stale challenge always fails.
- **Audited mutations.** Register success / reject, login success /
  reject, and revoke each emit a distinct event. A failed verification
  emits the reject-family event; the success event only fires after the
  library's signature check passes.
- **Bootstrap-aware tier gate.** Register + login ceremonies bypass the
  `on`-tier check because blocking them would make the feature
  unbootstrappable. Revoke + list do NOT bypass.
- **Defence-in-depth with feature 33.** When a credential is registered
  with `bound_cert_cn`, the minted WebAuthn JWT carries `required_cn`
  too. Attacker needs (1) the cert private key, (2) physical possession
  of the YubiKey + user touch, (3) a valid bearer JWT — three factors
  on three different substrates.

### Configuration

```bash
# Relying Party ID — the effective domain (no scheme/port).
export HELION_WEBAUTHN_RPID=helion.example.com

# Display name shown in the browser's touch prompt.
export HELION_WEBAUTHN_DISPLAY="Helion Coordinator"

# Comma-separated list of permitted full-origin URLs.
export HELION_WEBAUTHN_ORIGINS=https://helion.example.com

# Enforcement tier. Default off.
export HELION_AUTH_WEBAUTHN_REQUIRED=on
```

The first three must all be set; omitting any (including an empty
`RPID`) disables the feature entirely — the coordinator starts fine but
the webauthn routes stay unregistered.

### Storage

```
webauthn/credentials/<b64url_credential_id>          → JSON(CredentialRecord)
webauthn/by-operator/<b64url_user_handle>/<b64url_credential_id>  → marker
```

A reverse index by operator user handle makes `ListByOperator` O(k)
where k is the credential count per operator (typically 1–3). The
on-disk `CredentialRecord` embeds the raw `webauthn.Credential` struct
— public key, sign count, transports, attestation metadata — so future
library upgrades can re-verify against FIDO MDS without forcing
re-registration.

### Known limitations

- **Passkeys / cross-device FIDO2 deferred.** Apple + Google passkey
  sync reintroduces a "compromise the cloud, compromise the credential"
  story. Revisit when per-tenant passkey-sync controls are deployable.
- **Attestation MDS verification deferred.** go-webauthn supports FIDO
  Metadata Service (trust-anchor + model lookup) via its `MDS` config;
  integrating it is a follow-up. We accept any well-formed attestation;
  the hardware-bound-signature property is the load-bearing control.
- **User-verification enforcement deferred.**
  `UserVerification: required` (PIN or biometric on the authenticator)
  is available but not enabled by default; deferred until the
  operator-guide walk-through is stable.
- **Dashboard login path.** Register + list + revoke ship with this
  feature; a full "step-up to WebAuthn at login" UX is a follow-up.
  Operators can drive the flow manually via `curl` today.

Operator guide: [operators/webauthn.md](../operators/webauthn.md).
Spec: [planned-features/implemented/34-webauthn-fido2.md](../planned-features/implemented/34-webauthn-fido2.md).
