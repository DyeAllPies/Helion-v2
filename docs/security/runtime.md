> **Audience:** engineers + operators
> **Scope:** Node-side runtime hardening — submit guards, env denylist, service-spec validation, dry-run preflight.
> **Depth:** reference

# Security — runtime hardening

Every submit path is a trust boundary: once the coordinator accepts a job
definition, the node runtime honours its env, `args`, command, and artifact
bindings exactly as submitted. The controls below are what stops a
submit-permission attacker from turning a job description into a node-wide
compromise.

## 1. Submission tab (feature 22)

The `/submit` route group is the dashboard's single place to start a run —
single jobs, workflows, ML-templated workflows, and form-driven DAGs all POST
through the existing submit endpoints (`POST /jobs`, `POST /workflows`). The
UI is not a trusted client; every control has a server-side counterpart:

| Client control | Server-side counterpart |
|---|---|
| Two-click Validate → Preview → Submit | Per-subject rate limit (10 rps default) bounds accidental-click floods |
| Client-side env-key denylist (`LD_*`, `DYLD_*`, `GCONV_PATH`, …) | **Feature 25.** UX-only in the form; load-bearing rejection lands server-side. See [§ 2](#2-dangerous-env-denylist-feature-25). |
| Secret env toggle (`type="password"` on the value input) | **Feature 26.** Form emits a `secret_keys` list; server redacts values to `[REDACTED]` on every GET path. See [data-plane.md § Secret env redaction](data-plane.md#3-secret-env-redaction-feature-26). |
| Validate button runs shape validator in-browser | **Feature 24.** Validate can call `POST /jobs?dry_run=true` / `/workflows?dry_run=true` / `/api/datasets?dry_run=true` / `/api/models?dry_run=true` — server validator is the authority. See [§ 4](#4-dry-run-preflight-feature-24). |
| YAML/JSON editor uses `JSON.parse` (no YAML) | `JSON.parse` has no code-execution path. YAML arrives via Monaco and MUST use `js-yaml` with `JSON_SCHEMA` (no custom tags, no aliases). |

Rule for future submit paths: **no submit path may bypass the auth + rate
limit + body cap + validators stack. The submit tab is a convenience UI; it
does not relax any server-side check.**

## 2. Dangerous-env denylist (feature 25)

Every submit path (`POST /jobs`, `POST /workflows` per child job, under
`?dry_run=true` too) rejects env vars whose keys match the dynamic-loader /
glibc module-loading denylist:

- **Prefix matches:** `LD_*` (glibc loader — `LD_PRELOAD`, `LD_LIBRARY_PATH`,
  `LD_AUDIT`, …), `DYLD_*` (macOS loader — `DYLD_INSERT_LIBRARIES`, …).
- **Exact matches:** `GCONV_PATH`, `GIO_EXTRA_MODULES`, `HOSTALIASES`,
  `NLSPATH`, `RES_OPTIONS`.

Matched verbatim because the Linux loader itself only honours uppercase — a
lowercase `ld_preload` is inert and doesn't need blocking. Admin role is
subject to the same denylist: there's no legitimate admin workflow that
needs these keys through the submit path.

The Go runtime previously passed submit env directly to `exec.Command.Env`,
meaning anyone with submit permission could hijack every exec on the node
with a single `LD_PRELOAD` value. The Rust runtime's `env_clear()` dodges
this by accident; the denylist is load-bearing on the Go path and
defence-in-depth on both.

### Artifact-staging guards

In addition to env keys, `validateArtifactBindingsCtx` rejects two
artifact-staging patterns that could weaponise subprocess loading even
after the env denylist:

- **`file://` URIs rooted at system-library or secret paths** — `/lib`,
  `/lib64`, `/usr/lib`, `/usr/lib64`, `/usr/local/lib`, `/proc`, `/sys`,
  `/dev`, `/etc`, `/boot`, `/root`, `/var/run/secrets`, `/run/secrets`,
  `/run/credentials`. Path is `path.Clean`-normalised first so `//` tricks
  don't slip through.
- **LocalPath basenames matching loader-critical shared libraries** —
  `libc.so*`, `ld-linux*`, `ld-musl*`, `libpthread.so*`, `libdl.so*`,
  `libm.so*`, `librt.so*`, `libnss_*`, `libresolv.so*`, `libcrypt.so*`.
  Narrow on purpose: legitimate ML libs (`libcudart.so.11.0`,
  `libtorch.so`, `libcuda.so.1`) still pass.

Every reject emits `env_denylist_reject` carrying the blocked key + target
job/workflow id so a reviewer can spot probes in the audit log without
regex-matching error strings.

### Per-node overrides

For legitimate needs (e.g. a dedicated GPU node pool that needs
`LD_LIBRARY_PATH` for CUDA dlopen), the coordinator accepts a whitelist
via `HELION_ENV_DENYLIST_EXCEPTIONS` at startup:

```
HELION_ENV_DENYLIST_EXCEPTIONS=role=gpu:LD_LIBRARY_PATH
HELION_ENV_DENYLIST_EXCEPTIONS=role=gpu:LD_LIBRARY_PATH;pool=build:LD_LIBRARY_PATH,GCONV_PATH
```

Format: `<selector_key>=<selector_value>:<env_key>[,<env_key>]*[;<next_rule>]`.

A job's env var is allowed iff its `NodeSelector` contains the exact
`selector_key=selector_value` pair AND the env key appears in the rule's
list. Safety properties:

- **Only set via coordinator env at startup.** Nodes cannot declare their
  own exceptions — a compromised node can't unlock `LD_PRELOAD` for jobs
  it's about to run.
- **Malformed input fails to start.** A typo yields `os.Exit(1)` at boot
  rather than silently running with the denylist disabled or half-parsed
  rules.
- **Exception keys must themselves be on the denylist.** Adding
  `PYTHONPATH` to an exception rule is a parse error — rules with no
  effect are not silently accepted.
- **Overrides are loudly audited.** Every env var let through emits
  `env_denylist_override` with the job id and env key.
- **Requires explicit selector match.** A job with no NodeSelector or a
  NodeSelector value that doesn't match the rule remains denied. No
  "default allow" behaviour.
- **Dry-run doesn't bypass.** `?dry_run=true` applies the same check with
  the same audit events (`"dry_run": true` detail).

Spec: [planned-features/implemented/25-env-var-denylist.md](../planned-features/implemented/25-env-var-denylist.md).

## 3. Service-job surface (feature 17)

Long-running inference jobs (`SubmitRequest.service`) bypass standard
job-timeout enforcement — by design, a service is supposed to keep running —
so they widen the attack surface in three small ways. The controls below
keep each one within the existing doctrine.

| New attack surface | Control |
|--------------------|---------|
| Service bypasses timeout enforcement | A service job runs only for the lifetime of its Dispatch RPC. The coordinator's `Cancel` RPC is the supported stop path. A compromised node cannot make a non-service job run forever because the `IsService` flag is forwarded from the coordinator-signed `Job.Service`, not from the node. |
| Service exposes a TCP port on the node | The prober only hits `127.0.0.1:<port><health_path>`. The coordinator records `(node_address, port)` but does NOT act as a proxy — `GET /api/services/{id}` returns the upstream URL, authenticated clients hit it directly. Nodes behind Nginx/ingress must publish the port themselves; Helion does not punch holes in network policy. |
| Node could forge a `service.ready` for another node's job | `grpcserver.ReportServiceEvent` compares `ServiceEvent.NodeId` against the pinned `Job.NodeID` from the dispatcher record and returns `PermissionDenied` on mismatch. Same doctrine as `ReportResult`; same `LogSecurityViolation` path. |

### Submit-time validation

`validateServiceSpec` in `internal/api/handlers_jobs.go`:

- `service.port` must be in `[1024, 65535]`. Privileged ports are rejected
  because the production DaemonSet runs as non-root and would fail to bind
  anyway — catching it at submit surfaces a crisp 400 rather than a
  spawn-time crash.
- `service.health_path` must start with `/`, be non-empty, contain no
  whitespace or NUL, fit in 256 bytes.
- `service.health_initial_ms` caps at 30 min so a misconfigured job cannot
  suppress failure detection indefinitely.

### Rate limiting

The existing `POST /jobs` submit limiter covers service submission —
services are ordinary jobs that happen to carry a `service` block.
`GET /api/services/{id}` reads from an in-memory map; no additional
limiter required, standard JWT middleware authenticates the lookup.

### Audit events

| Event type | Emitter | Actor | Target | Details |
|---|---|---|---|---|
| `service.ready` | gRPC `ReportServiceEvent` | `node:<node_id>` | `job:<job_id>` | `port`, `health_path`, `consecutive_failures` (= 0 on transition into ready) |
| `service.unhealthy` | gRPC `ReportServiceEvent` | `node:<node_id>` | `job:<job_id>` | Same shape; `consecutive_failures` = probe-miss streak |

Both edge-triggered (one row per state flip, not per probe tick) so a
healthy service does not bloat the audit log.

### Runtime coverage

The Go runtime (`internal/runtime/go_runtime.go`) implements
`RunRequest.IsService`. The Rust runtime (`runtime-rust/`) does NOT yet —
service jobs dispatched to a Rust-backed node run with the runtime's
default timeout and no probe loop. Tracked in
[planned-features/deferred/20-rust-runtime-service-parity.md](../planned-features/deferred/20-rust-runtime-service-parity.md).
Operators running a Rust-backed cluster that need inference services today
should stay on the Go runtime.

### GPU pinning

| Threat | Mitigation |
|---|---|
| CPU job on a GPU-equipped node escapes per-job GPU pinning by setting its own `CUDA_VISIBLE_DEVICES` | Runtime stamps `CUDA_VISIBLE_DEVICES=""` into the subprocess env MAP (not via OS env precedence) for `req.GPUs == 0` on nodes with `allocator.Capacity() > 0`. Map-based override is unambiguous — no platform-dependent first/last-set-wins. |

## 4. Dry-run preflight (feature 24)

`?dry_run=true` is accepted on every submit/register endpoint
(`POST /jobs`, `POST /workflows`, `POST /api/datasets`,
`POST /api/models`). The request rides the **identical** middleware chain
as a real submit — auth, rate limit, body cap, validators — but the
terminal durable write, dispatch, and bus publish are all skipped. A
distinct audit event type is emitted so reviewers can filter probes from
real submissions. Key security properties:

- Dry-run is **not** a validation-skip probe oracle. Every validator that
  rejects a real submit also rejects the dry-run equivalent.
- Dry-run is **not** a rate-limit bypass. The shared per-subject limiter
  treats dry-run and real submits identically.
- Dry-run is **not** a duplicate-ID probe. Dry-run does not reserve IDs,
  and dry-run does not surface `ErrAlreadyExists` — it would leak
  membership without adding value.
- An invalid `dry_run` value (`?dry_run=maybe`) returns 400; silent
  fallback to the real path would turn a typo into an unintended
  submission.

Spec: [planned-features/implemented/24-dry-run-preflight.md](../planned-features/implemented/24-dry-run-preflight.md).

## 5. ML dashboard module surface (feature 18)

Feature 18 added a lazy-loaded Angular module at `/ml/datasets`,
`/ml/models`, and `/ml/services` plus the supporting REST endpoint
`GET /api/services` (list).

| New attack surface | Control |
|--------------------|---------|
| New client routes | All three views are inside the existing shell, behind the same `authGuard` and JWT interceptor as every other authenticated route. The shell's auth guard rejects unauthenticated nav before any ML-API call leaves the browser. |
| `GET /api/services` exposes node addresses | Already exposed by the per-job lookup; the list variant is the same data over the same auth middleware, just batched. Same-origin CORS via Nginx in production. |
| Register dataset modal accepts free-form URI | Server-side validator (`registry.ValidateURI`) is the authoritative check — rejects anything outside `file://` / `s3://`. The dashboard form's hint is informational; the coordinator's 400 is what the user sees on a bad URI. |
| Delete buttons on Datasets and Models | Confirm prompt is UX, not security. The flat "any authenticated user can delete any entry" model from feature 16 still applies (tightening to admin-only / owner-only deletion is on the [deferred backlog](../planned-features/deferred/17-registry-lineage-enforcement.md)). |

The Services view polls `GET /api/services` every 5 s — the same cadence
as the node-side prober, so the dashboard is never more than one tick
stale. No additional rate limiting is needed because the lookup is a
memory read.

New backend events surfaced to the dashboard:

| Event | When | Surfaced where |
|-------|------|----------------|
| `ml.resolve_failed` | Workflow job's artifact resolver fails (upstream missing, output missing) | Audit log + event bus today; future Pipelines DAG view |
| `job.unschedulable` (extended) | Same as before, plus `reason` field: `no_healthy_node` / `no_matching_label` / `all_matching_unhealthy` | Event-feed view shows the reason verbatim today |
