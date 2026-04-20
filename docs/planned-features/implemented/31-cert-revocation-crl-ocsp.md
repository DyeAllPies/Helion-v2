# Feature: Cert revocation via CRL or OCSP

**Priority:** P2
**Status:** Implemented (2026-04-20)
**Affected files:**
`internal/pqcrypto/ca.go` (new `RevokeOperatorCert` +
persistence of revoked-serial set),
`internal/api/operator_cert.go` (new `POST /admin/operator-certs/{serial}/revoke`),
`internal/api/operator_cert.go` (extend client-cert verification
path to reject revoked serials),
`cmd/helion-coordinator/main.go` (surface CRL at a well-known URL
for nginx `ssl_crl` — optional for the Nginx-terminates path),
`docs/SECURITY.md` (extend §9.6).

## Problem

Feature 27 ships with TTL-based rotation: operator certs default
to a 90-day lifetime, and the only way to invalidate one early is
to wait for expiry or re-generate the CA (which blows up every
other operator + every node). That's untenable when:

- An operator leaves the team mid-quarter.
- A P12 file is suspected leaked (laptop stolen,
  `defer log.Println(tok)` committed to a repo).
- A cert is accidentally issued with a wrong CN (typo); the old
  serial needs to become unusable.

Today's answer is "wait 90 days or throw the whole cluster away."
That's a hole.

## Current state

- `pqcrypto.CA.IssueOperatorCert` mints a fresh serial each call;
  serials are not persisted anywhere in the coordinator state.
- `POST /admin/operator-certs` emits an `operator_cert_issued`
  audit event carrying the serial, but there's no index from
  serial → current status.
- The coordinator's client-cert verification (tls.ClientCAs +
  `VerifyClientCertIfGiven`) chains to the CA but has no
  revocation check: any cert that chains, with an in-date
  NotBefore/NotAfter window, verifies.

## Design

### 1. Revoked-serial set

A persisted `map[serialHex]RevocationRecord` in BadgerDB under
`crypto/revoked/<serial>`. Record carries revocation timestamp,
reason (free-form operator input), and the CN at revocation time
so the audit log has enough context without cross-referencing
issuance records.

```go
// internal/pqcrypto/revocation.go
type RevocationRecord struct {
    SerialHex    string
    CommonName   string
    RevokedAt    time.Time
    RevokedBy    string // JWT subject of the admin who revoked
    Reason       string
}

type RevocationStore interface {
    Revoke(ctx, rec RevocationRecord) error
    IsRevoked(serialHex string) bool // hot path — must be O(1)
    List(ctx) ([]RevocationRecord, error)
}
```

### 2. Admin revoke endpoint

```
POST /admin/operator-certs/{serial}/revoke
{"reason":"alice left the team"}
```

Admin-only; rate-limited; audit `operator_cert_revoked`.
Idempotent: revoking an already-revoked serial returns 200 +
echoes the existing record (so a panicked operator can hit the
endpoint five times without generating five audit lines).

### 3. Verification hook

`clientCertMiddleware.extractVerifiedCN` runs AFTER the TLS
handshake has verified the chain. Add a check:

```go
if s.revocationStore != nil && s.revocationStore.IsRevoked(serialOfPeer(r)) {
    // Treat as cert-less. In warn mode: still emit
    // operator_cert_revoked_used audit event (for post-incident).
    // In on mode: 401.
}
```

### 4. CRL export (Nginx-mode deployments)

For the Nginx-terminates-TLS shape, the coordinator publishes
`GET /admin/ca/crl` — a PEM-encoded CRL that Nginx consumes via
`ssl_crl`. The coordinator signs the CRL with the CA key at
export time; Nginx reloads it on file change.

### 5. OCSP alternative (optional follow-up)

Implementing a full OCSP responder is significantly more work
(RFC 6960 compliance, signed responses per cert, nonce handling).
The CRL path covers 95% of operator needs. OCSP remains open as
an incremental follow-up for deployments with short CRL-refresh
tolerance.

## Security plan

