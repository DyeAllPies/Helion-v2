> **Audience:** operators
> **Scope:** JWT token lifecycle — issuance, usage, revocation.
> **Depth:** reference

# Helion v2 — JWT Authentication Guide

Complete reference for JWT token lifecycle, issuance, usage, and revocation.
For the broader security model, see [SECURITY.md](../SECURITY.md).

---

## Token properties

| Property | Value |
|---|---|
| Algorithm | HS256 (256-bit secret, auto-generated on first start) |
| Normal token expiry | 15 minutes |
| Root token expiry | 10 years |
| Revocation mechanism | Delete JTI record from BadgerDB |
| Revocation latency | < 1 second |

---

## Root token rotation

The coordinator **rotates** the root token on **every restart** by default. On startup it
revokes the previous token's JTI and issues a fresh one, then writes it to the path
specified by `HELION_TOKEN_FILE` (default: `/var/lib/helion/root-token`, mode `0600`).
Set `HELION_ROTATE_TOKEN=false` to reuse the stored token across restarts (useful for
automation that cannot read the token file on every restart).

This eliminates the "10-year never-expiring token" problem: a token leaked from a prior run
is invalidated automatically the moment the coordinator restarts. The current root token is
stored in BadgerDB; if BadgerDB is wiped a new token is generated on the next start.

---

## Issuing scoped tokens

Use the root token to issue short-lived, role-scoped tokens for operators or services:

```bash
# Issue an admin token valid for 8 hours (default)
curl -s -X POST https://coordinator:8443/admin/tokens \
  -H "Authorization: Bearer $ROOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"subject":"alice","role":"admin","ttl_hours":8}' \
  | jq -r .token

# Issue a node-role token valid for 1 hour
curl -s -X POST https://coordinator:8443/admin/tokens \
  -H "Authorization: Bearer $ROOT_TOKEN" \
  -d '{"subject":"ci-runner","role":"node","ttl_hours":1}' \
  | jq -r .token
```

Roles: `admin` (full access) / `node` (job submission and result reporting only, RBAC wiring in progress).
Maximum TTL: 720 hours (30 days).

---

## Token usage

```bash
# REST API
curl -H "Authorization: Bearer $ROOT_TOKEN" https://coordinator:8443/jobs

# WebSocket (first-message auth — token sent as first frame after connect)
# The client sends {"type":"auth","token":"<jwt>"} immediately after the
# WebSocket handshake completes. The server validates and replies with
# {"type":"auth_ok"} before streaming data. Tokens never appear in URLs.
wscat -c "wss://coordinator:8443/ws/metrics"
# then send: {"type":"auth","token":"$ROOT_TOKEN"}
```

---

## Revocation

Token revocation works by deleting the JTI record from BadgerDB. `ValidateToken` checks
for JTI presence on every call — if the record is absent the token is rejected immediately.

```bash
# Extract JTI from token
JTI=$(echo $TOKEN | cut -d. -f2 | base64 -d 2>/dev/null | jq -r .jti)

# Revoke immediately via the API (admin role required)
curl -s -X DELETE https://coordinator:8443/admin/tokens/$JTI \
  -H "Authorization: Bearer $ROOT_TOKEN"
```

**Timing test:**

```bash
START=$(date +%s%3N)
# revoke ...
curl -H "Authorization: Bearer $TOKEN" https://coordinator:8443/jobs
END=$(date +%s%3N)
echo "Rejection latency: $((END - START)) ms"
# Expected: < 1000 ms
```
