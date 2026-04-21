> **Audience:** engineers + operators
> **Scope:** Threat-model summary, grouped by subsystem, plus the index into crypto/auth/runtime/data-plane reference.
> **Depth:** reference

# Security — threat model and index

Security reference for the Helion v2 minimal orchestrator: post-quantum
cryptography, JWT + policy authentication, rate limiting, audit logging,
runtime hardening, and data-plane protections. Threats below are grouped by
the subsystem that owns the mitigation; every row carries the feature number
that shipped the control so back-references to
[`../planned-features/implemented/`](../planned-features/implemented/) stay
one click away.

## Subsystem reference

| File | What it covers |
|---|---|
| [crypto.md](crypto.md) | mTLS + hybrid-PQC key exchange, internal CA, node + operator cert lifecycle and revocation, envelope encryption of secrets at rest. |
| [auth.md](auth.md) | JWT lifecycle, rate limiting (submit / analytics / registry), REST surface, Principal model, resource ownership, authz policy engine, groups + shares. |
| [operator-auth.md](operator-auth.md) | Operator-facing auth layers: browser mTLS, token ↔ cert-CN binding, WebAuthn / FIDO2. |
| [runtime.md](runtime.md) | Node-side hardening — submit guards, env denylist, service-spec validation, dry-run preflight, ML dashboard module surface. |
| [data-plane.md](data-plane.md) | Audit log, secret env redaction, stdout scrubbing, PostgreSQL-authoritative log store, analytics PII posture, ML artifact attestation. |

For the runbook side (restart, rotation, revocation, troubleshooting) see
[`../operators/runbook.md`](../operators/runbook.md) and the targeted
operator guides:

- [operators/cert-rotation.md](../operators/cert-rotation.md) — operator-cert mint / rotate / revoke.
- [operators/webauthn.md](../operators/webauthn.md) — hardware-authenticator registration.
- [operators/jwt.md](../operators/jwt.md) — token issuance and revocation.

## Threat model

### Transport + cluster auth (see [crypto.md](crypto.md))

| Threat | Mitigation |
|---|---|
| Rogue node connecting to coordinator | mTLS — coordinator verifies node certificate on every connection. |
| Intercepted coordinator↔node traffic (today) | TLS 1.3 with X25519 key exchange. |
| Intercepted traffic decrypted by future quantum computer | Hybrid ML-KEM (Kyber-768) key exchange. |
| Tampered node certificate | ML-DSA (Dilithium-3) out-of-band signature verified on every registration. |
| New cert silently replacing an existing node's cert | SHA-256 certificate fingerprint pinned on first registration; mismatch rejected. |
| Revoked node with active heartbeat stream | Active gRPC stream closed immediately on revocation via done channel. |
| Attacker with filesystem access reads secret plaintext from BadgerDB | **Feature 30.** Per-job DEKs wrap each secret value in AES-256-GCM; DEKs wrapped in AES-256-GCM under a coordinator-held root KEK. Rotation supported via `/admin/secretstore/rotate`. |
| Operator laptop stolen with P12 imported in browser | P12 password at import time + OS keychain user-auth gating; short TTL (90 days default). **Feature 31.** Admin revokes the cert via `POST /admin/operator-certs/{serial}/revoke`; coordinator rejects the serial at client-cert middleware AND publishes a signed CRL via `GET /admin/ca/crl`. |
| Attacker forges `X-SSL-Client-Verify: SUCCESS` to bypass mTLS | Coordinator honours those headers ONLY from loopback (127.0.0.1 / ::1). |

### API identity + authorization (see [auth.md](auth.md))

| Threat | Mitigation |
|---|---|
| Stolen API token used after expiry | JWT 15-minute expiry enforced. |
| Stolen API token used before expiry | JTI-based revocation via `DELETE /admin/tokens/{jti}`; effective within 1 s. |
| Leaked root token from a prior coordinator run | Root token rotated (old JTI revoked) on every restart. |
| Privilege escalation via token sharing | Scoped tokens issued per-user via `POST /admin/tokens`; admin role required. |
| API abuse / DoS from a single node | Per-node token-bucket rate limiter with `GarbageCollect` to bound memory. |
| ML-pipeline DoS via oversize artifact metadata | `http.MaxBytesReader` at 1 MiB on `POST /api/datasets` and `POST /api/models`. Rate-limited per-subject via `registryQueryAllow` (2 rps burst 30). |
| Leaked workflow token escalates to admin (mints more tokens, revokes nodes) | `submit.py` mints a `job`-role token per workflow (subject `workflow:<id>`, TTL 1 h); `adminMiddleware` returns 403 for `/admin/*` on the `job` role. Feature 37's policy engine confines `job`-role reads to the owning workflow. |
| Leaked workflow token persists after the pipeline finishes | 1-hour TTL; operator can `DELETE /admin/tokens/{jti}` to invalidate immediately. |
| Compromised node forges a `node:<id>`-role JWT with `role=admin` | **Feature 35 + 37.** `Principal.IsAdmin()` returns false for `KindNode` regardless of the `Role` field. Feature 37's policy engine denies `KindNode` on every REST action. |
| Legacy records (pre-feature-36) loaded without an owner field | **Feature 36.** Load-time backfill stamps `SubmittedBy`-derived owner or `legacy:` sentinel. Feature 37 treats `legacy:`-owned resources as admin-only. |