| Threat | Control |
|---|---|
| Leaked operator cert remains usable until TTL expiry | Revocation endpoint + verification hook reject revoked serials at the TLS-verify stage. |
| Admin accidentally revokes their own cert and locks themselves out | Revocation endpoint requires a cert-less admin path (JWT + loopback, or explicit bypass label) so the operator can still reach the revocation API from e.g. a coordinator console container. Document this in the operator-cert-guide. |
| Revocation store is compromised and attacker un-revokes a serial | Revocation is append-only; an "unrevoke" action is a NEW issuance, not a deletion. Records stay in BadgerDB forever (TTL 0). |
| CRL on disk tampered with | CRL is signed by the CA; Nginx verifies signature before trusting. |

## Implementation order

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | `RevocationRecord` type + BadgerDB-backed `RevocationStore` + unit tests. | — | Medium |
| 2 | `POST /admin/operator-certs/{serial}/revoke` handler + `operator_cert_revoked` audit. | 1 | Small |
| 3 | Extend `clientCertMiddleware` to consult `IsRevoked(serial)` and treat matches as cert-less. | 1 | Small |
| 4 | `GET /admin/ca/crl` — PEM CRL export signed by the CA. | 1 | Medium |
| 5 | Operator-cert-guide + SECURITY.md §9.6 update. | 1-4 | Trivial |
| 6 | (Optional) RFC 6960 OCSP responder at `/ocsp`. | 1 | Large |

## Tests

- `TestRevocationStore_RevokeThenIsRevoked` — round-trip.
- `TestRevocationStore_UnrevokeNotSupported` — attempting to
  delete a record returns an error.
- `TestClientCertMiddleware_RevokedCert_TreatedAsCertless` — cert
  chains OK but serial is revoked → 401 in `on` mode.
- `TestRevokeHandler_AdminOnly` — non-admin → 403.
- `TestRevokeHandler_Idempotent` — revoking twice returns 200 both
  times; audit log has exactly ONE `operator_cert_revoked` entry.
- `TestCRLExport_Verifies` — fetch `/admin/ca/crl`, verify the CRL
  signature against the CA cert, assert the revoked serial is in
  the list.

## Acceptance criteria

1. `POST /admin/operator-certs/{serial}/revoke` returns 200 and
   emits `operator_cert_revoked`.
2. A subsequent HTTPS request presenting the revoked cert is
   rejected at 401 with `HELION_REST_CLIENT_CERT_REQUIRED=on`.
3. `GET /admin/ca/crl` returns a PEM CRL that verifies against
   the CA cert and lists the revoked serial.
4. `TestRevokeHandler_Idempotent` passes — repeated revoke calls
   do not duplicate audit entries.

## Deferred

- **OCSP responder.** Covered by step 6 but optional for v1.
- **Per-cert cross-coordinator revocation sync.** Helion is
  single-coordinator today; no sync needed.

## Implementation status

_Implemented 2026-04-20._

### What shipped

- `internal/pqcrypto/revocation.go`:
  - `RevocationRecord` (SerialHex, CommonName, RevokedAt,
    RevokedBy, Reason).
  - `RevocationStore` interface + `BadgerRevocationStore`
    implementation backed by the shared coordinator Badger
    DB under `crypto/revoked/` prefix. O(1) in-memory
    cache rebuilt from Badger at boot; every Revoke writes
    through.
  - Append-only semantics (no Delete primitive); idempotent
    Revoke returns the original record with isNew=false.
  - Defensive serial normalisation
    (`NormalizeSerialHex` — trim, strip `0x`, lowercase,
    hex-digit validate, 64-char cap).
  - Reason trimming + 512-byte cap at write time.
  - `CA.CreateCRLPEM` — signs a PEM-encoded X.509 CRL using
    the CA's ecdsa private key + cert. Populates both the
    modern `RevocationListEntry` and the legacy
    `RevokedCertificate` fields. Empty-list CRL still signs
    cleanly so consumers fetching before the first revoke
    don't error.

