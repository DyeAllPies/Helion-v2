# Feature: Analytics Pipeline (BadgerDB → PostgreSQL)

**Priority:** P1
**Status:** Implemented
**Affected files:** `internal/analytics/`, `internal/api/handlers_analytics.go`, `cmd/helion-coordinator/main.go`, `dashboard/src/app/features/analytics/`, `docker-compose.yml`

## Problem

The current Angular dashboard is **operational** — it shows live cluster state (running jobs,
healthy nodes, queue depth) via WebSocket streams and periodic polling. What it cannot answer:

- What was the **average job duration** last week, broken down by command?
- Which nodes **fail the most** jobs?
- How has **throughput** changed month over month?
- What is the **retry rate** per job type, and does retry improve success?
- How long do jobs **wait in pending** before getting scheduled?

These are analytical questions that require querying across historical data with aggregations,
joins, and time-series grouping. BadgerDB is a key-value store optimised for fast point lookups
and prefix scans — it is the wrong tool for ad-hoc SQL analytics. PostgreSQL is.

## Design principle: dual-database, not a migration

BadgerDB remains the **operational store** — low-latency reads/writes for the coordinator's
hot path (dispatch, heartbeats, state transitions). PostgreSQL becomes the **analytical store** —
append-only event facts, populated asynchronously, queried by dashboards and reports.

The two stores are **eventually consistent** — analytics lag behind operational state by
seconds, which is acceptable for historical analysis.

```
┌──────────────┐   events    ┌──────────────┐  async write  ┌──────────────┐
│  Coordinator │────────────▶│  Event Bus   │──────────────▶│  PostgreSQL  │
│  (BadgerDB)  │             │  (in-memory) │               │  (analytics) │
└──────────────┘             └──────────────┘               └──────────────┘
       │                                                           │
       │  operational queries                   analytical queries │
       ▼                                                           ▼
┌──────────────┐                                     ┌──────────────────┐
│  Angular     │                                     │  Analytics       │
│  Dashboard   │                                     │  Dashboard       │
│  (existing)  │                                     │  (new module)    │
└──────────────┘                                     └──────────────────┘
```

---

## Step 1 — PostgreSQL schema and migrations

Create a `migrations/` directory with numbered SQL migration files. Use a lightweight
migration runner (golang-migrate or hand-rolled sequential apply).

### Core tables

```sql
-- Immutable fact table: one row per event emitted by the coordinator.
CREATE TABLE events (
    id          UUID PRIMARY KEY,
    event_type  TEXT        NOT NULL,   -- "job.submitted", "node.stale", etc.
    timestamp   TIMESTAMPTZ NOT NULL,
    data        JSONB       NOT NULL,   -- full event payload
    -- Denormalised columns for fast filtering/grouping:
    job_id      TEXT,                   -- extracted from data->'job_id'
    node_id     TEXT,                   -- extracted from data->'node_id'
    workflow_id TEXT                    -- extracted from data->'workflow_id'
);

CREATE INDEX idx_events_type      ON events (event_type);
CREATE INDEX idx_events_timestamp ON events (timestamp);
CREATE INDEX idx_events_job       ON events (job_id)      WHERE job_id IS NOT NULL;
CREATE INDEX idx_events_node      ON events (node_id)     WHERE node_id IS NOT NULL;
CREATE INDEX idx_events_workflow  ON events (workflow_id)  WHERE workflow_id IS NOT NULL;

-- Materialised snapshot: one row per job, updated on each transition event.
-- Rebuilt from events, not from BadgerDB — analytics is self-contained.
CREATE TABLE job_summary (
    job_id          TEXT PRIMARY KEY,
    command         TEXT,
    priority        INT,
    workflow_id     TEXT,
    node_id         TEXT,
    final_status    TEXT,               -- last known status
    submitted_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,        -- first "running" transition
    completed_at    TIMESTAMPTZ,        -- terminal state timestamp
    duration_ms     BIGINT,
    attempts        INT DEFAULT 1,
    last_error      TEXT,
    last_exit_code  INT
);

-- Materialised snapshot: one row per node, tracking registration and health.
CREATE TABLE node_summary (
    node_id         TEXT PRIMARY KEY,
    address         TEXT,
    first_seen      TIMESTAMPTZ,
    last_seen       TIMESTAMPTZ,
    times_stale     INT DEFAULT 0,
    times_revoked   INT DEFAULT 0,
    jobs_completed  INT DEFAULT 0,
    jobs_failed     INT DEFAULT 0
);
```

