# Helion v2 - Phase 4: Security Hardening

## Overview

Phase 4 implements comprehensive security hardening with post-quantum cryptography, JWT authentication, rate limiting, and complete audit logging.

## Features Implemented

### 1. Post-Quantum Cryptography (PQC)

#### Hybrid Key Exchange
- **X25519 + ML-KEM-768 (Kyber)** hybrid key exchange
- Uses Go 1.23 experimental X25519MLKEM768 curve ID (0x6399)
- Falls back to classical X25519 if peer doesn't support hybrid
- Provides harvest-now-decrypt-later (HNDL) resistance

**Enable hybrid KEM:**
```bash
export GODEBUG=tlskyber=1  # Go 1.23+ experimental PQC support
```

**Verify with Wireshark:**
```bash
# Capture TLS handshake
tcpdump -i any -w helion.pcap port 50051

# Open in Wireshark and check:
# TLS > ClientHello > Extension: supported_groups
# Should contain: x25519_mlkem768 (0x6399)
```

#### ML-DSA Certificate Signing
- **Dilithium-3 (ML-DSA-65)** signatures on node certificates
- Dual-signature approach: ECDSA + ML-DSA
- Coordinator verifies both signatures on node registration
- Tampered certificates rejected

**Implementation:**
- `internal/pqcrypto/hybrid.go` - Hybrid KEM (X25519+ML-KEM-768)
- `internal/pqcrypto/mldsa.go` - ML-DSA signatures (Dilithium-3)

### 2. JWT Authentication

#### Token Properties
- **Short-lived tokens**: 15-minute expiry (configurable)
- **JTI tracking**: Unique token ID stored in BadgerDB
- **Revocation support**: Delete JTI to revoke token
- **HS256 signing**: 256-bit secret key

#### Root Token
Generated on first coordinator start:
```bash
╔════════════════════════════════════════════════════════════════╗
║         HELION COORDINATOR - FIRST START                       ║
╠════════════════════════════════════════════════════════════════╣
║ Root API token generated. Save this token securely!            ║
╠════════════════════════════════════════════════════════════════╣
║ Token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...                 ║
╚════════════════════════════════════════════════════════════════╝
```

**Save the root token** - it's printed only once and provides admin access to all API endpoints.

#### Usage
```bash
# Authenticate API requests
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  https://coordinator:8443/jobs

# WebSocket authentication (token in query param)
wscat -c "wss://coordinator:8443/ws/metrics?token=$ROOT_TOKEN"
```

#### Revocation
Tokens are revoked by deleting their JTI from BadgerDB:
```bash
curl -X POST -H "Authorization: Bearer $ROOT_TOKEN" \
  https://coordinator:8443/admin/tokens/revoke \
  -d '{"jti": "550e8400-e29b-41d4-a716-446655440000"}'
```

Revoked tokens are rejected within **1 second** (Phase 4 exit criteria).

**Implementation:**
- `internal/auth/jwt.go` - Complete JWT system

### 3. Rate Limiting

#### Configuration
```bash
# Set custom rate limit (default: 10 jobs/s per node)
export HELION_RATE_LIMIT_RPS=20

# Start coordinator
./coordinator
```

#### Behavior
- **Per-node limits**: Each node has independent rate limit
- **Token bucket algorithm**: Allows bursts up to the rate limit
- **gRPC status**: Returns `ResourceExhausted` when limit exceeded
- **Audit logging**: Rate limit hits logged to audit log

#### Load Test Validation
```bash
# Submit 100 jobs/s (should be rate limited to 10 jobs/s)
for i in {1..1000}; do
  helion-run echo "job $i" &
done
wait

# Check audit log for rate_limit_hit events
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?type=rate_limit_hit"
```

**Expected behavior:**
- First 10 jobs succeed immediately (burst)
- Sustained rate limited to 10 jobs/s
- Excess requests return `ResourceExhausted`

**Implementation:**
- `internal/ratelimit/limiter.go` - Per-node rate limiter

### 4. Audit Logging

#### Event Types
All security and operational events are logged:

- `node_register` - Node registers with coordinator
- `node_revoke` - Node certificate revoked
- `job_submit` - Job submitted via API
- `job_state_transition` - Job status changed
- `auth_failure` - Authentication failed
- `rate_limit_hit` - Rate limit exceeded
- `coordinator_start` - Coordinator started
- `coordinator_stop` - Coordinator stopped

#### Storage
- Events stored in BadgerDB with time-ordered keys
- TTL: 90 days (configurable, 0 = no expiry)
- Key format: `audit:<timestamp_nanos>:<event_id>`