- `internal/audit/logger.go`:
  - `EventOperatorCertRevoked` — admin used the revoke
    endpoint; carries serial_hex, common_name, revoked_by,
    reason, idempotent.
  - `EventOperatorCertRevokedUsed` — a revoked cert was
    presented at the TLS verification hook; carries
    serial_hex, common_name, remote, path, enforced
    (bool, true only in `on` tier).

- `internal/api/handlers_revocation.go` — three admin
  endpoints:
    - `POST /admin/operator-certs/{serial}/revoke` (201 on
      new, 200 on idempotent, 400 on invalid serial or
      missing reason, 403 on non-admin).
    - `GET /admin/operator-certs/revocations` (list).
    - `GET /admin/ca/crl` (signed PEM, 503 if no signer
      wired). Audit-before-response on the write endpoint.

- `internal/api/operator_cert.go` — `extractVerifiedCN`
  renamed to `extractVerifiedPeer` and extended to return
  the serial hex. The direct-TLS path reads the leaf's
  `SerialNumber`; the Nginx loopback-proxy path honours an
  optional `X-SSL-Client-Serial` header for belt-and-braces
  enforcement. `clientCertMiddleware` consults
  `revocationStore.IsRevoked(serial)` after the chain-verify
  step. `on` tier → 401 + `EventOperatorCertRevokedUsed`;
  `warn` tier → proceed WITHOUT stamping the Operator
  principal + double-audit (revoked-used AND cert-missing).

- `internal/api/server.go`:
  - `RevocationStoreIface` + `CRLSigner` interfaces.
  - `SetRevocationStore(iface)` registers POST revoke +
    GET list + (if signer wired) GET CRL.
  - `SetCRLSigner(signer)` — MUST be called before
    SetRevocationStore (net/http.ServeMux panics on
    duplicate-pattern registration).

- `cmd/helion-coordinator/main.go` — constructs the
  Badger-backed revocation store, wires it as both CRL
  signer (`bundle.CA`) and revocation store.
  Failure to load the store logs at Error but doesn't block
  coordinator boot; the endpoints simply remain
  unregistered in that case.

### Deviations from plan

- **OCSP responder** deferred. The spec explicitly listed it
  as optional; 95% of operator needs are covered by CRL.
  A future slice can add `/ocsp` without touching the
  revocation store.
- **X-SSL-Client-Serial** from loopback proxy — the spec's
  Nginx-terminated scenario assumes Nginx does CRL
  enforcement via `ssl_crl`. We added optional serial
  forwarding as defence-in-depth: operators who configure
  Nginx to send `ssl_client_serial` into a loopback-only
  header get a second revocation check at the coordinator.
  Not required; Nginx's `ssl_crl` alone is sufficient.

### Tests added

- `internal/pqcrypto/revocation_test.go`:
  - Round-trip (Revoke + IsRevoked + Get).
  - Idempotent Revoke preserves the original record's
    RevokedBy + Reason.
  - Reload from disk (simulated restart).
  - Reject bad serials (empty, non-hex, over-length).
  - Missing Get returns ErrRevocationNotFound.
  - List ordering (newest first).
  - NormalizeSerialHex table test (hex, 0x prefix, mixed
    case, whitespace, invalid).
  - Reason capped at 512 bytes.
  - CreateCRLPEM verifies against the CA cert.
  - CreateCRLPEM rejects backwards nextUpdate.
  - Empty-list CRL still signs cleanly.
  - SerialHexFromBigInt round-trip with NormalizeSerialHex.

- `internal/api/handlers_revocation_test.go`:
  - Revoke happy path (201 on new, 200 on idempotent).
  - Non-admin → 403.
  - Reason required → 400.
  - Invalid serial → 400.
  - EventOperatorCertRevoked audit emission.
  - List endpoint admin-only.
  - CRL export returns signed PEM that verifies against
    the real CA.
  - CRL export non-admin → 403.
  - clientCertMiddleware rejects revoked serial in `on`
    tier + emits EventOperatorCertRevokedUsed.
  - clientCertMiddleware passes valid (non-revoked) cert
    through.
