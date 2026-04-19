# Feature: Browser mTLS for dashboard operators

**Priority:** P2
**Status:** Pending
**Affected files:**
`internal/api/server.go` (require + verify client cert on
`ServeTLS`),
`cmd/helion-coordinator/main.go` (`HELION_REST_CLIENT_CERT_REQUIRED`
flag + CA loading),
`internal/pqcrypto/ca.go` (new operator-cert issuance helper —
the CA already does node certs; this adds a parallel path),
`dashboard/nginx.conf` (optional: pass client-cert header
through to coord),
a new `cmd/helion-issue-op-cert/` CLI for bootstrapping
operator P12 bundles,
`docs/SECURITY.md` (new §9 subsection),
`docs/ops/operator-cert-guide.md` (new — install walkthrough).

**Depends on** [feature 23](23-rest-hybrid-pqc.md) — the REST
listener must already be TLS before it can require client
certificates. 27 plugs into 23's `ServeTLS` config; it does not
re-do the listener work.

## Problem

After feature 23 lands, the dashboard → coordinator path rides
TLS 1.3 with hybrid-PQC key exchange, which stops a lateral
attacker from reading JWTs off the wire. That closes the
transport side.

What remains: **authentication is still just a pasted bearer
token.** A compromised browser extension, a copy-paste into the
wrong window, a screenshare with the token visible for half a
second, a clipboard-monitoring tool — any of these leaks the
full admin-role JWT, and the attacker can then submit jobs from
anywhere on the internet with only the leaked token in hand.

Browser mTLS (client certificate verification at the TLS layer)
raises the bar: an attacker needs **both** a valid JWT **and** a
client cert private key present in the victim's browser keychain
at the time of the attack. That's a meaningfully harder target.
The cert isn't a substitute for the JWT — it's a second factor
the attacker cannot obtain by reading traffic, clipboard, or
screen.

### Honest tradeoffs

This feature has real UX cost. Calling them out upfront so
reviewers can weigh:

- **Browser UX is not great.** The operator must install a P12
  bundle into their OS keychain or browser cert store. On
  Chrome/Edge/Safari this is a settings-menu operation; on
  Firefox it's inside the Firefox preferences pane. No one-
  click experience.
- **Dev ergonomics change.** `docker compose up` regenerates
  the CA on each boot, which would silently invalidate any
  installed op cert. Dev overlay must either pin the CA across
  restarts **or** skip client-cert verification in dev mode
  (`HELION_REST_CLIENT_CERT_REQUIRED=off`).
- **Cert issuance is operator onboarding work.** An admin has
  to mint an op cert for each operator and securely hand them
  the P12. For a single-operator local demo this is overkill.
  For a multi-tenant internal deployment it's table stakes.
- **Without a per-operator identity model**, every op cert
  carries the same authority — mTLS gives defense-in-depth but
  not per-user accountability. That pairs cleanly with feature
  22's "Per-operator role-scoped tokens" deferred item; both
  become richer together.

The right way to frame it: **worth building when Helion is
deployed for multiple operators or in environments where JWT
leakage is a realistic threat; overkill for a single-op demo.**
We ship it behind a flag so each operator chooses whether to
take the UX hit.

## Current state

- [`internal/pqcrypto/ca.go`](../../internal/pqcrypto/ca.go)
  already issues ECDSA + ML-DSA node certificates. The same
  CA can issue client certificates for operators — it's the
  same EC key generation + same cert signing, different
  ExtKeyUsage (`clientAuth` instead of `serverAuth`).