### Why denormalise into summary tables?

Raw events are great for flexibility but slow for "show me all jobs that failed on node X
last week." The summary tables are cheap to maintain (upsert on each event) and make the
common dashboard queries single-table scans.

---

## Step 2 — Analytics sink (event bus → PostgreSQL writer)

New package: `internal/analytics/`

### Sink design

```go
// internal/analytics/sink.go

// Sink subscribes to the event bus and writes events to PostgreSQL.
// It batches writes for efficiency and uses a WAL-style approach:
// events are buffered in memory, flushed every 500ms or 100 events
// (whichever comes first).
type Sink struct {
    db      *sql.DB
    bus     *events.Bus
    sub     *events.Subscription
    batch   []events.Event
    mu      sync.Mutex
    stopCh  chan struct{}
}

func NewSink(db *sql.DB, bus *events.Bus) *Sink { ... }
func (s *Sink) Start(ctx context.Context) error  { ... }
func (s *Sink) Stop()                             { ... }
```

### Batch insert strategy

Each flush performs a single `INSERT INTO events ... VALUES ($1...), ($2...)...` with
a batch of events, then runs upserts on `job_summary` / `node_summary` in the same
transaction. This keeps PostgreSQL write amplification low.

### Event-to-summary mapping

| Event type           | job_summary update                     | node_summary update         |
|----------------------|----------------------------------------|-----------------------------|
| `job.submitted`      | INSERT (job_id, command, priority, submitted_at) | —                  |
| `job.transition`     | UPDATE final_status; SET started_at if to=running | —                |
| `job.completed`      | UPDATE final_status, completed_at, duration_ms, node_id | INCREMENT jobs_completed |
| `job.failed`         | UPDATE final_status, last_error, last_exit_code, attempts | INCREMENT jobs_failed |
| `job.retrying`       | UPDATE attempts                        | —                           |
| `node.registered`    | —                                      | UPSERT (node_id, address, first_seen, last_seen) |
| `node.stale`         | —                                      | INCREMENT times_stale       |
| `node.revoked`       | —                                      | INCREMENT times_revoked     |
| `workflow.completed` | —                                      | —                           |
| `workflow.failed`    | —                                      | —                           |

### Failure handling

- If PostgreSQL is unreachable, buffer events in memory (bounded to 10,000).
- If buffer fills, drop oldest events and log a warning — analytics is best-effort.
- On reconnect, flush the buffer.
- The sink never blocks the event bus or the coordinator's hot path.

---

## Step 3 — Backfill from existing BadgerDB audit trail

The audit trail (`audit/{unix-nano}-{eventID}` → `AuditEvent` proto) contains historical
events already persisted. A one-time backfill command reads these and inserts them into
PostgreSQL so that analytics covers data from before the sink was deployed.

```go
// internal/analytics/backfill.go

// Backfill reads all audit events from BadgerDB and inserts them into
// PostgreSQL, skipping any event IDs that already exist (idempotent).
func Backfill(ctx context.Context, store *persistence.Store, db *sql.DB) error { ... }
```

The backfill runs as a coordinator subcommand:

```
helion-coordinator analytics backfill --pg-dsn=postgres://...
```

**Note:** The audit trail's `AuditEvent` proto has slightly different fields than the
event bus `Event` struct (actor/target/detail vs. Data map). The backfill normalises
audit events into the same `events` table schema, mapping:
- `event_type` → `event_type`
- `target` → `job_id` or `node_id` (parsed from prefix)
- `actor` → stored in `data` JSONB
- `detail` → stored in `data` JSONB
- `occurred_at` → `timestamp`

---

## Step 4 — SQL views for analytical queries

