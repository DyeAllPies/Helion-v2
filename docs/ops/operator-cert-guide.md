# Operator Certificate Guide (feature 27 — browser mTLS)

This guide walks through minting a dashboard operator client
certificate, importing it into the browser's certificate store, and
validating end-to-end that `HELION_REST_CLIENT_CERT_REQUIRED=on`
blocks cert-less traffic.

See [SECURITY.md §9.6](../SECURITY.md#96-optional-browser-mtls-for-dashboard-operators-feature-27)
for the threat model + safety properties this guide backs.

## Prerequisites

- An admin JWT for the coordinator (`HELION_TOKEN`).
- The coordinator's CA certificate PEM (`HELION_CA_FILE`) — needed
  so the CLI can verify the coordinator's server cert. Export from
  a known-good client, or copy from the coordinator's state volume.
- The CLI binary: `go build ./cmd/helion-issue-op-cert/` produces
  `helion-issue-op-cert`.

## 1. Mint a P12 for an operator

```sh
export HELION_COORDINATOR=https://coord.helion.internal:8080
export HELION_TOKEN="<admin-jwt>"
export HELION_CA_FILE=/etc/helion/ca.pem

helion-issue-op-cert \
  --operator-cn  "alice@ops" \
  --ttl-days     90 \
  --p12-password-file ./alice.p12.pass \
  --out          alice.p12
```

Notes:

- **Always use `--p12-password-file`**, not `--p12-password`, in
  shared-host environments. The flag appears in `ps aux` and shell
  history; a file read once is invisible to both.
- `--ttl-days` is capped server-side at the CA's remaining life.
  The default (90) is a sane quarterly rotation; feel free to set
  lower for high-privilege operators.
- Each issuance mints a fresh serial; there is no "re-issue" path.
  Lost P12? Mint a new one — the old serial stays valid until
  expiry or (future) revocation lands as feature 31.

Sample output:

```
issued operator cert:
  common_name      alice@ops
  serial_hex       180e0f20ac7b6...
  fingerprint_hex  b4a29e2...
  not_before       2026-04-19T18:42:01.1234Z
  not_after        2026-07-18T18:42:01.1234Z
  p12              alice.p12 (2310 bytes)

This issuance was recorded in the audit log (event type:
operator_cert_issued). Serial 180e0f20ac7b6... is revocable via a
future CRL/OCSP endpoint (feature 31).
```

## 2. Import the P12 into the operator's browser

### Chrome / Edge (Linux, macOS, Windows)

1. Settings → Privacy and security → Security → Manage certificates
2. On the **Your Certificates** tab click *Import*.
3. Select `alice.p12` and enter the P12 password from
   `alice.p12.pass`.

Chrome/Edge will prompt for which site the cert should be used for
automatically when you next hit the coordinator's URL.

### Firefox

1. `about:preferences#privacy` → Certificates → View Certificates
2. **Your Certificates** tab → Import, select `alice.p12`, enter
   password.

Firefox will prompt for the cert on the first TLS handshake with
the coordinator. Check *Remember this decision* to avoid prompting
on every request.

### Safari (macOS)

Double-click `alice.p12` in Finder. macOS imports it into the login
keychain automatically; you'll be asked for the P12 password, then
for your login password to authorise the import.

### curl / command-line tools

```sh
curl --cert-type P12 \
     --cert alice.p12:$(cat alice.p12.pass) \
     --cacert $HELION_CA_FILE \
     -H "Authorization: Bearer $HELION_TOKEN" \
     "$HELION_COORDINATOR/jobs?page=1&size=5"
```

## 3. Validate the three tiers

### Staged rollout

Flip the coordinator to `warn` first:

```sh
HELION_REST_CLIENT_CERT_REQUIRED=warn helion-coordinator ...
```

Every cert-less request now lands an `operator_cert_missing` audit
event (viewable via `GET /audit?type=operator_cert_missing`). Once
the audit log shows only the operators who are still on bearer-only
access, send them this guide, wait for them to import their certs,
then flip to `on`.

### Full enforcement

```sh
HELION_REST_CLIENT_CERT_REQUIRED=on helion-coordinator ...
```

Now cert-less requests return `401 client certificate required (HELION_REST_CLIENT_CERT_REQUIRED=on)`.

### Health exemption

`/healthz` and `/readyz` always serve even in `on` mode, because
k8s-style liveness / readiness probes can't present client certs.
These endpoints expose no operational detail worth protecting.

## 4. What's audited

| Event | When |
|---|---|
| `operator_cert_issued`  | Successful `POST /admin/operator-certs`. Detail: `common_name`, `serial_hex`, `fingerprint_hex`, `not_before`, `not_after`, and (if the issuer was itself cert-mTLS-authenticated) `operator_cn`. |
| `operator_cert_reject`  | Request to the same endpoint that fails validation (bad body, short password, empty CN). Detail: reject `reason`. |
| `operator_cert_missing` | In `warn` mode, fires on every cert-less request. In `on` mode, fires on the cert-less request that gets 401'd. Detail: `path`, `remote_addr`, plus `enforced: true` in `on` mode. |

All other audit events (job_submit, secret_revealed, etc.) now
carry an `operator_cn` detail whenever the request arrived with a
verified cert, so reviewers can attribute actions beyond the JWT
subject.

## 5. Rotation

Operator certs default to a 90-day TTL. There's no auto-rotation
today — mint a new P12 ahead of expiry and have the operator
import it. The old cert keeps working until it expires.

Full revocation (before expiry) is tracked as
[feature 31 — CRL/OCSP](../planned-features/31-cert-revocation-crl-ocsp.md).

## 6. Known limitations

- **CA is regenerated on coordinator restart.** This is a
  pre-feature-27 limitation. Until the CA is persisted (related
  to [feature 30](../planned-features/30-encrypted-env-storage.md)),
  every coordinator restart invalidates all issued operator AND
  node certs. In a production multi-operator deployment this is
  disruptive; for single-operator demos it's a non-issue.
- **Compromised browser process.** A malicious extension or code
  running inside the operator's browser can use the imported cert
  directly. Hardware-bound keys (WebAuthn / YubiKey) mitigate this;
  see [feature 34](../planned-features/34-webauthn-fido2.md).
- **Flat authority.** Every operator cert grants the same
  coordinator access; `operator_cn` gives attribution but not
  permission scoping. Richer identity → token binding is
  [feature 33](../planned-features/33-per-operator-accountability.md).
