> **Audience:** engineers
> **Scope:** `internal/persistence` rules, BadgerDB key schema, and TTL conventions.
> **Depth:** reference

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
  [COMPONENTS.md § 5.4](../COMPONENTS.md#54-registry--internalregistry).
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

See [ml-pipelines.md](../guides/ml-pipelines.md) for the ML-side
implications and [ARCHITECTURE.md § 12](../ARCHITECTURE.md#12-ml-pipeline)
for the diagram.

## Storage tiers — audit ↔ analytics ↔ logs (feature 28)

Helion's persistence story runs on **three stores with distinct
purposes**. Knowing which to hit for a given question is the
difference between a clean forensic trail and a support escalation.

| Store           | Backing | Retention | Purpose | Query shape |
|---|---|---|---|---|
| **Audit log**   | BadgerDB (`audit/` prefix) | **forever** (append-only) | Accountability. Compliance. Security incident response. | Per-key lookup; `Scan(prefix)` range walk. |
| **Analytics (non-log)** | PostgreSQL (feature 09 + 28) | **indefinite by default**; opt-in retention via `HELION_ANALYTICS_RETENTION_DAYS` | Dashboards. Capacity planning. Trend graphs. | `GROUP BY date_trunc` aggregation + percentiles. |
| **Log store**   | PostgreSQL (`job_log_entries`) — **authoritative long-term home**; BadgerDB (`log:` prefix) — **short-term cache freed once PG has the chunk** | PG: indefinite (NEVER pruned by the retention cron); Badger: default 7 d TTL, or freed sooner by the reconciler when PG confirms | Per-job stdout/stderr. | `GET /jobs/{id}/logs` (Badger, fast live-tail); `GET /api/analytics/job-logs?job_id=…` (PG, full history). |

### Why PG is the log long-term store, not Badger

Badger's value on the log path is fast live-tail — the existing
`/jobs/{id}/logs` endpoint scans a key prefix and streams. That
works great for the first few minutes of a job's life. It stops
being a good answer at month-old-archive timescales:

- Badger is a key-value engine; log search / filtering across jobs
  requires reading every key.
- The Badger TTL (default 7 d) silently drops old chunks; there's
  no "rotate to S3" escape hatch.
- Operators who want SQL-shaped queries against log bytes
  (`WHERE data ILIKE '%traceback%'`) can't get there from Badger.

PostgreSQL's `job_log_entries` table solves all three. The
feature-28 reconciler flips the natural deletion direction:

1. Node streams a chunk → gRPC `StreamLogs` handler.
2. Handler writes to Badger (primary read path today) AND
   publishes `job.log` to the event bus.
3. Analytics sink batches the event into `job_log_entries`.
4. Reconciler periodically asks PG "do you have (job_id, seq)?"
   and deletes the Badger copy when the answer is yes AND the
   entry is at least `MinAge` old.

After the reconciler catches up, **PG is the only durable copy**.
That's the intended steady state: PG stores every chunk forever,
Badger keeps only the last few minutes for live-tail speed.

### Invariants

- **PG `job_log_entries` is NEVER pruned by the retention cron.**
  `retainedTables` in `internal/analytics/retention.go` deliberately
  excludes it; a test (`TestRetentionCron_NeverPrunesJobLogs`)
  guards against regressions. If an operator enables retention via
  `HELION_ANALYTICS_RETENTION_DAYS=30`, every other feature-28
  table ages out but logs stay.
- **Badger log entries are deleted only when PG confirms them.**
  The reconciler runs a `SELECT 1 FROM job_log_entries WHERE
  job_id = $1 AND seq = $2` for each candidate; a miss means
  "don't delete". A PG outage never causes data loss — the next
  tick retries.
- **Safety-margin MinAge (default 5 min)** keeps just-landed
  chunks from racing the sink's batched flush. Entries younger
  than MinAge are skipped even when PG confirms them.
- **Every event in the `events` / feature-28 tables has a
  complementary entry in the audit log.** Losing an analytics row
  (retention, corruption, restore from backup) never compromises
  accountability — the audit log in BadgerDB is authoritative.
  The one exception: **logs are not in the audit log**; they live
  only in PG (long-term) + Badger (short-term cache), which is
  why PG logs are never pruned.

### Which store to hit

- "Who submitted this job?" → **audit log**. Canonical per-event
  record, never rotated.
- "How many jobs per hour did we run last week?" → **analytics**.
  Time-series-shaped, fast range aggregation.
- "Show me the stdout of job X from 30 minutes ago." → **Badger
  log cache** via `/jobs/{id}/logs`. Fast live-tail.
- "Show me the stdout of job X from 2 months ago." → **PG
  `job_log_entries`** via `/api/analytics/job-logs?job_id=X`. The
  reconciler has long since deleted Badger's copy, but PG has
  everything.
- "Who logged in during the breach window?" → **audit log** for
  accountability AND **analytics** `auth_events` for the
  time-series form. Both stores are authoritative, both are
  cross-referenceable by `occurred_at`.

### Compromise semantics

- **A compromised analytics database does NOT compromise the audit
  trail.** The audit log in Badger is untouched. Logs are at risk
  (PG is the long-term home), but the audit log proves what ran.
- **A compromised audit store is a full cluster compromise.** If
  BadgerDB is writable by an attacker, both audit history and job
  state are in play — treat as a security incident.
- **PII handling differs.** The audit log always stores raw actor
  subjects (accountability). The analytics store can hash subjects
  via `HELION_ANALYTICS_PII_MODE=hash_actor` for privacy-minded
  deployments — dashboards still get per-subject trends via
  consistent hashing, but a PG dump doesn't expose raw identities.

### Configuration knobs

Retention + PII + reconciler — all off by default. Operators opt
in as their deployment grows.

- `HELION_ANALYTICS_RETENTION_DAYS` — default **0 (disabled)**.
  Set positive to prune non-log analytics rows older than N days.
  `job_log_entries` is excluded regardless.
- `HELION_ANALYTICS_PII_MODE` — `off` (default) or `hash_actor`.
  Controls whether actor columns are written as raw strings or
  SHA-256 hashes.
- `HELION_ANALYTICS_PII_SALT` — per-install salt prepended to the
  SHA-256. Empty salt logs a WARN at boot; rotating mid-deployment
  breaks per-subject grouping across the rotation boundary
  (documented, not automated).
- `HELION_LOGSTORE_RECONCILE` — set to any non-empty value to
  enable the Badger→PG reconciler. Default: disabled (Badger's
  7 d TTL is the only log cleanup).
- `HELION_LOGSTORE_RECONCILE_INTERVAL_MIN` — minutes between
  reconciler sweeps. Default 10.
- `HELION_LOGSTORE_RECONCILE_MIN_AGE_MIN` — minimum age an entry
  must be before it's eligible for reconciler deletion. Default 5.
- `HELION_LOGSTORE_RECONCILE_BATCH` — candidates per PG query.
  Default 200.