Create views (and optionally materialised views refreshed on a schedule) for the
metrics the dashboard needs:

```sql
-- Hourly job throughput: how many jobs completed/failed per hour?
CREATE VIEW v_hourly_throughput AS
SELECT
    date_trunc('hour', completed_at) AS hour,
    final_status,
    COUNT(*)                         AS job_count,
    AVG(duration_ms)                 AS avg_duration_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95_duration_ms
FROM job_summary
WHERE completed_at IS NOT NULL
GROUP BY 1, 2;

-- Node reliability: which nodes fail the most?
CREATE VIEW v_node_reliability AS
SELECT
    node_id,
    address,
    jobs_completed,
    jobs_failed,
    ROUND(jobs_failed::numeric / NULLIF(jobs_completed + jobs_failed, 0) * 100, 2)
        AS failure_rate_pct,
    times_stale,
    times_revoked
FROM node_summary
ORDER BY failure_rate_pct DESC;

-- Retry effectiveness: do retries actually help?
CREATE VIEW v_retry_effectiveness AS
SELECT
    CASE WHEN attempts > 1 THEN 'retried' ELSE 'first_attempt' END AS category,
    final_status,
    COUNT(*) AS job_count,
    AVG(duration_ms) AS avg_duration_ms
FROM job_summary
WHERE final_status IN ('completed', 'failed')
GROUP BY 1, 2;

-- Queue wait time: how long from submitted to running?
CREATE VIEW v_queue_wait AS
SELECT
    date_trunc('hour', submitted_at) AS hour,
    AVG(EXTRACT(EPOCH FROM (started_at - submitted_at)) * 1000) AS avg_wait_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (started_at - submitted_at)) * 1000
    ) AS p95_wait_ms,
    COUNT(*) AS job_count
FROM job_summary
WHERE started_at IS NOT NULL
GROUP BY 1;

-- Workflow success rate
CREATE VIEW v_workflow_outcomes AS
SELECT
    event_type,
    date_trunc('day', timestamp) AS day,
    COUNT(*) AS count
FROM events
WHERE event_type IN ('workflow.completed', 'workflow.failed')
GROUP BY 1, 2;
```

---

## Step 5 — Analytics REST API

New handler group in `internal/api/` that queries PostgreSQL and returns JSON for the
analytics dashboard. These endpoints are read-only and authenticated with the same JWT.

```
GET /api/analytics/throughput?from=2026-04-01&to=2026-04-13&granularity=hour
GET /api/analytics/node-reliability
GET /api/analytics/retry-effectiveness
GET /api/analytics/queue-wait?from=...&to=...
GET /api/analytics/workflow-outcomes?from=...&to=...
GET /api/analytics/events?type=job.failed&limit=100&offset=0
```

Each endpoint maps to one of the SQL views above with optional time-range filtering.

---

## Step 6 — Analytics dashboard module

New lazy-loaded Angular module added to the existing dashboard:

```
dashboard/src/app/analytics/
├── analytics.module.ts
├── analytics-routing.module.ts
├── services/
│   └── analytics.service.ts          -- HttpClient calls to /api/analytics/*
├── components/
│   ├── throughput-chart/             -- line chart: jobs/hour, split by status
│   ├── node-reliability-table/       -- sortable table: node, completion, failure rate
│   ├── retry-effectiveness-chart/    -- bar chart: first-attempt vs retried outcomes
│   ├── queue-wait-chart/             -- line chart: p50/p95 wait time over time
│   └── workflow-outcomes-chart/      -- stacked bar: workflow success/failure per day
└── analytics-dashboard.component.ts  -- layout page with date-range picker
```

The existing operational dashboard (nodes, jobs, events) remains unchanged at `/`.
The analytics dashboard lives at `/analytics` with its own nav entry.

### Date range picker

All analytics views accept a `from`/`to` range. Default to "last 7 days."
The date range picker updates all charts simultaneously.

---

## Step 7 — Configuration and startup

### Coordinator config

