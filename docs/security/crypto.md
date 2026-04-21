> **Audience:** engineers + operators
> **Scope:** TLS, PQC, cert lifecycle, node + operator-cert revocation, envelope encryption of secrets at rest.
> **Depth:** reference

# Security — crypto and certificates

Everything that cryptographically protects traffic or data at rest:
coordinator↔node mTLS, hybrid post-quantum key exchange, the internal CA, node
and operator certificate lifecycles, revocation (nodes + operator certs), and
envelope encryption of secret env values in BadgerDB.

## 1. mTLS and certificate architecture

All coordinator↔node communication is mutually authenticated via mTLS.

### Certificate issuance flow

1. Node starts, finds no certificate on disk.
2. Node calls `Register` RPC with its node ID.
3. Coordinator's internal CA generates an ECDSA P-256 + ML-DSA-65 key pair,
   signs a certificate for the node, and returns it in `RegisterResponse`.
4. Node persists the certificate and uses it for all subsequent
   connections, including as the server cert on its own gRPC listener — the
   coordinator verifies node certs during job dispatch.

### Storage

- Coordinator stores DER bytes under `certs/{nodeID}` in BadgerDB (no expiry).
- Node stores its certificate on the local filesystem.

### TLS configuration

The coordinator builds a `tls.Config` with
`ClientAuth: tls.RequireAndVerifyClientCert`. Each gRPC connection is rejected
at the TLS handshake if the node certificate cannot be verified against the
internal CA. Revoked node IDs are also checked in a unary interceptor before
any RPC handler runs.

## 2. Post-quantum cryptography

### Hybrid key exchange (ML-KEM / Kyber-768)

TLS key exchange uses a hybrid mode: X25519 (classical) **and** ML-KEM-768
(post-quantum) are both negotiated in the same ClientHello. The session key
is derived from both; breaking the session requires breaking both
simultaneously.

- Curve ID: `x25519_mlkem768` (`0x6399`)
- Enabled by default in Go 1.26+
- Implemented in `internal/pqcrypto/hybrid.go` using the Cloudflare `circl`
  library (ML-KEM primitives from NIST FIPS 203)

**Surfaces covered.** Hybrid-KEM applies to BOTH coordinator-facing
listeners:

1. **coordinator ↔ node (gRPC, :9090)** — wired since the initial PQC pass
   via `ServerCredentials()` + `ClientCredentials()` on `auth.Bundle`.
2. **coordinator ↔ dashboard / in-workflow scripts (REST + WebSocket, :8080)**
   — added in feature 23. `api.Server.ServeTLS(addr, cfg)` expects a
   `*tls.Config` built via `bundle.CA.EnhancedTLSConfig(certPEM, keyPEM)`,
   the exact same path the gRPC listener uses. Default-on since feature 39
   retired the `HELION_REST_TLS=off` opt-out.

**Strict-mode enforcement.** Set `HELION_PQC_REQUIRED=1` on the
coordinator to fail startup if `ApplyHybridKEM` silently produced a config
without the Kyber curve (e.g. on a Go runtime with `GODEBUG=tlskyber=0`).
Without this flag the coordinator falls back to classical-only curves when
the runtime does not support Kyber; with the flag it refuses to start,
guaranteeing the production posture never silently downgrades.

**Why hybrid PQC at all?** The primary answer is "better safe than sorry."
Helion is a student learning project, not a production system — but wiring
hybrid PQC at design time costs relatively little (mostly Go + Chromium's
existing support; no new servers, no key-management overhead), and building
the habit on a non-production codebase means the same patterns land
correctly if the project ever is taken to production. The posture is
safety-by-default.

**Why not classical-only?** The secondary argument is
harvest-now-decrypt-later: an adversary could record encrypted
coordinator↔node traffic today and decrypt it once a sufficiently powerful
quantum computer exists. This matters most for systems that are actually
deployed and see real traffic — Helion is neither, so HNDL is a longevity
concern here rather than a live threat.

**Verification with Wireshark:**

```bash
tcpdump -i any -w helion.pcap port 50051
# Open in Wireshark → filter: tls.handshake.type == 1
# ClientHello → Extension: supported_groups
# Should contain: x25519_mlkem768 (0x6399)
```

### ML-DSA node certificate signing

Node certificates carry a dual signature: ECDSA P-256 (classical) **and**
ML-DSA-65 (Dilithium-3, NIST FIPS 204). The coordinator verifies both
signatures on registration.

- Implemented in `internal/pqcrypto/mldsa.go` and
  `internal/pqcrypto/ca.go`
- A certificate with a tampered signature is rejected at the `Register`
  RPC.

**Tampering test:**

```bash
# Modify any byte in a node certificate, then attempt registration:
xxd -p node.crt | sed 's/00/FF/1' | xxd -r -p > node_tampered.crt
# Expected: gRPC Unauthenticated — ML-DSA signature invalid
```

## 3. Node revocation

```bash
curl -X POST \
  -H "Authorization: Bearer $ROOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"reason": "security incident"}' \
  https://coordinator:8443/admin/nodes/{nodeID}/revoke

# Expected: {"success": true, "message": "node revoked"}
```