### Operator-side auth (see [operator-auth.md](operator-auth.md))

| Threat | Mitigation |
|---|---|
| Admin JWT leaks via clipboard / screenshare / browser extension → remote attacker submits jobs from the internet | **Feature 27 optional.** `HELION_REST_CLIENT_CERT_REQUIRED=on` requires a verified client cert. Staged rollout via `warn` tier. |
| Admin's leaked JWT used from ANY operator's browser succeeds as long as signature verifies | **Feature 33.** Tokens minted with `{"bind_to_cert_cn": "alice@ops"}` carry a `required_cn` JWT claim; `authMiddleware` refuses requests whose verified operator cert CN doesn't match. A stolen bound token is useless in anyone else's browser. |
| Compromised browser process uses the installed operator cert | **Feature 34.** Admins register YubiKeys / platform authenticators via `/admin/webauthn/register-*`. The subsequent `login-*` ceremony requires a hardware touch to mint a WebAuthn-backed JWT (`auth_method: "webauthn"`). `HELION_AUTH_WEBAUTHN_REQUIRED=on` refuses non-bootstrap admin endpoints for tokens without the claim. |

### Runtime hardening and submit guards (see [runtime.md](runtime.md))

| Threat | Mitigation |
|---|---|
| Attacker with submit permission hijacks subprocess execution via a dynamic-loader env var (`LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, `LD_LIBRARY_PATH`, `GCONV_PATH`, …) | **Feature 25.** Centralised denylist rejects every env key matching `LD_*` / `DYLD_*` prefixes or the five exact glibc module-loading names. Applied on `POST /jobs`, `POST /workflows` per child, and `?dry_run=true`. Rust runtime's `env_clear()` is defence-in-depth; the denylist is load-bearing on the Go runtime. |
| Attacker stages a `file://` artifact pointing at a system library or secret-material path (`/lib/libc.so.6`, `/proc/self/environ`, `/var/run/secrets/...`) | **Feature 25.** `validateArtifactBindingsCtx` refuses `file://` URIs rooted at system / secret paths. Path `path.Clean`-normalised first. |
| Attacker stages a loader-critical library under the job's working dir as a dlopen/LD_LIBRARY_PATH hijack target | **Feature 25.** `isDangerousLibraryBasename` rejects loader-critical basenames. Narrow on purpose — legitimate CUDA/Torch libs still pass. |
| Admin operator needs `LD_LIBRARY_PATH` on a specific GPU pool for CUDA dlopen | **Feature 25 per-node overrides.** `HELION_ENV_DENYLIST_EXCEPTIONS=role=gpu:LD_LIBRARY_PATH` on the coordinator. Override requires NodeSelector match AND denylist-key membership AND emits `env_denylist_override` audit. |
| Malicious service job binds a privileged port or hides behind a non-loopback probe | **Feature 17.** `validateServiceSpec` rejects `port < 1024`, `port > 65535`, non-absolute `health_path`, whitespace/NUL in the path, `health_initial_ms > 30 min`. Prober binds `127.0.0.1` only. |
| Compromised node forges `service.ready` for another node's job | `grpcserver.ReportServiceEvent` compares `ServiceEvent.NodeId` against the pinned `Job.NodeID`. |
| CPU job on a GPU-equipped node escapes per-job GPU pinning by setting its own `CUDA_VISIBLE_DEVICES` | Runtime stamps `CUDA_VISIBLE_DEVICES=""` into the subprocess env map (not via OS env precedence) for `req.GPUs == 0` on nodes with `allocator.Capacity() > 0`. |
| Operator tests a submit shape to probe validators without triggering the real path | **Feature 24.** `?dry_run=true` rides the identical middleware chain as a real submit; same validators, same rate limit, distinct audit event. Not a rate-limit bypass, not an ID-reservation probe, not a validation-skip probe. |

### Data plane: audit, secrets, logs, analytics (see [data-plane.md](data-plane.md))

| Threat | Mitigation |
|---|---|
| Undetected compromise post-incident | Append-only audit log covers all security events including token issuance/revocation. Default TTL 90 d; `HELION_AUDIT_TTL=0` disables expiry. |
| Dashboard user / CI read-only token extracts `HF_TOKEN` / `AWS_SECRET_ACCESS_KEY` by calling `GET /jobs/{id}` | **Feature 26.** Submitter flags keys via `secret_keys`; server replaces matching values with `[REDACTED]` on every response path. |
| Admin operator needs to recover a declared secret value (forgot, debugging, credential rotation) | **Feature 26 reveal-secret.** `POST /admin/jobs/{id}/reveal-secret` — admin-only, rate-limited (1/5s, burst 3), mandatory audit `reason`, audit-before-response fail-closed, refuses non-declared keys. |
| Attacker probes "does job X have a secret named Y?" via reveal-secret 404s | Every reject audited as `secret_reveal_reject` with target job_id + key + reason-for-reject. |
| Job prints its own `$HELION_TOKEN` to stdout → captured by logstore → visible via `GET /jobs/{id}/logs` | **Feature 29.** `logstore.ScrubbingStore` decorator substitutes every declared secret VALUE with `[REDACTED]` before chunks land in BadgerDB; response-path redactor repeats the substitution; the feature-28 analytics mirror is scrubbed before publish. Per-job RBAC gates log reads. |
| Compromised node reports a cross-job artifact URI (claims job A's bytes live under job B's prefix) | `attestOutputs` rejects any URI not matching `<scheme>://<bucket>/jobs/<reporting-job-id>/<local_path>`. |
| Compromised node reports an undeclared output name | `attestOutputs` cross-checks every reported `Name` against `Job.Outputs` (feature 12 audit 2026-04-15-05). |
| Compromised node serves tampered artifact bytes under a valid URI | Downstream node's Stager runs `artifacts.GetAndVerify` — returns `ErrChecksumMismatch` if the digest doesn't match the SHA-256 committed with the URI. |
| Analytics database compromised; attacker reads JWT subjects (PII) out of a PG dump | **Feature 28 PII mode.** `HELION_ANALYTICS_PII_MODE=hash_actor` writes `sha256(salt \|\| actor)` into every analytics `actor` column. Audit log remains authoritative for accountability. |
| Analytics database fills indefinitely | **Feature 28 retention cron — opt-in.** `HELION_ANALYTICS_RETENTION_DAYS` default 0. `job_log_entries` is EXCLUDED from the retention cron regardless. |
| Secret env values sneak into analytics tables | **Feature 28 defence in depth.** `submission_history` stores only `resource_id` (ULID), never the submit body. |
| Per-job logs outgrow Badger's 7 d TTL and old stdout is lost | **Feature 28.** Dual-write to PostgreSQL `job_log_entries` (authoritative long-term) + Badger (live tail). Reconciler deletes Badger's copy ONLY on confirmed PG hits. |
| Reconciler deletes a Badger log chunk that isn't actually in PG (data loss) | **Confirm-before-delete.** `SELECT 1 FROM job_log_entries WHERE (job_id, seq) = …`; deletion only on positive hit. `TestReconciler_PGQueryError_NoDeletes` + `TestReconciler_ConfirmedAreDeletedUnconfirmedKept` guard. |
| Race between a just-landed chunk and the Badger reconciler | **MinAge safety margin.** Reconciler skips entries newer than `HELION_LOGSTORE_RECONCILE_MIN_AGE_MIN` (default 5 min) even if PG reports them confirmed. |

### Supply chain and dependencies

| Threat | Mitigation |
|---|---|
| Vulnerable Go dependency | Snyk scans `go.mod` on every push; blocks on high severity. |
| Vulnerable container OS packages | Snyk container scan of coordinator image on every push. |

## References

- [NIST FIPS 203: ML-KEM (Kyber)](https://csrc.nist.gov/pubs/fips/203/final)
- [NIST FIPS 204: ML-DSA (Dilithium)](https://csrc.nist.gov/pubs/fips/204/final)
- [Cloudflare circl library](https://github.com/cloudflare/circl)
- [RFC 7519: JSON Web Token (JWT)](https://datatracker.ietf.org/doc/html/rfc7519)
- [golang.org/x/time/rate — token bucket](https://pkg.go.dev/golang.org/x/time/rate)