- [`internal/pqcrypto/ca.go:180`](../../internal/pqcrypto/ca.go#L180)
  sets `ClientAuth: tls.RequireAnyClientCert` on the gRPC
  server-side `tls.Config`, but uses that for **node**
  registration (the node presents its own cert). The REST
  listener that feature 23 stands up does not require a client
  cert today.
- Dashboard uses JWT-only. No client-side cert selection code.

## Design

### 1. New CA method — issue an operator certificate

```go
// internal/pqcrypto/ca.go

// IssueOperatorCert signs a client certificate for a dashboard
// operator. The CN is the operator's human-friendly name (free-
// form; only used in logs + cert store UI) and KeyUsage is
// DigitalSignature | KeyEncipherment + ExtKeyUsage ClientAuth
// (no ServerAuth). TTL defaults to 90 days so operators rotate
// roughly quarterly without needing to touch the server.
//
// The return is a PEM bundle + a raw P12 file — operators
// import the P12 into Chrome/Firefox/Safari keychain; the PEM
// is retained on disk for audit + re-bundling.
func (ca *CA) IssueOperatorCert(cn string, ttl time.Duration) (certPEM, keyPEM, p12 []byte, err error) { ... }
```

### 2. New CLI — `cmd/helion-issue-op-cert`

```
helion-issue-op-cert \
  --coordinator-state-dir /app/state \
  --operator-cn "alice@ops" \
  --ttl 90d \
  --out alice-op.p12
```

Talks directly to the coordinator's shared state volume to read
the CA private key (same trust boundary as the coordinator
itself — anyone with read access to `/app/state/ca.key` can
already impersonate the CA). Writes a P12 bundle the operator
imports into their browser.

Audit event `operator_cert_issued` fires on every issuance
with the CN + fingerprint.

### 3. Coordinator wires client-cert verification into the REST listener

Feature 23 produced a `ServeTLS(addr, *tls.Config)` method. This
feature adds an **extended** form that additionally populates:

```go
cfg.ClientAuth = tls.RequireAndVerifyClientCert
cfg.ClientCAs  = ca.CertPool()   // same CA that signed the op cert
```

And adds the middleware to extract the verified cert:

```go
// internal/api/middleware.go
func (s *Server) clientCertMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
            // TLS termination at Nginx: check X-Client-Cert-* headers
            // populated by nginx.conf's ssl_client_verify on.
            if !s.verifyProxiedClientCert(r) {
                writeError(w, 401, "client certificate required")
                return
            }
        }
        // Record the CN into the request context so audit entries
        // can attribute actions to an operator identity separate
        // from (or in addition to) the JWT subject.
        ctx := context.WithValue(r.Context(), operatorCNKey, cnFromCert(r))
        next(w, r.WithContext(ctx))
    }
}
```

Audit events now record BOTH `actor` (JWT sub) and
`operator_cn` (cert CN). These will typically be the same
string, but during transition (admin using old bearer-only flow)
they may differ.

### 4. Gating

The requirement is **opt-in** via env:

```
HELION_REST_CLIENT_CERT_REQUIRED=off   # default, current behaviour
HELION_REST_CLIENT_CERT_REQUIRED=warn  # audit-log every cert-less request, still serve
HELION_REST_CLIENT_CERT_REQUIRED=on    # 401 any request without a verified client cert
```

The `warn` tier exists so an operator can roll out client certs
gradually — deploy the flag, check the audit log for who's
still on bearer-only, tell those operators to install certs,
then flip to `on`.

### 5. Nginx pass-through (for prod deployments)

Two deployment shapes are supported:

**(a) Coordinator terminates TLS.** Nginx is removed or just
proxies HTTP/1.1 straight through. `ClientAuth: RequireAndVerify`
on the coordinator listener is the verification path.

**(b) Nginx terminates TLS.** Production deployments usually
want Nginx to handle cert rotation + HSTS + rate limiting, so
client-cert verification happens at Nginx:

```nginx
ssl_verify_client on;
ssl_client_certificate /etc/nginx/coord-ca.pem;
proxy_set_header X-SSL-Client-Verify   $ssl_client_verify;
proxy_set_header X-SSL-Client-S-DN     $ssl_client_s_dn;
proxy_set_header X-SSL-Client-Fingerprint $ssl_client_fingerprint;
```

The coordinator's `verifyProxiedClientCert` helper reads those
headers and trusts them **only from 127.0.0.1** (the Nginx
sidecar). Any request arriving with those headers from anywhere
else is rejected — prevents header smuggling.

## Security plan

| Threat | Control |
|---|---|
| Token leaked via clipboard / screenshare → remote attacker submits jobs | Attacker also needs a valid client cert keypair. Even with the token, cert-less TLS handshake fails at layer 4; request never reaches the auth middleware. |
| Header smuggling — attacker forges `X-SSL-Client-Verify: SUCCESS` | Coordinator accepts those headers only from loopback (the Nginx sidecar). Any other origin → 401, no matter what headers are set. |
| Attacker steals the P12 from a laptop | P12 has a password (required at import time); plus OS keychain itself has user-auth gating on modern OSes. Not iron-clad — if the laptop is unlocked and unlocked via biometrics, the cert is usable. Mitigation: short TTL (90 d default) + revocation list. |
| Dev ergonomics degraded; operator disables cert check in prod by accident | `HELION_REST_CLIENT_CERT_REQUIRED=off` emits a `WARN` on every boot AND an `audit.system` event. Review-friendly. |
| CA key exfiltrated from `/app/state/ca.key` | Out of scope for this feature — same trust boundary as today. A compromised CA key compromises the whole cluster regardless of mTLS. |

### What mTLS does NOT solve

- **It is not authentication replacement.** JWT still determines
  role (admin vs job vs node). Cert only says "this requester
  is one of the operators we issued a cert to".
- **It is not per-operator accountability** unless paired with a
  richer identity model (each cert CN is distinct, but the JWT
  role is still flat). The deferred "per-operator role-scoped
  tokens" work on feature 22 would pair with this to close the
  attribution gap.
- **It does not protect against a compromised browser process.**
  If the attacker is running code inside the operator's Chrome,
  they can use the installed cert directly. mTLS is a
  network-boundary control, not an in-browser one.

New entry in `docs/SECURITY.md` §9 (Dashboard security): a
subsection **"9.X Optional client-cert authentication"**
documenting the three tiers + how to opt in + what it does + does
not buy.

## Implementation order

1. `IssueOperatorCert` method on the CA + unit tests (cert parses,
   has `ClientAuth` EKU, signed by the CA).
2. `cmd/helion-issue-op-cert` CLI + integration test (invoke CLI,
   parse P12, verify).
3. Coordinator `ServeTLS` accepts a "cert-required" config mode.
   Wire `HELION_REST_CLIENT_CERT_REQUIRED` env parsing with the
   three tiers.
4. `clientCertMiddleware` + context propagation of operator CN.
5. Audit schema gains `operator_cn` field.
6. Nginx proxy-header pass-through + the loopback trust check.
7. Docs — `docs/ops/operator-cert-guide.md` step-by-step for
   Chrome / Firefox / Safari / curl.
8. Dev overlay `docker-compose.dev.yml` explicitly sets
   `HELION_REST_CLIENT_CERT_REQUIRED=off` with a comment pointing
   to this doc.

## Tests

- `TestIssueOperatorCert_HasClientAuthEKU` — cert verifies
  against the CA, `ExtKeyUsage` contains `ClientAuth`.
- `TestIssueOperatorCert_DoesNotHaveServerAuth` — cert has NO
  `ServerAuth` EKU (prevents accidental use as a server cert).
- `TestCoordinator_ClientCertRequired_RejectsMissingCert` —
  `HELION_REST_CLIENT_CERT_REQUIRED=on`; client dials TLS
  without presenting a cert → handshake aborted.
- `TestCoordinator_ClientCertRequired_AcceptsValidCert` — same
  flag; client presents a valid op cert → 200.
- `TestCoordinator_ClientCertRequired_RejectsUnknownCA` — client
  presents a cert signed by a **different** CA → 401.
- `TestCoordinator_ClientCertWarnMode_LogsButServes` — `warn`
  tier: no cert → 200 + audit event `operator_cert_missing`.
- `TestProxiedHeaders_LoopbackOnly` — Nginx-mode headers from
  loopback → accepted; same headers from a non-loopback IP → 401.
- `TestAuditEntry_IncludesOperatorCN` — submit a job with a
  client cert present, assert the audit entry carries
  `operator_cn=<cn>`.

## Acceptance criteria

1. Admin generates a P12 via
   `helion-issue-op-cert --operator-cn alice --out alice.p12 --ttl 90d`.
2. Admin imports the P12 into Chrome keychain on alice's
   workstation.
3. With `HELION_REST_CLIENT_CERT_REQUIRED=on` set on the
   coordinator, alice can open the dashboard, paste her JWT, and
   submit a job. `audit` shows the entry with `operator_cn=alice`.
4. From a browser that has NOT imported the cert, the same JWT
   paste fails: TLS handshake aborts before the login page even
   renders.
5. `HELION_REST_CLIENT_CERT_REQUIRED=warn`: bearer-only access
   still works; each such request emits
   `audit.operator_cert_missing`.
6. `HELION_REST_CLIENT_CERT_REQUIRED=off` (default): no behaviour
   change from today.

## Deferred (out of scope)

- **Cert revocation via CRL or OCSP.** TTL-based rotation
  (90 days default) covers the common case. Full revocation
  infrastructure is a bigger slice.
- **Per-operator role tokens** (pairs with this feature) —
  already listed as deferred on feature 22.
- **Web-based cert issuance UI.** An admin dashboard action
  that issues an op cert + emails the P12. Nice ergonomics;
  not required for v1.
- **Pure-client-cert auth (no JWT).** Ambitious; would replace
  the whole auth model.