After revocation:

1. The node ID is added to the coordinator's in-memory revocation set.
2. Any subsequent gRPC call from that node is rejected with
   `Unauthenticated` by the revocation interceptor (checked before any
   RPC handler runs).
3. The node must re-register with a new certificate to participate again.
4. A `node_revoke` audit event is written.
5. An active gRPC heartbeat stream closes immediately on revocation via
   a done channel — not just on the next RPC.

## 4. Operator-cert revocation (feature 31)

Feature 27 (see [operator-auth.md § 1](operator-auth.md#1-browser-mtls-feature-27))
ships TTL-based operator certs (default 90 days). Without revocation, a
leaked PKCS#12 file is usable until expiry. Feature 31 closes that gap
with an append-only revocation set + admin endpoint + signed CRL.

### Endpoints

```
POST /admin/operator-certs/{serial}/revoke
  body: {"reason": "<required>", "common_name": "<optional>"}
  → 201 Created (new) | 200 OK (idempotent repeat)

GET  /admin/operator-certs/revocations
  → {"revocations": [...], "total": N}

GET  /admin/ca/crl
  → PEM-encoded X.509 CRL, signed by the CA
```

All three are admin-only (feature 37 `ActionAdmin`). The CRL export sets
`Content-Type: application/x-pem-file` so browser fetches save it
verbatim.

### Verification hook

After TLS handshake verifies the chain, `clientCertMiddleware` extracts
the peer's serial and queries the revocation set (O(1) in-memory lookup).
A revoked cert:

- **`on` tier:** rejected 401 with body `"client certificate is
  revoked"`. `EventOperatorCertRevokedUsed` records the serial, CN,
  remote addr, `"enforced": true`.
- **`warn` tier:** request proceeds WITHOUT a stamped Operator principal
  — treated as cert-less, so feature 37 authz refuses any operator-scoped
  action. The event is still recorded (no `enforced` flag) plus
  `EventOperatorCertMissing` with
  `reason: "revoked_cert_treated_as_certless"`.

### Safety properties

- **Append-only.** The store exposes no Delete primitive. An "unrevoke" is
  a NEW cert issuance, not a deletion of the revocation record.
  Revocations persist forever (TTL 0) — part of audit history.
- **Idempotent revoke.** Re-posting the same serial returns the ORIGINAL
  record (with whatever reason the first call supplied) + `"idempotent":
  true`. Double-clicking doesn't produce duplicate audit entries.
- **Audit-before-response.** Admin endpoint writes the audit event BEFORE
  sending response. Downed audit sink → 500 and no leak — fail-closed on
  accountability.
- **O(1) hot-path lookup.** `IsRevoked` is called on every authenticated
  request in `warn`/`on` tiers; in-memory cache guarantees a map lookup
  under RWMutex.
- **Persistent truth.** Every revoke writes through Badger first; cache
  rebuilt from Badger at startup. No revocation is RAM-only.
- **Defensive serial normalisation.** `abcd`, `ABCD`, `0xabcd`, and
  `  AbCd\n` all address the same record. Case / prefix / whitespace
  mismatches between issuance audit lines and admin revoke requests
  don't cause missed enforcement.
- **Nginx-terminated deployments.** CRL export designed for Nginx's
  `ssl_crl` directive. Nginx rejects revoked certs at the TLS handshake
  — requests never reach the coordinator. Belt-and-braces: the
  coordinator also accepts `X-SSL-Client-Serial` from loopback peers and
  applies the same check when the header is present.

### Operator playbook

```bash
# Revoke a leaked cert.
curl -X POST https://helion.example.com/admin/operator-certs/deadbeef/revoke \
  -H "Authorization: Bearer $ADMIN_JWT" \
  -H "Content-Type: application/json" \
  -d '{"reason": "laptop stolen 2026-04-20", "common_name": "alice@ops"}'

# Export CRL for Nginx.
curl -H "Authorization: Bearer $ADMIN_JWT" \
     https://helion.example.com/admin/ca/crl \
     -o /etc/nginx/helion-ca.crl
nginx -s reload

# List current revocations.
curl -H "Authorization: Bearer $ADMIN_JWT" \
     https://helion.example.com/admin/operator-certs/revocations | jq
```

See also [operators/cert-rotation.md](../operators/cert-rotation.md) for the
full rotation runbook.

### Known limitations

- **Accidental self-revoke lockout.** An admin who revokes their OWN
  current cert in `on` tier cannot then reach the admin endpoint to fix
  it. Recovery: JWT-only admin auth via a coordinator-console container
  that doesn't present a client cert.
- **Nginx CRL staleness.** Nginx reloads `ssl_crl` on file change.
  Cron-fetched revocations lag by `cron_interval`. Coordinator-side check
  (direct TLS or loopback header) closes the gap for requests that reach
  the coordinator.
- **No OCSP.** RFC 6960 responder is explicitly out of scope; CRL covers
  95% of operator needs.
- **No cross-coordinator sync.** Helion is single-coordinator today; a
  multi-coordinator deployment would need a shared revocation store (out
  of scope until Helion gains HA).

Spec: [planned-features/implemented/31-cert-revocation-crl-ocsp.md](../planned-features/implemented/31-cert-revocation-crl-ocsp.md).

## 5. Encrypted env storage (feature 30)

Feature 26 redacts secret env values on every response path. Feature 29
scrubs them from log output. Feature 30 closes the last gap: before
feature 30, the `cpb.Job.Env` map still reached BadgerDB as JSON plaintext,
so an attacker with filesystem / backup access to the coordinator grepped
the Badger store for every HF token, AWS key, or API secret.

Feature 30 applies **envelope encryption at the persistence boundary**:

```
plaintext  ── AES-256-GCM(DEK, nonce_v) ─▶ ciphertext
DEK        ── AES-256-GCM(KEK, nonce_d) ─▶ wrapped_DEK
```

The DEK ("data encryption key") is a fresh 32-byte value per encrypt. The
KEK ("key encryption key") is a single 32-byte secret read from
`HELION_SECRETSTORE_KEK` at coordinator boot. Both layers use AES-256-GCM
with 12-byte random nonces.

### Safety properties

- **Authenticated encryption.** AES-GCM's 16-byte tag detects any bit flip
  in ciphertext, nonce, wrapped-DEK, or wrapped-DEK nonce. A tampered
  envelope fails Decrypt with no plaintext byte output.
- **Fresh DEK per encrypt.** Two encrypts of the same plaintext produce
  distinct ciphertext.
- **Nonces from crypto/rand.** 12 random bytes per encrypt; at realistic
  per-value volumes the collision probability is negligible, and the DEK
  is single-use anyway.
- **Persistence-boundary only.** Encryption in `SaveJob` /
  `SaveWorkflow`; decryption in `LoadAllJobs` / `LoadAllWorkflows`. Every
  in-memory reader (dispatch, reveal-secret, log-scrub, response-redaction)
  keeps working unchanged.
- **Fail-closed on wrong KEK / tampered bytes.** A Job whose envelope
  cannot be decrypted blocks load: coordinator refuses to start rather
  than silently dropping records.
- **Legacy records still load.** Pre-feature-30 records with plaintext
  Env and no `EncryptedEnv` continue to load. The next SaveJob (any state
  transition) rewrites them under envelope encryption.
- **No-keyring fallback.** Deployments that don't configure
  `HELION_SECRETSTORE_KEK` still run — secret values persist in plaintext
  exactly as pre-feature-30. Coordinator logs a `WARN` at boot so
  operators see the gap. Production should always configure the KEK.
- **KEK wipe on drop.** `KeyRing.RemoveKEK` zeros the key bytes before
  removing them from the map. Best-effort — Go has no guaranteed
  non-elided erase — but shortens the window a core dump can capture.

### KEK configuration

32 bytes of key material via env var. Encodings: 64-char hex or standard
/ URL-safe base64 (with or without padding). Both decode to exactly 32
bytes; shorter / longer inputs rejected at boot.

```bash
# Hex.
export HELION_SECRETSTORE_KEK=$(openssl rand -hex 32)

# Or base64.
export HELION_SECRETSTORE_KEK=$(openssl rand -base64 32)

# Optional — explicit version. Defaults to 1.
export HELION_SECRETSTORE_KEK_VERSION=2

# Older versions during a rotation window:
export HELION_SECRETSTORE_KEK_V1=<prior hex KEK>
```

Versions are 1–16 (realistically a rotation uses 2–3 at a time). All-zero
keys and keys of wrong length are rejected.

### Rotation

1. Generate a new KEK, assign version N+1.
2. Keep old KEK loaded as `HELION_SECRETSTORE_KEK_V<old>` and set the new
   one as `HELION_SECRETSTORE_KEK` with
   `HELION_SECRETSTORE_KEK_VERSION=<N+1>`.
3. Restart. New encrypts use N+1; existing records still decrypt under
   their recorded version.
4. `curl -X POST /admin/secretstore/rotate` (admin-auth). Sweeps every
   persisted Job + WorkflowJob, rewrapping every non-active envelope
   under N+1. Idempotent — safe to retry.
5. Once the sweep reports zero scanned records, drop
   `HELION_SECRETSTORE_KEK_V<old>` and restart.

`secretstore_rotate` audit event records every sweep (success AND
failure) with active + loaded versions and rewrap counts.

### Known limitations

- **In-memory plaintext.** Once a Job is loaded, its `Env[secretKey]`
  holds plaintext (dispatch needs it). A process memory dump while the
  coordinator is live leaks every live secret AND the KEK itself.
  "Coordinator core dump" is explicitly in the key-compromise bucket.
- **Single coordinator-wide KEK.** Per-tenant KEKs deferred.
- **No passphrase derivation.** Operators supply a 32-byte key directly.
  KDF-from-passphrase adds complexity without improving the surface — the
  env var IS the secret.
- **No auto-expiry / forward secrecy.** KEKs live as long as the operator
  leaves them configured.

Spec: [planned-features/implemented/30-encrypted-env-storage.md](../planned-features/implemented/30-encrypted-env-storage.md).