#### Query API
```bash
# Get all audit events (paginated)
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?page=1&size=50"

# Filter by event type
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?type=job_submit"
```

**Implementation:**
- `internal/audit/logger.go` - Audit logging system

### 5. REST API Endpoints (Phase 3 Completion)

#### All endpoints require JWT authentication (except `/healthz`)

**Node Management:**
```bash
# List all registered nodes
GET /nodes
Response: {
  "nodes": [
    {
      "id": "node-abc123",
      "health": "healthy",
      "last_seen": "2026-04-09T12:34:56Z",
      "running_jobs": 3,
      "address": "10.0.1.5:50051"
    }
  ],
  "total": 1
}

# Revoke node certificate
POST /admin/nodes/{id}/revoke
Body: {"reason": "security incident"}
Response: {"success": true, "message": "node revoked"}
```

**Job Management:**
```bash
# List jobs (paginated, filterable)
GET /jobs?page=1&size=20&status=running
Response: {
  "jobs": [...],
  "total": 42,
  "page": 1,
  "size": 20
}

# Get single job
GET /jobs/{id}
Response: {
  "id": "job-xyz789",
  "command": "echo",
  "args": ["hello"],
  "status": "COMPLETED",
  "node_id": "node-abc123",
  "created_at": "2026-04-09T12:00:00Z",
  "finished_at": "2026-04-09T12:00:01Z"
}
```

**Metrics:**
```bash
# Get cluster metrics snapshot
GET /metrics
Response: {
  "nodes": {"total": 5, "healthy": 5},
  "jobs": {
    "running": 10,
    "pending": 2,
    "completed": 1000,
    "failed": 5,
    "total": 1017
  },
  "timestamp": "2026-04-09T12:34:56Z"
}
```

**Audit Log:**
```bash
# Query audit events
GET /audit?page=1&size=50&type=job_submit
Response: {
  "events": [
    {
      "id": "event-123",
      "timestamp": "2026-04-09T12:34:56Z",
      "type": "job_submit",
      "actor": "root",
      "details": {"job_id": "job-xyz", "command": "echo"}
    }
  ],
  "total": 100,
  "page": 1,
  "size": 50
}
```

### 6. WebSocket Streaming

#### Job Log Streaming
```javascript
// Connect to job log stream
const ws = new WebSocket(`wss://coordinator:8443/ws/jobs/${jobId}/logs?token=${token}`);

ws.onmessage = (event) => {
  const logChunk = JSON.parse(event.data);
  console.log(logChunk.message);
};
```

#### Metrics Streaming
```javascript
// Connect to metrics stream (updates every 5 seconds)
const ws = new WebSocket(`wss://coordinator:8443/ws/metrics?token=${token}`);

ws.onmessage = (event) => {
  const metrics = JSON.parse(event.data);
  updateDashboard(metrics);
};
```

## Testing

### Unit Tests
```bash
# Run all tests
go test ./...

# Run security tests only
go test ./tests/integration/security/...

# Run with verbose output
go test -v ./tests/integration/security/...
```

### Integration Tests

#### JWT Authentication
```bash
go test -v ./tests/integration/security/jwt_test.go

# Tests:
# - Token generation and validation
# - Token expiry
# - Token revocation (< 1 second)
# - Reuse after revocation (rejected)
# - Invalid signatures rejected
# - Malformed tokens rejected
```

#### Rate Limiting
```bash
go test -v ./tests/integration/security/rate_limit_test.go

# Tests:
# - Basic rate limiting
# - Sustained rate enforcement
# - Per-node independence
# - Configurable rate limits
# - Load test (100 jobs/s → 10 jobs/s)
# - Concurrent access
```

#### Load Test
```bash
# Run load test (takes 10 seconds)
go test -v -run TestRateLimitLoadTest ./tests/integration/security/

# Expected output:
# Load test over 10s: Allowed: ~100, Rejected: ~900
# Sustained rate: ~10.0 jobs/s
```

## Security Verification

### TLS Cipher Suite Verification

**Using Wireshark:**
1. Capture coordinator traffic: `tcpdump -i any -w helion.pcap port 50051`
2. Open in Wireshark
3. Filter: `tls.handshake.type == 1`
4. Check ClientHello → Extension: supported_groups
5. Verify `x25519_mlkem768 (0x6399)` is present

**Using OpenSSL:**
```bash
# This won't work for hybrid KEM (OpenSSL doesn't support it yet)
# Use Wireshark instead
openssl s_client -connect coordinator:50051 -tls1_3
```

### Certificate Tampering Test
```bash
# Generate node certificate
./helion-node --register-only

# Tamper with certificate (modify any byte)
xxd -p node.crt | sed 's/00/FF/' | xxd -r -p > node_tampered.crt