```yaml
analytics:
  enabled: false                     # opt-in; no PostgreSQL required by default
  postgres_dsn: "postgres://helion:secret@localhost:5432/helion_analytics?sslmode=disable"
  batch_size: 100                    # events per flush
  flush_interval: "500ms"            # max time between flushes
  buffer_limit: 10000                # max in-memory buffer before dropping
```

When `analytics.enabled` is `true`, the coordinator:
1. Connects to PostgreSQL and runs pending migrations.
2. Starts the analytics `Sink` (subscribes to `"*"` on the event bus).
3. Registers the `/api/analytics/*` endpoints.

When `false`, none of the above happens — zero overhead, no PostgreSQL dependency.

### Docker Compose addition

```yaml
services:
  analytics-db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: helion_analytics
      POSTGRES_USER: helion
      POSTGRES_PASSWORD: secret
    ports:
      - "5432:5432"
    volumes:
      - analytics_pgdata:/var/lib/postgresql/data
```

---

## Step 8 — Documentation

Update the following docs:
- `docs/ARCHITECTURE.md` — add analytics pipeline section
- `docs/COMPONENTS.md` — add analytics sink and API descriptions
- `docs/persistence.md` — note dual-store design (BadgerDB operational + PostgreSQL analytical)
- `docs/dashboard.md` — add analytics module documentation

---

## Implementation order

| Step | Description                            | Depends on | Effort | Status |
|------|----------------------------------------|------------|--------|--------|
| 1    | PostgreSQL schema + migrations         | —          | Small  | Done   |
| 2    | Analytics sink (bus → PostgreSQL)       | Step 1     | Medium | Done   |
| 3    | Backfill from BadgerDB audit trail     | Step 1     | Small  | Done   |
| 4    | SQL views                              | Step 1     | Small  | Done   |
| 5    | Analytics REST API                     | Step 2, 4  | Medium | Done   |
| 6    | Analytics dashboard module             | Step 5     | Medium | Done   |
| 7    | Configuration and startup wiring       | Step 2     | Small  | Done   |
| 8    | Documentation                          | All        | Small  | Done   |

## Implementation notes

Deviations from the original plan spec:

- **Configuration:** The plan showed YAML (`analytics.enabled`, `postgres_dsn`, etc.).
  The implementation uses environment variables (`HELION_ANALYTICS_DSN`,
  `HELION_ANALYTICS_BATCH`, `HELION_ANALYTICS_FLUSH_MS`, `HELION_ANALYTICS_BUFFER`)
  to match the project convention — every other coordinator setting is an env var.

- **SQL driver:** The plan sketch used stdlib `*sql.DB`. The implementation uses
  `pgx/v5` directly, via a **connection pool** (`pgxpool.Pool`). A single
  `*pgx.Conn` is **not** safe for concurrent use — the sink writes and API
  handlers read in parallel, so a shared conn produced `"conn busy"` errors.

- **Migration location:** Migrations live in `internal/analytics/migrations/` as
  `go:embed`-ded SQL files, not a top-level `migrations/` directory. Keeps the
  schema colocated with the code that applies it.

- **Backfill CLI:** Implemented as a subcommand of the existing coordinator binary:
  `helion-coordinator analytics backfill [--pg-dsn=...] [--db-path=...]`. Reads
  flags or falls back to env vars. Runs migrations before backfilling so the
  target database can be empty. Opens BadgerDB via
  `cluster.NewBadgerJSONPersisterReadOnly` (uses BadgerDB's `WithReadOnly` +
  `WithBypassLockGuard`) so the backfill can coexist with other readers on
  the same directory. **Limitation:** cannot run against a BadgerDB that
  currently has an active writer (the coordinator) — the WAL must be clean.
  In practice backfills run during maintenance windows or after a fresh
  restart.

- **Workflow event emission:** During E2E testing we discovered
  `workflow.completed` / `workflow.failed` were defined in `internal/events/topics.go`
  but never actually published. Wired up in `WorkflowStore.OnJobCompleted` so
  the analytics sink (and any future subscriber) receives them. Attached via
  `workflows.SetEventBus(eventBus)` in coordinator startup.

## Security hardening

