> **Audience:** operators
> **Scope:** Index for day-2 operations — env vars, runbooks, cert + auth rotation.
> **Depth:** runbook

# Operators

Everything needed to run a Helion cluster: environment variables, startup
checklists, rotation runbooks, and authentication setup for dashboard
operators. For design rationale behind the mitigations, see
[`../security/`](../security/).

## Files in this folder

| File | Contents |
|---|---|
| [runbook.md](runbook.md) | Security-ops checklist — restart, recovery, rotation, revocation. |
| [cert-rotation.md](cert-rotation.md) | Minting, installing, rotating, revoking operator client certificates (features 27 + 31). |
| [webauthn.md](webauthn.md) | Registering hardware authenticators and the step-up login flow (feature 34). |
| [jwt.md](jwt.md) | JWT token lifecycle — issuance, usage, revocation. |
| [docker-compose.md](docker-compose.md) | Local-dev compose workflow notes. |

## Environment variable reference

Grouped by subsystem. For the rationale behind each control, the
`security/` cross-reference on the right side of each table points at
the exact section that owns the feature.

### Core orchestration

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_COORDINATOR` | Node, CLI | `localhost:9090` | Coordinator gRPC address. |
| `HELION_ALLOW_ISOLATION` | Node | `false` | Enable Linux namespace isolation (needs root / `CAP_SYS_ADMIN`). |
| `HELION_RUNTIME` | Node | `go` | Runtime backend: `go` (subprocess) or `rust` (cgroup v2 + seccomp). |
| `HELION_RUNTIME_SOCKET` | Node | _(unset)_ | Unix socket path for the Rust runtime. |
| `HELION_DEFAULT_TIMEOUT_SEC` | Runtime | `300` | Fallback job timeout when `TimeoutSeconds` is unset or ≤ 0. |
| `HELION_ALLOWED_COMMANDS` | Runtime | _(unset)_ | Comma-separated command allowlist; unset = allow-all (dev). |
| `HELION_RATE_LIMIT_RPS` | Coordinator | `10` | Per-node job-submission rate limit — see [security/auth.md § 2](../security/auth.md#2-rate-limiting). |
| `HELION_SCHEDULER` | Coordinator | `roundrobin` | Scheduler policy. Options: `roundrobin`, `least`. |
| `PORT` | Node | `8080` | Node listen port. |

### Certificates and PQC

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_CA_CERT_TTL_DAYS` | Coordinator | `730` | Internal CA certificate lifetime. |
| `HELION_NODE_CERT_TTL_HOURS` | Coordinator | `24` | Node certificate lifetime (renewed on each Register). |
| `HELION_NODE_PINS` | Coordinator | _(unset)_ | Pre-configured cert pins `nodeID:sha256hex,…`; unlisted nodes use first-seen. |
| `HELION_CA_FILE` | All | _(unset)_ | Path to the coordinator CA PEM for in-cluster TLS. |
| `HELION_EXTRA_SANS` | Coordinator | _(unset)_ | Comma-separated extra SANs added to the coordinator's REST cert. |
| `HELION_PQC_REQUIRED` | Coordinator | `0` | `1` fails startup if hybrid-KEM silently downgrades to classical. See [security/crypto.md § 2](../security/crypto.md#2-post-quantum-cryptography). |
| `HELION_REST_TLS` | Coordinator | `on` | `off` disables REST TLS (feature 39 retired the opt-out from default). |
| `HELION_REST_CLIENT_CERT_REQUIRED` | Coordinator | `off` | `off` / `warn` / `on`. Operator-cert mTLS. See [security/operator-auth.md § 1](../security/operator-auth.md#1-browser-mtls-feature-27). |

### Tokens and auth

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_ROTATE_TOKEN` | Coordinator | `true` | Rotate root token on each startup; `false` reuses the stored token. |
| `HELION_TOKEN_FILE` | Coordinator | `/var/lib/helion/root-token` | Path the rotated root token is written to (mode `0600`). |
| `HELION_TOKEN` | CLI | _(unset)_ | Bearer token attached to all API requests. |
| `HELION_JOB_ID` | CLI | _(unset)_ | Stable job ID for idempotent retries. |
| `HELION_WEBAUTHN_RPID` | Coordinator | _(unset)_ | Effective domain (no scheme/port) for WebAuthn. Required to enable the feature. |
| `HELION_WEBAUTHN_DISPLAY` | Coordinator | _(unset)_ | Display name shown in the browser's touch prompt. |
| `HELION_WEBAUTHN_ORIGINS` | Coordinator | _(unset)_ | Comma-separated permitted full-origin URLs. |
| `HELION_AUTH_WEBAUTHN_REQUIRED` | Coordinator | `off` | `off` / `warn` / `on`. See [security/operator-auth.md § 3](../security/operator-auth.md#3-webauthn--fido2-feature-34). |

### Audit, analytics, logs

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_AUDIT_TTL` | Coordinator | `90d` | Audit log TTL. `0` disables expiry. |
| `HELION_ANALYTICS_DSN` | Coordinator | _(unset)_ | PostgreSQL DSN; when set, enables the analytics pipeline and the `/api/analytics/*` surface. |
| `HELION_ANALYTICS_FLUSH_MS` | Coordinator | `1000` | Sink batch-flush interval. E2E cluster pins this to `200` for snappy assertions. |
| `HELION_ANALYTICS_PII_MODE` | Coordinator | _(unset)_ | `hash_actor` hashes JWT subjects in analytics actor columns. See [security/data-plane.md § 6](../security/data-plane.md#6-analytics-pii-mode-feature-28). |
| `HELION_ANALYTICS_RETENTION_DAYS` | Coordinator | `0` | Retention cron over feature-28 tables (excluding `job_log_entries`). `0` disables. |
| `HELION_LOGSTORE_RECONCILE` | Coordinator | _(unset)_ | Opt-in to the PG-confirm-then-delete Badger log reconciler. |
| `HELION_LOGSTORE_RECONCILE_MIN_AGE_MIN` | Coordinator | `5` | MinAge safety margin on the reconciler. |

### Secret storage (feature 30)

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_SECRETSTORE_KEK` | Coordinator | _(unset)_ | 32-byte KEK for envelope encryption of secret env at rest. Hex (64 chars) or base64. See [security/crypto.md § 5](../security/crypto.md#5-encrypted-env-storage-feature-30). |
| `HELION_SECRETSTORE_KEK_VERSION` | Coordinator | `1` | Active KEK version (1–16). |
| `HELION_SECRETSTORE_KEK_V<N>` | Coordinator | _(unset)_ | Older KEK versions during rotation. |

### Artifacts and ML

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_ARTIFACTS_BACKEND` | Coordinator, Node | `local` | `local` or `s3`. See [../guides/ml-pipelines.md](../guides/ml-pipelines.md). |
| `HELION_ARTIFACTS_S3_ENDPOINT` | Coordinator, Node | _(unset)_ | S3 endpoint (MinIO, AWS). |
| `HELION_ARTIFACTS_S3_BUCKET` | Coordinator, Node | _(unset)_ | Bucket name. |
| `HELION_ARTIFACTS_S3_ACCESS_KEY` | Coordinator, Node | _(unset)_ | Access key. |
| `HELION_ARTIFACTS_S3_SECRET_KEY` | Coordinator, Node | _(unset)_ | Secret key. |
| `HELION_ARTIFACTS_S3_USE_SSL` | Coordinator, Node | `1` | `0` disables TLS for the S3 endpoint (MinIO dev). |

### Runtime hardening (feature 25)

| Variable | Component | Default | Description |
|---|---|---|---|
| `HELION_ENV_DENYLIST_EXCEPTIONS` | Coordinator | _(unset)_ | Per-node denylist overrides. See [security/runtime.md § 2](../security/runtime.md#2-dangerous-env-denylist-feature-25). |
