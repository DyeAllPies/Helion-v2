# internal/persistence

BadgerDB wrapper for the Helion coordinator.

## Rules

**No package outside `persistence/` imports BadgerDB.**
All storage access goes through `Store`. This is the boundary that makes the §3.3
swap path to etcd possible without touching business logic.

**All keys are built through the typed constructors in `keys.go`.**
Never write `[]byte("nodes/" + addr)` in a business-logic file.
Use `persistence.NodeKey(addr)` instead. A rename is then a one-file change.

**Proto types are the wire format.**
`Put[T]` and `Get[T]` only accept `proto.Message` values.
The sole exception is `PutRaw`/`GetRaw`, reserved for X.509 DER bytes under `certs/`.

**TTL is explicit.**
`Put` never sets a TTL. If a value must expire (nodes/, tokens/), use `PutWithTTL`.
This makes expiry intent visible at the call site.

**Audit entries are append-only.**
Use `AppendAudit`. Never `Put` to a key under `audit/` — the key schema would
allow it, but the audit log must be immutable.

## Key schema (§4.5)

| Prefix         | Value type         | TTL               |
|----------------|--------------------|-------------------|
| `nodes/{addr}` | Node (proto)       | 2× heartbeat interval |
| `jobs/{id}`    | Job (proto)        | none (permanent)  |
| `workflows/{id}` | Workflow (JSON)  | none (permanent)  |
| `certs/{id}`   | X.509 DER (raw)    | none (permanent)  |
| `audit/{ts}-{id}` | AuditEvent (proto) | none (append-only) |
| `tokens/{jti}` | JWT metadata (proto) | token expiry    |
| `log:{job_id}:{seq}` | LogEntry (JSON) | 7 days (configurable) |
| `datasets/{name}/{version}` | Dataset (JSON) | none (permanent, feature 16) |
| `models/{name}/{version}` | Model (JSON) | none (permanent, feature 16) |

Notes on the ML additions (features 11–19):

- `datasets/` and `models/` share the coordinator's BadgerDB.
  The registry metadata is small and low-traffic compared to
  jobs; a separate DB file would be operational overhead for no
  isolation benefit. See
  [COMPONENTS.md § 5.4](COMPONENTS.md#54-registry--internalregistry).
- The `jobs/{id}` Job record gains a
  `ResolvedOutputs []ArtifactOutput` field when a job completes
  with declared outputs. This field is the authoritative record
  of `(name, uri, sha256, size)` tuples that downstream workflow
  jobs' `from:` references read from — attested via scheme +
  `jobs/<job-id>/` prefix + `local_path` suffix + declared-name
  checks before persistence.
- `cluster.ServiceRegistry` (feature 17 live inference
  endpoints) is **in-memory only** and deliberately not
  persisted. A coordinator restart starts with an empty map; the
  next node-side probe tick re-populates within ~5 s. Persisting
  would risk surfacing stale entries pointing at gone
  `node:port` addresses — worse than a brief empty state.

## Running tests

```
go test ./internal/persistence/... -v
```

Skip the TTL test (which sleeps 2 s) with `-short`:

```
go test ./internal/persistence/... -short
```

Run with the race detector (required in CI):

```
go test -race ./internal/persistence/...
```

## Three-tier storage (analytics + ML artifacts)

The base deployment is BadgerDB-only. Two optional tiers layer
on top when the corresponding features are enabled; each has a
distinct access pattern that would be a poor fit for BadgerDB:

| Tier | Engine | Purpose | Opt-in | Consistency |
|---|---|---|---|---|
| Operational | BadgerDB (embedded) | Dispatch, heartbeats, state transitions, registry metadata, `ResolvedOutputs` | always on | Strong (synchronous) |
| ML artifacts | S3-compatible (MinIO/AWS), `file://` fallback | Training/model bytes addressed by `s3://<bucket>/jobs/<job-id>/<path>` | `HELION_ARTIFACTS_BACKEND=s3` | Strong per-object (Put-then-Get); integrity via SHA-256 |
| Analytical | PostgreSQL (external) | Historical event facts, dashboard analytics | `HELION_ANALYTICS_DSN` | Eventually consistent (≤ flush interval, default 500 ms) |

Each tier's unavailability degrades a different capability:

| Tier down | Effect |
|---|---|
| BadgerDB | Coordinator unusable — scheduling + registry + auth all blocked |
| Object store | New ML jobs fail at Stager.Finalize (upload) or dispatch (verified download); operational control plane unaffected |
| PostgreSQL | Analytics dashboard loses historical data; operational state intact |

Key boundaries:

- **BadgerDB is never written by hot paths that move artifact
  bytes.** The Stager owns uploads; the coordinator only
  persists the `(name, uri, sha256, size)` tuple on the Job
  record after attesting it.
- **The object store is never read by the dashboard.** Dashboard
  reads lineage via `GET /workflows/{id}/lineage` which joins
  the Job record's `ResolvedOutputs` against the model registry
  — no direct S3 access from the browser.
- **PostgreSQL is never written by the operational hot path.**
  The analytics sink subscribes to the in-memory event bus and
  batches writes out-of-band; the coordinator's dispatch loop
  never blocks on PG.

See [ml-pipelines.md](ml-pipelines.md) for the ML-side
implications and [ARCHITECTURE.md § 13](ARCHITECTURE.md#13-ml-pipeline)
for the diagram.
