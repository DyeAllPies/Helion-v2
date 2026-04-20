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
| Compromised node reports a cross-job artifact URI (claims job A's bytes live under job B's prefix) | `attestOutputs` rejects any `ResolvedOutputs` URI that doesn't match `<scheme>://<bucket>/jobs/<reporting-job-id>/<local_path>` (feature 13). A compromised node can only report URIs under its own running job's prefix. |
| Compromised node reports an undeclared output name (invents names the downstream resolver would accept) | `attestOutputs` cross-checks every reported `Name` against `Job.Outputs` (the submit-time declaration); undeclared names are dropped (feature 12 audit 2026-04-15-05). |
| Compromised node serves tampered artifact bytes under a valid URI | Downstream node's Stager runs `artifacts.GetAndVerify` — reads into a capped buffer, hashes, returns `ErrChecksumMismatch` if the digest doesn't match the SHA-256 the upstream committed with the URI. Catches leaked S3-creds tamper, bit rot, and store-side MITM. |
| Leaked workflow token escalates to admin (mints more tokens, revokes nodes) | `submit.py` mints a `job`-role token per workflow (subject `workflow:<id>`, TTL 1 h); `adminMiddleware` returns 403 for `/admin/*` endpoints on the `job` role. Pre-feature-19 wiring had `POST /admin/nodes/{id}/revoke` missing `adminMiddleware`; fixed in the same commit that added the role. |
| Leaked workflow token persists after the pipeline finishes | 1-hour TTL; operator can `DELETE /admin/tokens/{jti}` to invalidate immediately. `submit.py` falls back to the root admin token with a stderr warning on older coordinator builds that lack the `job` role. |
| ML-pipeline DoS via oversize artifact metadata | `http.MaxBytesReader` at 1 MiB on `POST /api/datasets` and `POST /api/models` — shared with the job-submit handler. Rate-limited per-subject via `registryQueryAllow` (2 rps burst 30). |
| CPU job on a GPU-equipped node escapes per-job GPU pinning by setting its own `CUDA_VISIBLE_DEVICES` | Runtime stamps `CUDA_VISIBLE_DEVICES=""` into the subprocess env map (not via OS env precedence) for `req.GPUs == 0` on nodes with `allocator.Capacity() > 0`. Map-based override is unambiguous — no platform-dependent first/last-set-wins. |
| Malicious service job binds a privileged port or hides behind a non-loopback probe | `validateServiceSpec` rejects `port < 1024`, `port > 65535`, non-absolute `health_path`, whitespace/NUL in the path, and `health_initial_ms > 30 min`. The prober binds `127.0.0.1` only — coordinator never proxies a service, a client that needs external access puts an Nginx in front. |
| Attacker with submit permission hijacks subprocess execution on the node via a dynamic-loader env var (`LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, `LD_LIBRARY_PATH`, `GCONV_PATH`, …) | **Feature 25.** Centralised denylist (`internal/api/env_denylist.go`) rejects every env key matching `LD_*`/`DYLD_*` prefixes or one of five exact glibc module-loading names. Applied on `POST /jobs`, `POST /workflows` (per child), and under `?dry_run=true`. Rust runtime's `env_clear()` is defence-in-depth; the denylist is load-bearing on the Go runtime. Every rejection emits an `env_denylist_reject` audit event. |
| Attacker stages a file:// artifact pointing at a system library or secret-material path (`/lib/libc.so.6`, `/proc/self/environ`, `/var/run/secrets/...`) | **Feature 25.** `validateArtifactBindingsCtx` refuses `file://` URIs rooted at `/lib`, `/lib64`, `/usr/lib`, `/usr/lib64`, `/proc`, `/sys`, `/dev`, `/etc`, `/boot`, `/root`, `/var/run/secrets`, `/run/secrets`, `/run/credentials`. Path is `path.Clean`-normalised first so `//` tricks don't bypass. |
| Attacker stages a loader-critical library (`libc.so.6`, `ld-linux-x86-64.so.2`, `libpthread.so.0`) under the job's working dir as a dlopen/LD_LIBRARY_PATH hijack target | **Feature 25.** `isDangerousLibraryBasename` rejects LocalPath basenames matching the loader itself (`ld-linux*`, `ld-musl*`, `ld.so*`) or the handful of libraries the dynamic linker unconditionally loads (`libc.so*`, `libpthread.so*`, `libdl.so*`, `libm.so*`, `librt.so*`, `libnss_*`, `libresolv.so*`, `libcrypt.so*`). Narrow on purpose — legitimate CUDA/Torch libs (`libcudart.so.*`, `libtorch.so`) still pass. |
| Admin operator needs `LD_LIBRARY_PATH` on a specific GPU pool for CUDA dlopen | **Feature 25 per-node overrides.** `HELION_ENV_DENYLIST_EXCEPTIONS=role=gpu:LD_LIBRARY_PATH` on the coordinator (parsed at startup; malformed input fails to start). A job's env var is allowed iff its NodeSelector carries the exact key=value pair AND the env key is in the rule's list. Each override use emits an `env_denylist_override` audit event so use of the escape hatch is always visible. |
| Dashboard user / CI read-only token extracts `HF_TOKEN` / `AWS_SECRET_ACCESS_KEY` by calling `GET /jobs/{id}` | **Feature 26.** Submitter flags keys via `secret_keys`; server replaces matching values with `[REDACTED]` on every response path (single/list/dry-run/workflow). Plaintext exists on-disk in the Job record (runtime needs it) but is unreachable via any non-admin endpoint. See §9.3. |
| Admin operator needs to recover a declared secret value (forgot, debugging, credential rotation) | **Feature 26 reveal-secret endpoint.** `POST /admin/jobs/{id}/reveal-secret` — admin-only, rate-limited (1/5s, burst 3), mandatory audit `reason`, audit-before-response fail-closed, refuses non-declared keys. See §9.4. |
| Attacker probes "does job X have a secret named Y?" via reveal-secret 404s | Every reject is itself audited as `secret_reveal_reject` with the target job_id + key + reason-for-reject. Enumeration shows up loud in the audit stream. |
| Attacker with filesystem access reads secret plaintext from BadgerDB | **Feature 30 shipped.** Per-job DEKs wrap each secret value in AES-256-GCM; DEKs are themselves wrapped in AES-256-GCM under a coordinator-held root KEK. Every declared secret is encrypted at the persistence boundary; the Badger record never carries plaintext. Rotation is supported via `/admin/secretstore/rotate`. See §9.6. |
| Job prints its own `$HELION_TOKEN` to stdout → captured by logstore → visible via `GET /jobs/{id}/logs` | **Feature 29 shipped.** `logstore.ScrubbingStore` decorator substitutes every declared secret VALUE with `[REDACTED]` before chunks land in BadgerDB; response-path redactor on `GET /jobs/{id}/logs` repeats the substitution as belt-and-braces; the feature-28 analytics mirror (PG sink) scrubs the same bytes before publish so PG cannot become a side-channel. Per-job RBAC now gates log reads too. See §9.5. |
| Admin JWT leaks via clipboard / screenshare / browser extension → remote attacker submits jobs from the internet | **Feature 27 optional.** `HELION_REST_CLIENT_CERT_REQUIRED=on` requires the caller to also present a client certificate verified against the coordinator's CA. Cert-less requests are refused at 401 before auth middleware even runs. Staged rollout via `warn` tier. See §9.6. |
| Attacker forges `X-SSL-Client-Verify: SUCCESS` to bypass mTLS | Coordinator honours those headers ONLY from loopback (127.0.0.1 / ::1). Any non-loopback peer carrying those headers is treated as if no cert was presented. |
| Operator laptop stolen with P12 imported in browser | P12 password at import time + OS keychain user-auth gating. Not iron-clad; mitigation is short TTL (90 days default) and revocation follow-up [feature 31](planned-features/31-cert-revocation-crl-ocsp.md). |
| Compromised browser process uses the installed operator cert | Out of scope for mTLS (a network-boundary control). Tracked as [feature 34 — WebAuthn/FIDO2](planned-features/34-webauthn-fido2.md), which moves the signing key to a hardware device requiring physical touch per auth event. |
| Analytics database compromised; attacker reads JWT subjects (PII) out of a PG dump | **Feature 28 PII mode.** `HELION_ANALYTICS_PII_MODE=hash_actor` writes `sha256(salt \|\| actor)` into every analytics `actor` column instead of the raw subject. Dashboards still group by hash (same actor = same hash); ops learns trends without identity. Audit log remains authoritative for accountability (raw subjects, forever-retained, Badger-backed). |
| Analytics database fills indefinitely; attacker exploits to DoS / run out of disk | **Feature 28 retention cron — opt-in.** `HELION_ANALYTICS_RETENTION_DAYS` defaults to 0 (disabled) because PG is the intended long-term store. Operators who want retention set a positive value; the cron prunes every feature-28 table EXCEPT `job_log_entries` (PG is the authoritative log home, see §9.7). Audit log in BadgerDB is untouched regardless. |
| Secret env values sneak into analytics tables | **Feature 28 defence in depth.** `submission_history` stores only `resource_id` (a ULID), never the submit body. The command/args/env live in the audit log under the matching `job_id`. Feature 26's redaction applies to the audit store too. An analytics dump is never a secret-exposure event. |
| Per-job logs outgrow Badger's 7 d TTL and old stdout is lost | **Feature 28 PG-backed log store (authoritative long-term).** Every chunk dual-writes: Badger for fast live-tail, PostgreSQL `job_log_entries` for permanent retention. Reconciler deletes Badger's copy once PG confirms the chunk. PG `job_log_entries` is explicitly EXCLUDED from the retention cron — a forensic reviewer 6 months later still gets the full stdout via `/api/analytics/job-logs?job_id=…`. |
| Reconciler deletes a Badger log chunk that isn't actually in PG (data loss) | **Confirm-before-delete.** The reconciler runs `SELECT 1 FROM job_log_entries WHERE (job_id, seq) = …` and deletes Badger's copy ONLY on confirmed hits. A PG outage or a sink-dropped event leaves the Badger entry in place; the next tick retries. `TestReconciler_PGQueryError_NoDeletes` + `TestReconciler_ConfirmedAreDeletedUnconfirmedKept` gate against this regression. |
| Race between a just-landed chunk and the Badger reconciler ("sink hasn't flushed yet, but reconciler thinks PG is authoritative") | **MinAge safety margin.** Reconciler skips any entry whose Timestamp is newer than `HELION_LOGSTORE_RECONCILE_MIN_AGE_MIN` (default 5 min) even if PG reports it as confirmed. Long enough that the sink's ~500 ms batch is comfortably in the past; short enough that fresh chunks don't pile up in Badger indefinitely. |

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

**Surfaces covered.** Hybrid-KEM applies to BOTH coordinator-facing listeners:

1. **coordinator ↔ node (gRPC, :9090)** — wired since the initial post-quantum pass
   via `ServerCredentials()` + `ClientCredentials()` on `auth.Bundle`.
2. **coordinator ↔ dashboard / in-workflow scripts (REST + WebSocket, :8080)** — added
   in feature 23. `api.Server.ServeTLS(addr, cfg)` expects a `*tls.Config` built via
   `bundle.CA.EnhancedTLSConfig(certPEM, keyPEM)`, the exact same path the gRPC
   listener uses. The coordinator main wires this by default; opt-out requires
   explicit `HELION_REST_TLS=off` and emits a WARN log on every startup.

**Strict-mode enforcement.** Set `HELION_PQC_REQUIRED=1` on the coordinator to fail
startup if `ApplyHybridKEM` silently produced a config without the Kyber curve
(e.g. on a Go runtime with `GODEBUG=tlskyber=0`). Without this flag the coordinator
falls back to classical-only curves when the runtime does not support Kyber; with the
flag it refuses to start, guaranteeing the production posture never silently downgrades.

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

### Registry API rate limiting

The `/api/datasets` and `/api/models` endpoints share the same per-subject
limiter shape. Registry writes are cheap BadgerDB single-key puts, but an
authenticated user could otherwise flood the audit log or chew through disk
by registering millions of entries.

| Property | Value |
|---|---|
| Rate | 2 requests/sec per JWT subject |
| Burst | 30 |
| HTTP status on limit hit | `429 Too Many Requests` |
| Keyed on | JWT `sub` claim (subject) |

Every mutation (`POST` / `DELETE`) also lands in the audit log with the
subject as actor, the dataset/model (name, version), and the URI for
register events. See `internal/api/handlers_registry.go`.

**Registry authorization model (current):** any authenticated user can
register, read, or delete any entry. There is no per-entry owner check.
This matches the small-team deployment model of the rest of the API and
is explicitly called out in the registry handler doc comment. Tightening
to admin-only delete or owner-only delete is a deliberate future step,
tracked in `docs/planned-features/10-minimal-ml-pipeline.md`.

**Input validation.** The registry rejects malformed input before any
disk write:

- `name`, `version` — k8s-shaped (`[a-z0-9._-]`, bounded length).
- `uri` — scheme allowlist (`file://`, `s3://`). `http(s)://` is
  rejected so a caller can't wire the registry into an SSRF chain
  on downstream consumers.
- `metrics` — `NaN` / `+Inf` / `-Inf` are rejected. JSON can smuggle
  these via `1e400` which parses as `+Inf`; the validator catches it.
- `tags` — k8s label bounds (32 entries, 63-char keys, 253-char
  values, printable-ASCII, no `NUL`).
- `source_dataset` — partial pointers (name without version or vice
  versa) are rejected. A lineage pointer is either complete or absent.

The URI existence is *not* checked at register time. A registered
dataset/model URI may dangle if the underlying artifact is deleted
out-of-band. This is intentional: the registry is metadata-only, and
coupling the two would require artifact-store credentials on the
coordinator. Deletion of a dataset also does not cascade to models
that reference it — lineage becomes soft. Both are explicit deferrals
in the feature 10 spec.

**Load test:**

```bash
for i in {1..1000}; do helion-run echo "job $i" & done
wait
# First ~10 jobs succeed (burst); sustained rate limited to 10 jobs/s thereafter.
# Check audit log for rate_limit_hit events:
curl -H "Authorization: Bearer $ROOT_TOKEN" \
  "https://coordinator:8443/audit?type=rate_limit_hit"
```

### Inference service surface (feature 17)

Long-running inference jobs (`SubmitRequest.service`) bypass the
standard job-timeout enforcement — by design, a service is supposed
to keep running — so they widen the attack surface in three small
ways. The controls below keep each one within the existing doctrine.

| New attack surface | Control |
|--------------------|---------|
| Service bypasses timeout enforcement | A service job runs only for the lifetime of its Dispatch RPC. The coordinator's `Cancel` RPC is the supported stop path; it delegates to `Registry`-tracked heartbeat stream cancellation plus the node-side runtime's `running[jobID]` cancel func. A compromised node cannot make a non-service job run forever because the `IsService` flag is forwarded from the coordinator-signed `Job.Service`, not from the node. |
| Service exposes a TCP port on the node | The prober only hits `127.0.0.1:<port><health_path>`. The coordinator records `(node_address, port)` but does **not** act as a proxy — `GET /api/services/{id}` returns the upstream URL, authenticated clients hit it directly. Nodes that run behind Nginx/ingress must publish the port themselves; Helion does not punch holes in network policy. |
| Node could forge a `service.ready` for another node's job | `grpcserver.ReportServiceEvent` compares `ServiceEvent.NodeId` against the pinned `Job.NodeID` from the dispatcher record and returns `PermissionDenied` on mismatch. Same doctrine as `ReportResult`; same `LogSecurityViolation` path. |

**Submit-time validation** (`validateServiceSpec` in `internal/api/handlers_jobs.go`):

- `service.port` must be in `[1024, 65535]`. Privileged ports are
  rejected because the production DaemonSet runs as non-root and
  would fail to bind anyway — catching it at submit surfaces a crisp
  400 rather than a spawn-time crash.
- `service.health_path` must start with `/`, be non-empty, contain
  no whitespace or NUL, and fit in 256 bytes.
- `service.health_initial_ms` caps at 30 min so a misconfigured job
  cannot suppress failure detection indefinitely.

**Rate limiting.** The existing `POST /jobs` submit limiter covers
service submission — services are ordinary jobs that happen to carry
a `service` block. `GET /api/services/{id}` reads from an in-memory
map; no additional limiter is required, and the standard JWT middle-
ware authenticates the lookup.

**Audit event taxonomy.** Two new event types land with this slice:

| Event type | Emitter | Actor | Target | Details |
|---|---|---|---|---|
| `service.ready` | gRPC `ReportServiceEvent` | `node:<node_id>` | `job:<job_id>` | `port`, `health_path`, `consecutive_failures` (= 0 on transition into ready) |
| `service.unhealthy` | gRPC `ReportServiceEvent` | `node:<node_id>` | `job:<job_id>` | same shape, `consecutive_failures` = probe-miss streak |

Both are edge-triggered (one row per state flip, not per probe tick)
so a healthy service does not bloat the audit log.

**Runtime coverage.** The Go runtime (`internal/runtime/go_runtime.go`)
implements the new `RunRequest.IsService` flag. The Rust runtime
(`runtime-rust/`) does **not** yet — service jobs dispatched to a
Rust-backed node will run with the runtime's default timeout and no
probe loop. Tracked in the deferred backlog; see
[`planned-features/deferred/`](planned-features/deferred/). The Go
backend is the default; operators running a Rust-backed cluster that
need inference services today should stay on the Go runtime until
the Rust parity slice lands.

### ML dashboard module surface (feature 18)

Feature 18 added a lazy-loaded Angular module at `/ml/datasets`,
`/ml/models`, and `/ml/services` plus the supporting REST endpoint
`GET /api/services` (list).

| New attack surface | Control |
|--------------------|---------|
| New client routes | All three views are inside the existing shell, behind the same `authGuard` and JWT interceptor as every other authenticated route. The shell's auth guard rejects unauthenticated nav before any ML-API call leaves the browser. |
| `GET /api/services` exposes node addresses | Already exposed by the per-job lookup (`GET /api/services/{job_id}`); the list variant is the same data over the same auth middleware, just batched. Same-origin CORS via Nginx in production. |
| Register dataset modal accepts free-form URI | Server-side validator (`registry.ValidateURI`) is the authoritative check — rejects anything outside `file://` / `s3://`. The dashboard form's hint is informational; the coordinator's 400 is what the user sees on a bad URI. |
| Delete buttons on Datasets and Models | Confirm prompt in the UI is a UX guard, not a security one. The flat "any authenticated user can delete any entry" model from feature 16 still applies; tightening to admin-only / owner-only deletion is on the deferred backlog (`deferred/17-registry-lineage-enforcement.md` covers the same authz question). |

The Services view polls `GET /api/services` every 5 s — the same
cadence as the node-side prober, so the dashboard is never more
than one tick stale. No additional rate limiting is needed because
the lookup is a memory read on the coordinator side.

**New backend events surfaced to the dashboard:**

| Event | When | Surfaced where |
|-------|------|----------------|
| `ml.resolve_failed` | Workflow job's artifact resolver fails (upstream missing, output missing, etc.) | Future Pipelines DAG view (deferred); already on the audit log + event bus today |
| `job.unschedulable` (extended) | Same as before, plus a new `reason` field: `no_healthy_node` / `no_matching_label` / `all_matching_unhealthy` | Same — event-feed view shows the reason verbatim today; the Pipelines view will colour-code it once it lands |

---

## 5a. Principal model (feature 35)

Every authenticated request, every gRPC call from a registered
node, and every coordinator-internal loop carries a typed
`*principal.Principal` in its Go context. The Principal is the
single identity primitive later authorization slices (features
36–38) evaluate against.

### Kinds

| Kind | ID format | How it gets stamped |
|---|---|---|
| `user` | `user:<jwt_subject>` | `authMiddleware` after JWT validation succeeds for a non-node, non-job role. |
| `operator` | `operator:<cert_cn>` | `clientCertMiddleware` after verifying a client certificate (feature 27). Wins over JWT resolution — the cert is the strictly stronger identity. |
| `node` | `node:<node_id>` | gRPC handlers after the node ID is known (`Register`, `Heartbeat` on first message with a NodeId, `ReportResult`, `ReportServiceEvent`). The mTLS handshake already verified the node's bootstrap certificate to the coordinator's CA. |
| `service` | `service:<name>` | Coordinator-internal loops (`dispatcher`, `workflow_runner`, `retry_loop`, `log_ingester`, `retention`, `log_reconciler`, `coordinator`). Package-level vars in `internal/principal/`; audit helpers (`LogJobStateTransition`, `LogCoordinatorStart/Stop`) default-stamp `service:coordinator` when no more specific principal is in context. |
| `job` | `job:<jwt_subject>` | JWT with `role=job` — workflow-scoped tokens minted by `submit.py` (feature 19 + related). |
| `anonymous` | `anonymous` | Dev-mode when `Server.DisableAuth` is set. Feature 37 will deny non-trivial actions against anonymous principals. |

### Safety properties

- **IDs are prefix-qualified.** A node registered as `alice`
  produces `node:alice`; a user with the same JWT subject
  produces `user:alice`. Collisions across kinds are
  impossible.
- **Cert wins over JWT.** When `HELION_REST_CLIENT_CERT_REQUIRED`
  is active and a verified client cert is present,
  `authMiddleware` does NOT overwrite the operator principal
  with a user principal derived from the accompanying JWT. The
  cert CN stays the primary ID; the JWT role + subject flow
  through audit metadata but not identity.
- **Node never admin.** `Principal.IsAdmin()` returns `false`
  for `KindNode` regardless of the `Role` field's value — a
  guard against a compromised node forging a node-JWT with
  `role=admin`. Same for `KindService` and `KindAnonymous`.
- **`FromContext` never returns nil.** A context without a
  Principal reads back as `Anonymous()`; handlers never need a
  nil check.
- **Audit events carry both forms.** `Event.Actor` stays the
  legacy bare string (user ID, "system", "unknown") for
  back-compat with existing consumers; `Event.Principal` +
  `Event.PrincipalKind` carry the new typed identity.
  Dashboards filter on the typed fields; pre-feature-35 tooling
  keeps reading Actor.

### What feature 35 does NOT do

- **It is not authorization.** The Principal names *who* is
  acting; feature 37's policy engine decides *what* they may
  do. Feature 35 stamps the identity; every existing RBAC check
  (admin-only middleware, `claims.Subject == job.SubmittedBy`)
  stays in place unchanged.
- **It does not re-verify auth material.** Feature 35 reads
  what the auth surface already trusts (JWT claims, cert CN,
  node ID). A compromised JWT still produces a well-formed
  Principal — the blast radius is identical to "the attacker
  has the JWT".
- **It does not add persistence.** Principals are derived from
  request-scoped auth material; they are not stored. Groups
  (feature 38) will add a lookup to enrich `Principal.Groups`.

See [`internal/principal/principal.go`](../internal/principal/principal.go)
for the type definitions and
[`docs/planned-features/implemented/35-principal-model.md`](planned-features/implemented/35-principal-model.md)
for the slice reconciliation.

---

## 5b. Resource ownership (feature 36)

Every persisted stateful type carries a single authoritative
owner field — `OwnerPrincipal`, formatted as the
feature-35 principal ID that created it (`user:alice`,
`operator:alice@ops`, `service:workflow_runner`, or the
`legacy:` sentinel for pre-feature-36 records). Feature 37's
policy engine will compare this field against the caller's
Principal; feature 38 layers sharing on top.

### Types with an owner

| Type | Where it's stamped |
|---|---|
| `cpb.Job` | `handleSubmitJob` stamps `principal.FromContext(ctx).ID` at create time. Workflow-materialised jobs inherit `Workflow.OwnerPrincipal` via `WorkflowStore.Start`. |
| `cpb.Workflow` | `handleSubmitWorkflow` stamps `principal.FromContext(ctx).ID`. |
| `registry.Dataset` / `registry.Model` | `handleRegisterDataset` / `handleRegisterModel` stamp `principal.FromContext(ctx).ID`. |
| `cpb.ServiceEndpoint` | `gRPC ReportServiceEvent` reads the owning `cpb.Job`'s `OwnerPrincipal` and passes it to `services.Upsert` on first `ready` event, so the in-memory endpoint map carries an owner. |

### Safety properties

- **Immutable after creation.** Every state transition, retry,
  and cancel path preserves `OwnerPrincipal`. `service:retry_loop`
  re-driving a failed user job does NOT transfer ownership to
  the retry loop — the loop is the actor in audit, but the
  resource owner stays the original submitter. Guarded by
  `TestOwnerPrincipal_JobSubmitPersistsAndSurvivesTransitions`
  + the workflow / cancel counterparts.
- **Legacy fail-closed.** Records persisted before feature 36
  shipped backfill on load: `SubmittedBy=<sub>` → `user:<sub>`;
  missing both legacy proxies → `principal.LegacyOwnerID`
  (`legacy:`). Feature 37 will treat `legacy:`-owned resources
  as admin-only — the same fail-closed behaviour the
  pre-feature-36 AUDIT L1 check produced for empty SubmittedBy.
- **Audit distinguishes actor from resource owner.** Create-path
  audit events (`job_submit`, `workflow_dry_run`,
  `dataset.registered`, `model.registered`, etc.) now include
  a `resource_owner` detail alongside `actor`. Reviewers can
  tell "who did it" (actor) from "who owns the target"
  (resource_owner); identical for user-driven creates,
  divergent when a service principal acts on a user-owned
  resource.
- **Back-compat aliases.** `SubmittedBy` on `cpb.Job` and
  `CreatedBy` on registry types stay on the wire for one
  release. External tooling that reads the legacy fields keeps
  working; the typed `owner_principal` is the authoritative
  value for authz.

### What feature 36 does NOT do

- **It is not authorization.** It provides the field feature 37's
  policy engine will compare against the Principal. Until
  feature 37 lands, existing RBAC checks (`claims.Subject ==
  job.SubmittedBy`) stay unchanged.
- **No ownership transfer.** A `/chown`-style endpoint is
  deferred; feature 38's share mechanism covers real-world
  delegation without breaking the immutability invariant.
- **No multi-owner.** Revisited if a team-scoped use case
  appears.

See [`docs/planned-features/implemented/36-resource-ownership.md`](planned-features/implemented/36-resource-ownership.md)
for the slice reconciliation and test inventory.

---

## 5c. Authorization policy (feature 37)

Every authz decision in the coordinator funnels through one
function: `authz.Allow(principal, action, resource)`. The
evaluator is pure, table-driven, and fails closed on every
unexpected input. Handlers that mutate or read a resource load
it from the store, construct an `*authz.Resource`, and call
`Allow` before serving. Denials produce a 403 response carrying
a stable machine-readable `code` and emit an `authz_deny`
audit event.

### Actions

| Action | Endpoint examples |
|---|---|
| `read` | GET /jobs/{id}, /workflows/{id}, /api/datasets/.../{name}/{version} |
| `list` | GET /jobs, /workflows, /api/datasets, /api/models, /api/services |
| `write` | POST /jobs, /workflows, /api/datasets, /api/models |
| `cancel` | POST /jobs/{id}/cancel, DELETE /workflows/{id} |
| `delete` | DELETE /api/datasets/..., DELETE /api/models/... |
| `reveal` | POST /admin/jobs/{id}/reveal-secret |
| `admin` | POST /admin/nodes/{id}/revoke, /admin/tokens, /admin/operator-certs |

### Rule precedence

1. `nil` Principal → deny (`nil_principal`).
2. `Kind=user` or `Kind=operator` with `Role=admin` → allow
   every action (break-glass).
3. `Kind=node` → deny on every REST action. A compromised
   node's mTLS-derived JWT cannot stand up fake jobs or read
   user-owned workflows via REST. Nodes still act on the
   internal gRPC surface (`Register`, `Heartbeat`,
   `ReportResult`, `StreamLogs`, `ReportServiceEvent`), which
   is governed by separate per-kind allow-lists.
4. `Kind=service` → narrow per-service allow-list in
   `internal/authz/rules.go`. `service:retry_loop` can cancel
   jobs it's retrying; `service:dispatcher` can read/cancel
   jobs it's dispatching. The table is a compile-time artifact
   — a new service that needs an action goes through a code
   review.
5. `Kind=job` → workflow-scoped tokens may only read jobs
   belonging to the same workflow (the token's subject IS the
   workflow ID). No write/cancel/delete.
6. `Kind=user` or `Kind=operator` (non-admin) → allow iff
   `p.ID == res.OwnerPrincipal` (owner check).
7. `Kind=anonymous` → deny everywhere.
8. Unknown kind → deny (`unknown_kind`).

Resources with `OwnerPrincipal == "legacy:"` (the feature-36
backfill sentinel for records without a recoverable owner)
are admin-only. Resources of `Kind=system` require admin.

### Denial codes

A 403 body carries both the legacy shape and the new typed
deny code:

```json
{"error": "forbidden", "code": "not_owner"}
```

Codes: `nil_principal`, `nil_resource`, `anonymous_denied`,
`not_owner`, `legacy_owner_admin_only`, `node_not_allowed`,
`service_not_allowed`, `job_scope_mismatch`, `admin_required`,
`system_non_admin_action`, `unknown_kind`.

### Audit emission

Every deny emits an `EventAuthzDeny` audit event with the
deny code, attempted action, resource kind + id + owner,
requesting principal, and request path. A sudden drop in
`authz_deny` volume after a deploy is an alert — the policy
engine may have silently widened.

Distinct from `EventAuthFailure` (feature 35), which covers
authentication failures (401 — bad/missing JWT). Feature 37
covers authorisation denials (403 — valid identity but
policy refused).

### List-endpoint filtering

`list` endpoints (jobs, workflows, datasets, models,
services) fetch the full matching set and filter per-row
through `authz.Allow(ActionRead)` before paginating. A
non-admin caller sees only their own resources; admin sees
everything. Per-row denials do NOT audit — the filter is
expected behaviour, not a security event.

A scope-push-down (store-level owner filter) is deferred
until deployments hit the filter-in-memory cliff (>10k active
resources).

### DisableAuth + dev mode

`Server.DisableAuth()` stamps a synthetic `dev-admin`
Principal on every request (a `KindUser` with `Role=admin`).
This keeps the authz path identical between dev and prod —
no bypass branches inside the evaluator or middleware — and
produces an unambiguous audit signal (`principal ==
user:dev-admin-disableauth`) if DisableAuth ever leaks into
a prod binary.

See [`docs/planned-features/implemented/37-authorization-policy.md`](planned-features/implemented/37-authorization-policy.md)
for the slice reconciliation and test inventory.

---

## 5d. Groups and resource shares (feature 38)

Feature 37 gives admin-or-owner. Feature 38 adds two orthogonal
delegation primitives that widen access without concentrating
blast radius on the admin role:

  1. **Groups** — named, flat collections of Principal IDs. A
     group `ml-team` with members `user:alice`, `user:bob`,
     `operator:carol@ops` is a single identifier that can
     appear as a grantee on a resource share.
  2. **Shares** — per-resource grants attached to a specific
     resource (job, workflow, dataset, model) naming a grantee
     (direct user / operator / service / job principal, OR a
     `group:<name>` reference) and an enumerated Action set
     the grantee may perform.

### Safety properties

- **Non-transitive.** A grantee with `ActionRead` on a resource
  cannot re-share onward. Share-mutation endpoints require the
  caller to be the resource owner OR have `ActionAdmin`.
  Transitive delegation is explicitly deferred per the feature
  spec.
- **Typed namespace.** `user:`, `operator:`, `group:`, etc. are
  prefix-qualified; a principal ID cannot collide with a group
  name because their prefixes differ.
- **Flat groups.** v1 does NOT support groups-of-groups —
  recursion-risk without a concrete use case.
- **Action-scoped.** A share granting only `[read]` does NOT
  grant cancel / delete / reveal. The evaluator's rule 6b
  checks `containsAction(share.Actions, action)` before
  allowing.
- **Legacy-sentinel still wins.** Resources with owner
  `legacy:` (feature 36 backfill) stay admin-only regardless of
  shares — the legacy fail-closed check runs BEFORE rule 6b.
- **Admin cannot share `ActionAdmin`.** `ValidateShare` rejects
  any share whose Actions include `ActionAdmin`. Admin is a
  kind-level role, not a per-resource capability.
- **Per-resource cap.** `MaxSharesPerResource = 32`. Beyond
  that the endpoint returns 400 with a hint to use a group
  grantee instead. Keeps the per-request Allow scan cheap and
  nudges operators toward groups for large teams.
- **Name validation.** Group names are `[a-zA-Z0-9._-]{1,64}`
  and must not start with `.` (defence against path-traversal
  key shapes).
- **Admin-only management.** Group create/delete/member-edit
  endpoints require `ActionAdmin`. Share-mutation endpoints
  require owner-or-admin.

### Management API

```
POST   /admin/groups                          {name}
GET    /admin/groups                          -> [Group...]
GET    /admin/groups/{name}                   -> Group
DELETE /admin/groups/{name}
POST   /admin/groups/{name}/members           {principal_id}
DELETE /admin/groups/{name}/members/{id...}

POST   /admin/resources/{kind}/share?id=<id>
       body: {grantee, actions}
GET    /admin/resources/{kind}/shares?id=<id>
DELETE /admin/resources/{kind}/share?id=<id>&grantee=<id>
```

Supported resource kinds: `job`, `workflow`, `dataset`, `model`.
Registry resource ids ride in the `id` query parameter as
`name/version` (datasets + models).

Share mutations are idempotent:
  - POST same (grantee, actions) twice → single record.
  - POST same grantee with different actions → actions replaced
    (last-writer-wins).
  - DELETE an absent grantee → 204.

### Audit

New event types — filtering the analytics panel on these lets
a reviewer answer "who gained access to this resource in the
last 24h?" without scanning raw audit keys:

  - `group_created`, `group_deleted`
  - `group_member_added`, `group_member_removed`
  - `resource_shared`, `resource_share_revoked`

Every share mutation emits an event carrying the resource
kind + id, grantee, actions (for create), and the granting
principal. Share-escalation attempts (non-owner trying to add
a share) emit `authz_deny` with the same rich context.

### Principal resolution

`authMiddleware` populates `Principal.Groups` at every
authenticated request via a single `GroupsFor(p.ID)` lookup on
the configured groups store (O(1) via the reverse-index prefix
scan). Store failures log at Warn and leave `Groups` nil — a
lookup outage does not block authentication; the cost is that
`group:<name>` shares become inert for that request until the
store recovers.

Deployments that don't configure a groups store (dev binaries)
skip the lookup entirely. Direct `user:<id>` shares still work;
`group:<name>` shares are inert (no groups exist to match).

See [`docs/planned-features/implemented/38-groups-and-shares.md`](planned-features/implemented/38-groups-and-shares.md)
for the slice reconciliation and test inventory.

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

### 9.1 Submission tab (feature 22)

The `/submit` route group is the dashboard's single place to
start a run — single jobs, workflows, ML-templated workflows,
and form-driven DAGs all POST through the existing submit
endpoints (`POST /jobs`, `POST /workflows`). The UI is not a
trusted client; every control has a server-side counterpart:

| Client control | Server-side counterpart |
|---|---|
| Two-click Validate → Preview → Submit | Per-subject rate limit (10 rps default) bounds accidental-click floods |
| Client-side env-key denylist (`LD_*`, `DYLD_*`, `GCONV_PATH`, …) | **Deferred to feature 25.** UX-only today; a malicious client can POST raw JSON past the denylist. Load-bearing rejection lands server-side. |
| Secret env toggle (`type="password"` on the value input) | **Feature 26 shipped.** Form now emits a `secret_keys` list alongside the `env` map; server redacts those values to `[REDACTED]` on every GET path. Read-back requires `POST /admin/jobs/{id}/reveal-secret` (admin-only, audited, rate-limited). See §9.3 + §9.4. |
| Validate button runs shape validator in-browser | **Feature 24 shipped.** The dashboard Validate button can now call `POST /jobs?dry_run=true` / `POST /workflows?dry_run=true` / `POST /api/datasets?dry_run=true` / `POST /api/models?dry_run=true` — the server validator is the authority for accept/reject. Dry-run returns `200` with `"dry_run": true` in the body and never persists, dispatches, or publishes a bus event. Audit emits a distinct event type (`job_dry_run`, `workflow_dry_run`, `dataset.dry_run`, `model.dry_run`) so reviewers can filter probes from real submissions. A typo (`?dry_run=maybe`) returns `400` rather than silently falling through to the real path. |
| YAML/JSON editor uses `JSON.parse` (no YAML) | `JSON.parse` has no code-execution path. YAML arrives with the feature 22 Monaco upgrade and MUST use `js-yaml` with `JSON_SCHEMA` (no custom tags, no aliases). |

New rule for future submit paths: **"No submit path may bypass
the seven-layer stack documented in §§4-8. The submit tab is a
convenience UI; it does not relax any server-side check."**

### 9.2 Dangerous-env denylist (feature 25)

Every submit path (`POST /jobs`, `POST /workflows` per child job,
under `?dry_run=true` too) rejects env vars whose keys match the
dynamic-loader / glibc module-loading denylist:

- **Prefix matches:** `LD_*` (glibc dynamic loader — `LD_PRELOAD`,
  `LD_LIBRARY_PATH`, `LD_AUDIT`, …), `DYLD_*` (macOS loader —
  `DYLD_INSERT_LIBRARIES`, …).
- **Exact matches:** `GCONV_PATH`, `GIO_EXTRA_MODULES`, `HOSTALIASES`,
  `NLSPATH`, `RES_OPTIONS`.

Matched verbatim because the Linux loader itself only honours
uppercase — a lowercase `ld_preload` is inert and doesn't need
blocking. Admin role is subject to the same denylist: there's no
legitimate admin workflow that needs these keys through the submit
path.

The Go runtime previously passed submit env directly to
`exec.Command.Env`, meaning anyone with submit permission could
hijack every exec on the node with a single `LD_PRELOAD` value. The
Rust runtime's `env_clear()` dodges this by accident but the denylist
is load-bearing on the Go path and defence-in-depth on both.

In addition to env keys, `validateArtifactBindingsCtx` rejects two
artifact-staging patterns that could weaponise subprocess loading
even after the env denylist:

- **`file://` URIs rooted at system-library or secret paths** —
  `/lib`, `/lib64`, `/usr/lib`, `/usr/lib64`, `/usr/local/lib`,
  `/proc`, `/sys`, `/dev`, `/etc`, `/boot`, `/root`,
  `/var/run/secrets`, `/run/secrets`, `/run/credentials`. Path is
  `path.Clean`-normalised first so `//` tricks don't slip through.
- **LocalPath basenames matching loader-critical shared libraries** —
  `libc.so*`, `ld-linux*`, `ld-musl*`, `libpthread.so*`, `libdl.so*`,
  `libm.so*`, `librt.so*`, `libnss_*`, `libresolv.so*`, `libcrypt.so*`.
  Narrow on purpose: legitimate ML libs (`libcudart.so.11.0`,
  `libtorch.so`, `libcuda.so.1`) still pass.

Every reject emits an `env_denylist_reject` audit event carrying the
blocked key + target job/workflow id so a reviewer can spot probes in
the audit log without regex-matching error strings.

#### Per-node overrides

For legitimate needs (e.g. a dedicated GPU node pool needing
`LD_LIBRARY_PATH` for CUDA dlopen), the coordinator accepts a
whitelist via the `HELION_ENV_DENYLIST_EXCEPTIONS` environment
variable at startup:

```
HELION_ENV_DENYLIST_EXCEPTIONS=role=gpu:LD_LIBRARY_PATH
HELION_ENV_DENYLIST_EXCEPTIONS=role=gpu:LD_LIBRARY_PATH;pool=build:LD_LIBRARY_PATH,GCONV_PATH
```

Format: `<selector_key>=<selector_value>:<env_key>[,<env_key>]*[;<next_rule>]`.

A job's env var is allowed iff its `NodeSelector` contains the exact
`selector_key=selector_value` pair AND the env key appears in the
rule's list. Safety properties:

- **Only set via coordinator env at startup.** Nodes cannot declare
  their own exceptions (a compromised node can't unlock `LD_PRELOAD`
  for jobs it's about to run).
- **Malformed input fails to start.** A typo in the env var yields
  `os.Exit(1)` at boot rather than silently running with the denylist
  disabled or half-parsed rules.
- **Exception keys must themselves be on the denylist.** Adding
  `PYTHONPATH` to an exception rule is a parse error — rules that
  would have no effect are not silently accepted (they'd be a
  confusing artefact that looked like a real exception).
- **Overrides are loudly audited.** Every env var let through by an
  exception emits its own `env_denylist_override` audit event with
  the job id and env key, so the escape hatch's usage is traceable.
- **Requires explicit selector match.** A job with no NodeSelector,
  or with a NodeSelector value that doesn't match the rule, remains
  denied. No "default allow" behaviour.
- **Dry-run doesn't bypass.** `?dry_run=true` applies the same check
  with the same audit events (with a `"dry_run": true` detail field
  so reviewers can distinguish).

### 9.3 Secret env vars (feature 26)

`SubmitRequest` carries a sibling `secret_keys: string[]` list that
names env keys whose values must be redacted on every response path.
The coordinator keeps the plaintext value in `Env` (the runtime
needs it to dispatch to the node) but wraps every response build
through `redactSecretEnv(env, secretKeys)`. Applied on:

- `GET /jobs/{id}` (single job)
- `GET /jobs` (paginated list)
- `POST /jobs` + `POST /workflows` on the 201 response body
- `?dry_run=true` responses on all submit paths
- `GET /workflows/{id}` child-job env

The replacement string is the literal `[REDACTED]`. The redactor
never mutates the caller's map — it returns a fresh copy, so
mutating a response map never pollutes the stored record.

Validation rules enforced at submit:

- Every key in `secret_keys` MUST appear in `env`. A flag on a
  non-existent key is rejected with 400 — either a typo or a probe
  for which names the server silently accepts.
- No duplicates; no empty strings; list capped at 32 entries (the
  overall env map caps at 128, but a pathological "flag everything"
  submit would render GET useless).

Audit invariants:

- `job_submit` events carry a `secret_keys` detail field listing
  the KEY NAMES. Values are NEVER in audit details.
- The submit handler emits `env_denylist_reject` + `env_denylist_
  override` (feature 25) with the same value-free policy.

### 9.4 Reveal-secret endpoint (feature 26)

`POST /admin/jobs/{id}/reveal-secret` is the single audited path
by which an operator can read back a declared secret value.
Shipped from feature 26's original "deferred" list on user request.

Request body:

```json
{ "key": "HF_TOKEN", "reason": "on-call debug of HF model load" }
```

Safety properties:

- **Admin role only.** `adminMiddleware` runs first; `node`-role
  tokens get 403 before the handler sees the request.
- **Rate-limited per subject** at 1 reveal / 5 s (burst 3). Tighter
  than the `/admin/tokens` limit because every successful call
  exposes a plaintext value. A compromised admin token cannot
  bulk-dump the coordinator's secret inventory before the audit
  stream triggers detection.
- **Reason field is mandatory**, non-empty, trimmed, ≤ 512 bytes,
  NUL-free. The reason lands verbatim in the audit detail so
  post-incident review can tell intentional debugging apart from
  enumeration.
- **Key must be declared secret on THAT job.** Reading a
  non-secret env value via this endpoint is refused with 404 —
  the endpoint is not a generic env reader (operators can use
  `GET /jobs/{id}` for non-secret values). Removes the uplift an
  attacker would gain from using this endpoint as an env dump.
- **Audit event written BEFORE response.** `secret_revealed` is
  persisted before the plaintext enters the response body. A
  downed audit sink yields 500 and no leak — we fail closed on
  the accountability story rather than the confidentiality story.
- **Every reject is audited too.** Unknown-job, not-declared-secret,
  malformed-body, and missing-reason paths each emit
  `secret_reveal_reject` with actor + reason-for-reject. This
  means a probe sweep ("does job X have a secret named HF_TOKEN?")
  shows up loud and clear in the audit stream, not silent 404s.
- **Response body carries an audit notice.** The `audit_notice`
  string in `RevealSecretResponse` reminds the operator on-screen
  that the reveal was logged; the dashboard renders it alongside
  the value. Belt-and-braces against "I didn't know it was logged"
  post-hoc claims.
- **Storage no longer holds plaintext** (feature 30).
  Secret values are envelope-encrypted on the way to Badger
  and decrypted on load — a disk snapshot / backup yields
  ciphertext that is useless without the coordinator's
  root KEK. See §9.6 for the crypto + operator playbook.
  A compromised coordinator process (memory dump while the
  KEK is loaded) remains a key-compromise event.
- **stdout leaks are mitigated by feature 29.** If a job prints
  its own `$HELION_TOKEN` to stdout, the coordinator's log
  store substitutes the plaintext value with `[REDACTED]`
  before persisting. See §9.5.

### 9.5 Log scrubbing on the write path (feature 29)

Feature 26 closes "operator reads env via GET /jobs/{id}". It
does not close "operator reads the job's stdout/stderr and
finds the plaintext there because the job printed it":

```python
print(f"Auth: Bearer {os.environ['HF_TOKEN']}")  # common debug
```

Feature 29 wraps the log store in a substitution decorator
(`logstore.ScrubbingStore`) that replaces every occurrence of
every declared secret VALUE with the literal `[REDACTED]`
before the chunk lands in BadgerDB. A second pass runs at
response-build time on `GET /jobs/{id}/logs` as belt-and-braces
against chunks that landed before the decorator was wired
(rolling deploy edge case).

Safety properties:

- **Same substitution at both sinks.** The Badger append path
  is decorated; the feature-28 analytics mirror (`events.Bus`
  → PG) is scrubbed in the gRPC `StreamLogs` handler before
  publish. PG cannot become a side-channel around the
  decorator — both sinks see the same redacted bytes.
- **Empty-value DoS guard.** A secret declared with an empty
  value would otherwise match every byte position
  (`bytes.ReplaceAll` semantics) and inflate the chunk
  unboundedly. The scrubber skips zero-length secrets.
- **Idempotent.** Running the scrubber twice produces the
  same output, so layering the decorator AND the response-
  path redactor does not double-redact.
- **Fail-open on store lookup.** A secrets-lookup error (job
  terminal + evicted) returns `(nil, false)` — no scrubbing,
  but the chunk still persists. We prefer "not dropping logs"
  over a perfect redact; the response-path pass gives a
  second chance.
- **Per-job RBAC on `GET /jobs/{id}/logs`.** Feature 37's
  authz check now gates log reads the same as job reads.
  Pre-feature-29 this endpoint had no per-job RBAC; any
  authenticated caller could fetch any job's log chunks.

Known limitations:

- **Chunk-boundary miss.** If a secret value lands split
  across two write chunks (rare; node runtimes flush
  line-buffered), neither chunk matches on its own. The
  response-path redactor catches this at GET time if it
  concatenates entries first — current implementation
  scrubs per-chunk, so the boundary case remains. Operators
  worried about this should use structured JSON env delivery
  (the split-point is stable).
- **Malicious transform.** A script that base64-encodes or
  xors its own token before printing is not caught; that
  falls under "operator chose to run a malicious job", which
  the coordinator does not claim to sandbox against.
- **Regex-based detection of undeclared secrets** (e.g. AWS
  access key IDs by shape) is explicitly out of scope — fancy
  regexes false-positive on legitimate output and drag
  operator trust down. Declare the secret at submit time; the
  scrubber takes it from there.

See [`docs/planned-features/implemented/29-stdout-secret-scrubbing.md`](planned-features/implemented/29-stdout-secret-scrubbing.md)
for the slice reconciliation and test inventory.

### 9.6 Encrypted env storage (feature 30)

Feature 26 redacts secret env values on every response path.
Feature 29 scrubs them from log output. Feature 30 closes the
last gap: before feature 30, the `cpb.Job.Env` map still
reached BadgerDB as JSON plaintext, so an attacker with
filesystem / backup access to the coordinator grepped the
Badger store for every HF token, AWS key, or API secret the
coordinator had ever seen.

Feature 30 applies **envelope encryption at the persistence
boundary**:

  plaintext  ── AES-256-GCM(DEK, nonce_v) ─▶ ciphertext
  DEK        ── AES-256-GCM(KEK, nonce_d) ─▶ wrapped_DEK

The DEK ("data encryption key") is a fresh 32-byte value per
encrypt. The KEK ("key encryption key") is a single 32-byte
secret read from `HELION_SECRETSTORE_KEK` at coordinator boot.
Both layers use AES-256-GCM with 12-byte random nonces.

### Safety properties

- **Authenticated encryption.** AES-GCM's 16-byte tag detects
  any bit flip in ciphertext, nonce, wrapped-DEK, or wrapped-
  DEK nonce. A tampered envelope fails Decrypt with no
  plaintext byte output.
- **Fresh DEK per encrypt.** Two encrypts of the same
  plaintext produce distinct ciphertext. Reusing a DEK across
  values is never allowed; reusing a nonce under the same key
  is the one crypto cardinal-sin that GCM cannot recover from.
- **Nonces from crypto/rand.** 12 random bytes per encrypt.
  At realistic per-value volumes (<<2^32 encrypts per key)
  the collision probability is negligible, and the DEK is
  single-use anyway.
- **Persistence-boundary only.** Encryption happens in
  `SaveJob` / `SaveWorkflow`; decryption in `LoadAllJobs` /
  `LoadAllWorkflows`. Every in-memory reader (dispatch,
  reveal-secret, log-scrub, response-redaction) keeps
  working unchanged.
- **Fail-closed on wrong KEK / tampered bytes.** A Job whose
  envelope cannot be decrypted blocks the load path: the
  coordinator refuses to start rather than silently dropping
  records. An operator who suspects KEK compromise MUST
  rotate and rewrap, not rewrite code to silently skip.
- **Legacy records still load.** Pre-feature-30 records
  with plaintext Env and no EncryptedEnv continue to load
  unchanged. The next SaveJob (any state transition) rewrites
  them under envelope encryption.
- **No-keyring fallback.** Deployments that don't configure
  `HELION_SECRETSTORE_KEK` still run — secret values persist
  in plaintext exactly as pre-feature-30. The coordinator
  logs a `WARN` at boot so operators see the gap in their
  deploy logs. Production deployments should always configure
  the KEK.
- **KEK wipe on drop.** `KeyRing.RemoveKEK` zeros the key
  bytes before removing them from the map. Best-effort — Go
  has no guaranteed-non-elided erase — but shortens the
  window a core dump can capture an unloaded version.

### KEK configuration

The KEK is 32 bytes of key material supplied via env var.
Accepted encodings:

  - 64-character hex (lowercase or uppercase)
  - Standard or URL-safe base64 (with or without padding)

Both decode to exactly 32 bytes. Shorter / longer inputs are
rejected at boot.

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

Each Version is 1–16 (supports long rotation windows with up
to 16 concurrent KEKs; realistically a rotation uses 2–3 at a
time). All-zero keys and keys of wrong length are rejected.

### Rotation

To rotate the KEK:

  1. Generate a new KEK, assign version N+1.
  2. Keep the old KEK loaded as
     `HELION_SECRETSTORE_KEK_V<old>` and set the new one as
     `HELION_SECRETSTORE_KEK` with
     `HELION_SECRETSTORE_KEK_VERSION=<N+1>`.
  3. Restart the coordinator. New encrypts use version N+1;
     existing records still decrypt under their recorded
     version.
  4. `curl -X POST /admin/secretstore/rotate` (admin-auth).
     Sweeps every persisted Job + WorkflowJob, rewrapping
     every non-active envelope under version N+1. Idempotent
     — safe to retry if it fails partway.
  5. Once the sweep reports zero scanned records, the old
     KEK is no longer referenced. Drop
     `HELION_SECRETSTORE_KEK_V<old>` from the deploy env and
     restart.

The `secretstore_rotate` audit event records every sweep
(success AND failure) with the active + loaded versions and
the rewrap counts.

### Known limitations

- **In-memory plaintext.** Once a Job is loaded, its
  `Env[secretKey]` holds plaintext (dispatch needs it). A
  process memory dump while the coordinator is live leaks
  every live secret AND the KEK itself. Feature 30's threat
  model explicitly puts "coordinator core dump" in the
  key-compromise bucket.
- **Single coordinator-wide KEK.** Per-tenant KEKs are
  deferred — a multi-tenant deployment wanting per-tenant
  blast-radius limiting needs the identity/tenant model
  which is out of scope for this slice.
- **No passphrase derivation.** Operators supply a 32-byte
  key directly. KDF-from-passphrase adds complexity without
  improving the surface — the env var IS the secret.
- **No auto-expiry / forward secrecy.** KEKs live as long as
  the operator leaves them configured. Forward secrecy
  (ephemeral keys that can be discarded) would add a
  different threat-model surface area; deferred.

See [`docs/planned-features/implemented/30-encrypted-env-storage.md`](planned-features/implemented/30-encrypted-env-storage.md)
for the slice reconciliation and test inventory.

### 9.7 Dry-run preflight (feature 24)

`?dry_run=true` is accepted on every submit/register endpoint
(`POST /jobs`, `POST /workflows`, `POST /api/datasets`,
`POST /api/models`). The request rides the **identical** middleware
chain as a real submit — auth, rate limit, body cap, validators —
but the terminal durable write, dispatch, and bus publish are all
skipped. A distinct audit event type is emitted so a reviewer can
filter probes from real submissions. Key security properties:

- Dry-run is **not** a validation-skip probe oracle. Every validator
  that rejects a real submit also rejects the dry-run equivalent.
- Dry-run is **not** a rate-limit bypass. The shared per-subject
  limiter treats dry-run and real submits identically.
- Dry-run is **not** a duplicate-ID probe. Dry-run does not reserve
  IDs, and dry-run does not surface `ErrAlreadyExists` — it would
  leak membership without adding value.
- An invalid `dry_run` value (`?dry_run=maybe`) returns `400`; silent
  fallback to the real path would turn a typo into an unintended
  submission.

### 9.8 Optional browser mTLS for dashboard operators (feature 27)

After feature 23 shipped TLS 1.3 with hybrid-PQC on the REST
listener, the dashboard → coordinator path is protected against
lateral traffic interception. What remains: a leaked JWT is still
the full access story. Feature 27 adds an optional client-certificate
check so an attacker who steals a JWT (clipboard, screenshare,
compromised extension, lab log line) also needs the operator's
client-cert private key to submit requests.

Three enforcement tiers, selected at coordinator boot via
`HELION_REST_CLIENT_CERT_REQUIRED`:

| Tier    | Value | Behaviour |
|---------|-------|-----------|
| off     | `off` / unset / `0` / `no` | Default. No client-cert check. Existing behaviour. |
| warn    | `warn`                     | Every cert-less request is served, AND emits an `operator_cert_missing` audit event. Used for staged rollouts: flip to `warn`, watch the audit log, ask operators on bearer-only to install certs, then flip to `on`. |
| on      | `on` / `1` / `yes` / `required` | Cert-less requests are refused at 401. `/healthz` and `/readyz` remain exempt so k8s-style probes keep working. |

Malformed values are fatal at coordinator startup — a typo must not
silently weaken security below the default.

Safety properties:

- **CA is shared with node mTLS.** The coordinator's existing CA
  signs both node and operator certs. Operator certs carry
  `ExtKeyUsage = ClientAuth` ONLY — a leaked operator cert cannot
  be re-used to stand up a fake server. Node certs keep both
  `ClientAuth` and `ServerAuth` because nodes act as both.
- **Admin role is subject to the same check.** There is no escape
  hatch for the admin role; admin tokens + cert-less = 401 in `on`
  mode.
- **Issuance is admin-mediated.** `POST /admin/operator-certs`
  (admin-only, rate-limited 1 / 10 s, audit-before-response
  fail-closed on audit-sink failure) mints a fresh ECDSA P-256
  client cert + PKCS#12 bundle encrypted with an operator-supplied
  password. Every issuance writes `operator_cert_issued` to the
  audit log with CN + serial + fingerprint.
- **The CLI `helion-issue-op-cert`** wraps that HTTP path for
  operator convenience.
- **`operator_cn` stamped on audit events.** When a verified client
  cert is present, every subsequent audit event (job submits,
  reveal-secret, cert issuance itself) carries an `operator_cn`
  detail alongside the JWT subject. Enables attribution beyond "an
  admin token did something".
- **Nginx-proxy mode.** If Nginx terminates TLS in front of the
  coordinator, set `ssl_verify_client on` on Nginx and forward
  `X-SSL-Client-Verify`, `X-SSL-Client-S-DN`,
  `X-SSL-Client-Fingerprint`. The coordinator accepts those headers
  **only from loopback** (`127.0.0.1`, `::1`) — any non-loopback
  peer carrying those headers is treated as cert-less to prevent
  header smuggling.
- **Read-once response.** `POST /admin/operator-certs` returns the
  private key + P12 once. The server does not retain either; a
  lost response means the operator requests a fresh issuance
  (which mints a new serial).

What mTLS does NOT solve:

- **In-browser compromise.** An attacker running code inside the
  operator's browser can use the installed cert directly — it's
  a network-boundary control, not an in-browser one. Tracked as
  [feature 34 — WebAuthn/FIDO2](planned-features/34-webauthn-fido2.md),
  which moves the key to a hardware device requiring physical touch.
- **Revocation.** Cert rotation is TTL-based today (90 days by
  default). Explicit revocation (CRL or OCSP) is tracked as
  [feature 31](planned-features/31-cert-revocation-crl-ocsp.md).
- **Cert issuance UX.** CLI is the shipping interface; a dashboard-
  based admin issuance action is tracked as
  [feature 32](planned-features/32-web-cert-issuance-ui.md).
- **Per-operator accountability.** Every operator cert today
  carries the same flat JWT role; `operator_cn` is the only
  per-operator distinguisher. Richer identity → token binding is
  [feature 33](planned-features/33-per-operator-accountability.md).

### 9.9 Job log persistence (feature 28 — PG-authoritative)

Per-job stdout/stderr lives in two stores with well-defined
roles:

- **PostgreSQL `job_log_entries`** — authoritative long-term home.
  Never pruned by the analytics retention cron. A log line from 6
  months ago is still queryable via
  `GET /api/analytics/job-logs?job_id=…`.
- **BadgerDB `log:` prefix** — short-term live-tail cache. 7 d
  TTL by default; individual entries freed sooner by the
  reconciler once PG confirms them.

**Reconciler safety contract:**

- `SELECT 1 FROM job_log_entries WHERE (job_id, seq) = ($1, $2)`
  per candidate; deletion happens only on a positive hit.
- `MinAge` gate (default 5 min) protects just-landed chunks from
  racing the sink's batched flush.
- PG query errors never cascade into deletions. Next reconciler
  tick retries cleanly.
- Opt-in via `HELION_LOGSTORE_RECONCILE`. Operators who prefer
  dual-copy forever leave it off; the Badger TTL still frees
  space eventually, the PG copy remains permanent.
- `TestReconciler_ConfirmedAreDeletedUnconfirmedKept` +
  `TestReconciler_PGQueryError_NoDeletes` +
  `TestReconciler_AgeGate_SkipsYoung` guard the contract against
  regressions.

**What this buys:**

- Badger stays small on high-log clusters (freeing disk pressure
  on the operational KV store).
- Log history persists as long as PG does (no forced 7 d ceiling).
- Operators keep the fast live-tail UX that Badger's prefix scan
  provides for in-flight jobs.

**What this doesn't buy:**

- It's not a replacement for the audit log. Logs contain whatever
  the job printed; they are not accountability records in the way
  `audit/` BadgerDB entries are.
- A PG outage during a live job means Badger keeps everything
  until PG comes back. If PG never comes back and Badger's TTL
  fires first, those chunks are lost — same as today. An
  operator running a cluster without durable PG is opting into
  Badger's TTL as the retention ceiling.

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