Added alongside the initial implementation to match the rest of the authenticated
API surface (jobs/nodes/metrics/audit):

| Control | Value | Source |
|---|---|---|
| JWT Bearer auth | Required | `authMiddleware` |
| Per-subject rate limit | 2 rps burst 30 → 429 | `analyticsQueryAllow` (mirrors `tokenIssueAllow`) |
| Time-range bounds | 365-day max; 400 on invalid / inverted / malformed | `parseTimeRange` |
| Pagination bounds | `limit` clamped to 1000, negative → default | `parseIntParam` |
| Audit log of queries | Every successful query recorded as `analytics.query` with actor + endpoint + range; rate-limited requests are **not** audited | `analyticsPreflight` + `audit.Logger` |
| Error masking | Generic `"internal error"`; pgx details server-side only | `writeError` |

The dashboard side inherits the existing dashboard security contract: in-memory
JWT only (never `localStorage`), auth-interceptor auto-logout on 401, Angular
auth-guard on `/analytics`, Nginx CSP on same-origin.

## Test coverage

End-to-end coverage lives in
[`dashboard/e2e/specs/analytics.spec.ts`](../../dashboard/e2e/specs/analytics.spec.ts)
(35 tests). Highlights:

**Dashboard UI:**
- page title/subtitle/nav link/icon
- default date range is last 7 days, both inputs populated
- loading spinner resolves
- error banner shown when API fails, hidden when healthy
- throughput / queue-wait / workflow-outcomes charts render after real jobs/workflows
- node-reliability table appears after jobs complete (resilient — retries a few submissions if the first lands pre-dispatch)
- retry-effectiveness cards render
- isolated: changing date range re-issues API calls with new `from`
- isolated: API 500 → error banner
- isolated: unauthenticated → `/login` redirect

**REST API (direct):**
- all 6 endpoints return 200 with `{ data }`
- 401 without Bearer
- 400 on inverted / >365-day / malformed ranges
- `limit` clamped to 1000 (events endpoint)
- `type=job.submitted` filter returns only matching rows
- `offset` produces disjoint pages
- JSONB `data` field round-trips as a base64 string that decodes to the
  original event payload (job_id included)
- submitted job's `job.submitted` event appears in `/api/analytics/events`
  (data consistency between operational submit and analytical read)
- analytics query produces an `analytics.query` audit record
- audit record's `actor` matches the JWT subject (not `"anonymous"`)
- rate limit buckets are isolated per JWT subject: draining the root
  token's bucket leaves a freshly-minted token unaffected
- backfill CLI subcommand is dispatched when invoked (verifies the binary
  wiring; full backfill coverage is via `TestBackfill_*` unit tests with
  mocks, since running against a live BadgerDB writer is blocked by the
  read-only WAL limitation)

**Isolated (destructive to shared page state):**
- navigating from /analytics back to /analytics re-issues API calls with
  new `from` timestamp
- 400 from inverted date inputs in the UI surfaces as the error banner
- 500 from analytics API surfaces as the error banner
- unauthenticated request to /analytics redirects to /login

**Destructive (runs last):**
- 150 parallel requests overflow the rate bucket → at least one 429 with
  the expected body

Unit coverage:
- `internal/analytics` — 88%+ (Go), migrations / sink / backfill with mock DB
- `handlers_analytics_test.go` — 27 tests covering rate-limit, time-range,
  limit clamp, audit recording
- `analytics-dashboard.component.spec.ts` — 25 tests (Angular Karma)
- `workflow_lifecycle_test.go` — 3 tests asserting `workflow.completed` /
  `workflow.failed` are published only on full terminal state

## What this does NOT include

- **Replacing BadgerDB** — BadgerDB remains the operational store. This is additive.
- **Real-time analytics** — The dashboard queries PostgreSQL on load/refresh, not via WebSocket. The existing operational dashboard already covers real-time.
- **Multi-tenant analytics** — Single analytics database. Tenant isolation is a deferred enhancement.
- **Grafana integration** — The Angular analytics module is the primary interface. Grafana can connect to the same PostgreSQL if desired, but we don't ship dashboards for it.