# Try to register with tampered cert
./helion-node --cert node_tampered.crt

# Expected: Connection rejected (ML-DSA signature invalid)
```

### Token Revocation Timing Test
```bash
# Generate token
TOKEN=$(curl -s -X POST -H "Authorization: Bearer $ROOT_TOKEN" \
  https://coordinator:8443/admin/tokens/generate \
  -d '{"subject": "test", "role": "node"}' | jq -r .token)

# Use token (should succeed)
curl -H "Authorization: Bearer $TOKEN" https://coordinator:8443/jobs

# Revoke token and measure rejection time
START=$(date +%s%3N)
curl -X POST -H "Authorization: Bearer $ROOT_TOKEN" \
  https://coordinator:8443/admin/tokens/revoke -d "{\"token\": \"$TOKEN\"}"

# Try to use revoked token
curl -H "Authorization: Bearer $TOKEN" https://coordinator:8443/jobs
END=$(date +%s%3N)

echo "Rejection time: $((END - START)) ms"
# Expected: < 1000 ms (Phase 4 exit criteria)
```

## Dependencies

```go
require (
    github.com/cloudflare/circl v1.3.7       // ML-KEM + ML-DSA
    github.com/golang-jwt/jwt/v5 v5.2.0      // JWT handling
    github.com/gorilla/websocket v1.5.1      // WebSocket support
    golang.org/x/time v0.5.0                 // Rate limiting
)
```

## Configuration

### Environment Variables

```bash
# Rate limiting
export HELION_RATE_LIMIT_RPS=10           # Jobs per second per node

# JWT (secrets stored in BadgerDB, auto-generated)
# export HELION_JWT_SECRET=<auto-generated>  # Don't set manually

# TLS / PQC
export GODEBUG=tlskyber=1                  # Enable Go 1.23 hybrid KEM

# Coordinator
export HELION_COORDINATOR_ADDR=0.0.0.0:50051
export HELION_API_ADDR=0.0.0.0:8443
```

## Phase 4 Exit Criteria

- [x] TLS handshake uses hybrid key exchange (X25519+ML-KEM-768)
- [x] Node certificates include ML-DSA signatures (Dilithium-3)
- [x] Revoked tokens rejected within 1 second
- [x] Expired tokens rejected
- [x] Token reuse after revocation rejected
- [x] Revoked node must re-register with new certificate
- [x] Load test: 100 jobs/s limited to 10 jobs/s
- [x] ResourceExhausted status on rate limit
- [x] Complete audit trail for job lifecycle
- [x] All REST endpoints require JWT auth
- [x] WebSocket log streaming works
- [x] Integration tests pass

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Helion Coordinator                        │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐     ┌──────────────┐     ┌──────────────┐│
│  │   REST API   │     │  gRPC Server │     │  BadgerDB    ││
│  │  (port 8443) │     │ (port 50051) │     │  Persistence ││
│  └───────┬──────┘     └───────┬──────┘     └──────┬───────┘│
│          │                    │                    │        │
│          ├──── JWT Auth ──────┤                    │        │
│          │   (15min expiry)   │                    │        │
│          │   JTI in BadgerDB  │                    │        │
│          │                    │                    │        │
│          ├──── Rate Limit ────┤                    │        │
│          │   (10 jobs/s/node) │                    │        │
│          │                    │                    │        │
│          └──── Audit Log ─────┴────────────────────┘        │
│                 (90d TTL)                                    │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │         mTLS + Hybrid PQC (X25519+ML-KEM-768)        │  │
│  │         Certs signed with ECDSA + ML-DSA-65          │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

## Next Steps (Phase 5)

Potential future enhancements:

1. **Certificate rotation**: Auto-renew certificates before expiry
2. **Audit log export**: Export to external SIEM (Elasticsearch, Splunk)
3. **RBAC**: Role-based access control (admin, operator, read-only)
4. **mTLS for HTTP API**: Extend PQC to REST API (currently gRPC only)
5. **Distributed coordinator**: Multi-coordinator HA with etcd
6. **Token refresh**: Refresh tokens to reduce re-authentication

## References

- [NIST FIPS 203: ML-KEM](https://csrc.nist.gov/pubs/fips/203/final)
- [NIST FIPS 204: ML-DSA](https://csrc.nist.gov/pubs/fips/204/final)
- [Cloudflare circl library](https://github.com/cloudflare/circl)
- [RFC 7519: JSON Web Token (JWT)](https://datatracker.ietf.org/doc/html/rfc7519)
- [Go rate limiting](https://pkg.go.dev/golang.org/x/time/rate)
