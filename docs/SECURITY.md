# Helion v2 — Security Reference

Security model for the Helion v2 minimal orchestrator: post-quantum
cryptography, JWT authentication, rate limiting, audit logging, and
operational procedures.

---

## Table of contents

1. [Threat model](#1-threat-model)
2. [mTLS and certificate architecture](#2-mtls-and-certificate-architecture)
3. [Post-quantum cryptography](#3-post-quantum-cryptography)
4. [JWT authentication](#4-jwt-authentication) → [JWT-GUIDE.md](JWT-GUIDE.md)
5. [Rate limiting](#5-rate-limiting)
6. [Audit logging](#6-audit-logging)
7. [Node revocation](#7-node-revocation)
8. [REST API security](#8-rest-api-security)
9. [Dashboard security](#9-dashboard-security)
10. [Operational guide](#10-operational-guide) → [SECURITY-OPS.md](SECURITY-OPS.md)
11. [References](#11-references)

---

## 1. Threat model

| Threat | Mitigation |
|---|---|
| Rogue node connecting to coordinator | mTLS — coordinator verifies node certificate on every connection |
| Intercepted coordinator↔node traffic (today) | TLS 1.3 with X25519 key exchange |
| Intercepted traffic decrypted by future quantum computer | Hybrid ML-KEM (Kyber-768) key exchange |
| Tampered node certificate | ML-DSA (Dilithium-3) out-of-band signature verified on every registration |
| New cert silently replacing an existing node's cert | SHA-256 certificate fingerprint pinned on first registration; mismatch rejected |
| Revoked node with active heartbeat stream | Active gRPC stream closed immediately on revocation via done channel |
| Stolen API token used after expiry | JWT 15-minute expiry enforced |
| Stolen API token used before expiry | JTI-based revocation via `DELETE /admin/tokens/{jti}`; effective within 1 s |
| Leaked root token from a prior coordinator run | Root token rotated (old JTI revoked) on every restart |
| Privilege escalation via token sharing | Scoped tokens issued per-user via `POST /admin/tokens`; admin role required |
| API abuse / DoS from a single node | Per-node token-bucket rate limiter with `GarbageCollect` to bound memory |
| Undetected compromise post-incident | Append-only audit log covers all security events including token issuance/revocation |
| Vulnerable Go dependency | Snyk scans `go.mod` on every push; blocks on high severity |
| Vulnerable container OS packages | Snyk container scan of coordinator image on every push |

---

## 2. mTLS and certificate architecture

All coordinator↔node communication is mutually authenticated via mTLS.

**Certificate issuance flow:**

1. Node starts, finds no certificate on disk.
2. Node calls `Register` RPC with its node ID.
3. Coordinator's internal CA generates an ECDSA P-256 + ML-DSA-65 key pair, signs a
   certificate for the node, and returns it.
4. Node persists the certificate and uses it for all subsequent connections.

**Certificate storage:**

- Coordinator stores DER bytes under `certs/{nodeID}` in BadgerDB (no expiry).
- Node stores its certificate on the local filesystem.

**TLS configuration:**

The coordinator builds a `tls.Config` with `ClientAuth: tls.RequireAndVerifyClientCert`.
Each gRPC connection is rejected at the TLS handshake if the node certificate cannot be
verified against the internal CA. Revoked node IDs are also checked in a unary interceptor
before any RPC handler runs.

---

## 3. Post-quantum cryptography

### Hybrid key exchange (ML-KEM / Kyber-768)

TLS key exchange uses a hybrid mode: X25519 (classical) **and** ML-KEM-768 (post-quantum)
are both negotiated in the same ClientHello. The session key is derived from both; breaking
the session requires breaking both simultaneously.

- Curve ID: `x25519_mlkem768` (`0x6399`)
- Enabled by default in Go 1.26+
- Implemented in `internal/pqcrypto/hybrid.go` using the Cloudflare `circl` library
  (ML-KEM primitives from NIST FIPS 203)

**Why now?** The threat is harvest-now-decrypt-later: an adversary can record encrypted
coordinator↔node traffic today and decrypt it once a sufficiently powerful quantum computer
exists. Building hybrid PQC at design time costs relatively little; retrofitting it is
expensive. NIST finalised ML-KEM as FIPS 203 in 2024.

**Verification with Wireshark:**

```bash
tcpdump -i any -w helion.pcap port 50051
# Open in Wireshark → filter: tls.handshake.type == 1
# ClientHello → Extension: supported_groups
# Should contain: x25519_mlkem768 (0x6399)
```

### ML-DSA node certificate signing

Node certificates carry a dual signature: ECDSA P-256 (classical) **and** ML-DSA-65
(Dilithium-3, NIST FIPS 204). The coordinator verifies both signatures on registration.

- Implemented in `internal/pqcrypto/mldsa.go` and `internal/pqcrypto/ca.go`
- A certificate with a tampered signature is rejected at the `Register` RPC

**Tampering test:**

```bash
# Modify any byte in a node certificate, then attempt registration:
xxd -p node.crt | sed 's/00/FF/1' | xxd -r -p > node_tampered.crt
# Expected: gRPC Unauthenticated — ML-DSA signature invalid
```

---

## 4. JWT authentication

See [JWT-GUIDE.md](JWT-GUIDE.md) for the full JWT reference: token properties,
root token rotation, issuing scoped tokens, usage examples, and revocation.

Summary: HS256 with 15-minute expiry (normal) or 10-year expiry (root, rotated
on every restart). JTI-based revocation via `DELETE /admin/tokens/{jti}` with
sub-second latency.

---

## 5. Rate limiting

Each node has an independent token-bucket rate limiter in the coordinator.

| Property | Value |
|---|---|
| Default rate | 10 jobs/s per node |
| Algorithm | Token bucket (allows short bursts up to the rate limit) |
| Configuration | `HELION_RATE_LIMIT_RPS` environment variable |
| gRPC status on limit hit | `ResourceExhausted` |
| Audit event | `rate_limit_hit` |

**Applied at two levels:**

1. gRPC unary interceptor — intercepts `Register` and `ReportResult` RPCs
2. Heartbeat handler — streaming RPCs bypass unary interceptors; rate limit is checked
   per heartbeat message

### Analytics API rate limiting

The `/api/analytics/*` endpoints have their own per-subject limiter because
their queries (`PERCENTILE_CONT`, `ORDER BY` on `job_summary`) are expensive
as data grows. Without this limit, an authenticated user could DoS the
coordinator.

| Property | Value |
|---|---|
| Rate | 2 queries/sec per JWT subject |
| Burst | 30 |
| Sustained cap | ~120 queries/min per subject |
| HTTP status on limit hit | `429 Too Many Requests` |
| Body | `{"error":"analytics query rate limit exceeded"}` |
| Keyed on | JWT `sub` claim (subject) |

Rate-limited requests are rejected *before* the audit step, so abusive
traffic doesn't flood the audit log. See
`internal/api/middleware.go:analyticsQueryAllow`.

**Load test:**

```bash
for i in {1..1000}; do helion-run echo "job $i" & done
wait
# First ~10 jobs succeed (burst); sustained rate limited to 10 jobs/s thereafter.
# Check audit log for rate_limit_hit events:
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?type=rate_limit_hit"
```

---

## 6. Audit logging

Every security and operational event is written to an append-only log in BadgerDB.

### Event types

| Event | Trigger |
|---|---|
| `node_register` | Node registers with coordinator |
| `node_revoke` | Node certificate revoked via API |
| `job_submit` | Job submitted via REST API |
| `job_state_transition` | Job status changed (any transition) |
| `auth_failure` | JWT missing, expired, revoked, or invalid |
| `rate_limit_hit` | Per-node rate limit exceeded |
| `security_violation` | Seccomp or OOMKilled reported by node |
| `coordinator_start` | Coordinator process started |
| `coordinator_stop` | Coordinator process stopping (graceful shutdown) |
| `analytics.query` | Authenticated call to a `/api/analytics/*` endpoint. `details` carries `endpoint`, `from`, `to`, `actor`. |

### Storage

- Key: `audit:{timestamp_nanos}:{event_id}` (time-ordered)
- Default TTL: 90 days; set `HELION_AUDIT_TTL=0` to disable expiry
- Never updated, never deleted in normal operation

### Query API

```bash
# Paginated events
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?page=1&size=50"

# Filter by type
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?type=job_submit"

# Count by type
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit" | jq '.events[] | .type' | sort | uniq -c
```

Response format:

```json
{
  "events": [
    {
      "id": "event-123",
      "timestamp": "2026-04-10T12:34:56Z",
      "type": "job_submit",
      "actor": "root",
      "details": { "job_id": "job-xyz", "command": "echo" }
    }
  ],
  "total": 100,
  "page": 1,
  "size": 50
}
```

---

## 7. Node revocation

```bash
# Revoke a node certificate
curl -X POST \
  -H "Authorization: Bearer $ROOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"reason": "security incident"}' \
  https://coordinator:8443/admin/nodes/{nodeID}/revoke

# Expected: {"success": true, "message": "node revoked"}
```

After revocation:

1. The node ID is added to the coordinator's in-memory revocation set.
2. Any subsequent gRPC call from that node is rejected with `Unauthenticated` by the
   revocation interceptor (checked before any RPC handler runs).
3. The node must re-register with a new certificate to participate again.
4. A `node_revoke` audit event is written.

---

## 8. REST API security

### Authentication middleware

All endpoints except `/healthz` and `/readyz` require a valid JWT in the `Authorization`
header:

```
Authorization: Bearer <token>
```

On missing or invalid token: `401 Unauthorized`.

### Security endpoints

| Endpoint | Auth | Description |
|---|---|---|
| `POST /admin/nodes/{id}/revoke` | Required | Revoke a node certificate |
| `GET /audit` | Required | Query audit log (paginated, filterable by type) |
| `GET /healthz` | None | Liveness probe — always 200 OK |
| `GET /readyz` | None | Readiness probe — 200 after BadgerDB open + node registered |

### Actor attribution

When a request carries a valid JWT, the `claims.Subject` field is extracted from the token
and recorded as the `actor` in any audit events generated by that request. Unauthenticated
paths record `actor = "anonymous"`.

---

## 9. Dashboard security

- JWT stored in memory only. Never `localStorage`, `sessionStorage`, or a cookie. Lost on
  page refresh — user re-enters the token.
- HTTP interceptor attaches `Authorization: Bearer {token}` to every outbound request. On
  `401`, clears token and redirects to login.
- `AuthGuard` blocks navigation to protected routes if no token is present.
- WebSocket authentication uses first-message pattern: the JWT is sent as the first
  frame after `onopen`, never as a URL query parameter. This prevents token leakage
  via server access logs, browser history, and `Referer` headers.
- Error banners display generic messages only. Raw error details are logged to
  `console.error` — never rendered in the UI.
- Nginx CSP header: no inline scripts, no eval, same-origin only.

---

## 10. Operational guide & troubleshooting

See [SECURITY-OPS.md](SECURITY-OPS.md) for environment variables, first-start
checklist, production recommendations, and troubleshooting common issues.

---

## 11. References

- [NIST FIPS 203: ML-KEM (Kyber)](https://csrc.nist.gov/pubs/fips/203/final)
- [NIST FIPS 204: ML-DSA (Dilithium)](https://csrc.nist.gov/pubs/fips/204/final)
- [Cloudflare circl library](https://github.com/cloudflare/circl)
- [RFC 7519: JSON Web Token (JWT)](https://datatracker.ietf.org/doc/html/rfc7519)
- [golang.org/x/time/rate — token bucket](https://pkg.go.dev/golang.org/x/time/rate)
