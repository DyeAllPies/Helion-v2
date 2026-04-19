# Feature: Unified analytics sink — capture every interesting event

**Priority:** P2
**Status:** Pending
**Affected files:**
`internal/analytics/sink.go` (new event-type switch cases),
`internal/analytics/migrations/` (new tables + views),
`internal/api/handlers_analytics*.go` (new query endpoints),
`internal/events/events.go` (a handful of new event constructors
for surfaces that don't yet publish),
`internal/cluster/` + `internal/api/` (publish where missing),
`dashboard/src/app/features/analytics/` (new panels + tabs),
`docs/persistence.md` (retention tiers),
`docs/ARCHITECTURE.md` + `docs/SECURITY.md` (surface notes).

## Problem

The analytics PostgreSQL store is the right place to ask "what
has this cluster been doing?", but today it only answers a slice
of the question. The coordinator publishes 12+ event types onto
the in-process bus; the analytics sink
([`internal/analytics/sink.go`](../../internal/analytics/sink.go))
consumes exactly 8:

**Currently sunk into analytics (complete list):**

- `job.submitted`
- `job.transition`
- `job.completed`
- `job.failed`
- `job.retrying`
- `node.registered`
- `node.stale`
- `node.revoked`

**Already published by the coordinator but silently dropped by
the sink:**

- `workflow.completed` — we have no workflow-level success/failure
  rollups over time despite the data being emitted.
- `workflow.failed` — same.
- `job.unschedulable` — the scheduler emits this when a job's
  `node_selector` matches no live node. High-value signal for
  capacity planning; currently nowhere in the dashboard.
- `ml.resolve_failed` — artifact staging could not resolve a
  `from: <upstream>.<name>` reference. Diagnosing ML pipeline
  breakage today requires tailing logs.
- `dataset.registered`, `dataset.deleted`,
  `model.registered`, `model.deleted` — every registry mutation
  lands in audit but not analytics, so there's no "growth of
  the registry over time" panel.

**Not published anywhere** (the coordinator would have to emit
them):

- Submission history per operator (promoted out of feature 22
  "Submission history / re-run log" — the deferred item that
  sparked this spec).
- Auth events — successful login, token mint, 401 reject, 429
  throttle. Audit log has them; analytics doesn't, so there's
  no "logins per hour" or "throttle-rate" panel to feed incident
  response.
- Artifact-store activity — bytes uploaded / downloaded, SHA-256
  verification failures (feature 12's hash check).
- Service probe pass→fail / fail→pass transitions (feature 17).
- Resource-usage samples (heartbeat already reports CPU + memory
  per node; analytics doesn't persist a time-series).

Every one of these would answer a question an operator asks
during routine use of the dashboard. Moving them into the same
PostgreSQL store makes the Analytics tab the single place to
ask "what happened" — the unified sink the prompt calls for.

## Current state

### The write path (unchanged shape, just more consumers)

```
  coordinator → events.Bus.Publish → analytics.Sink.handle → PG batch flush
  coordinator → audit.Logger.Log   → BadgerDB append
```

The existing sink already does batched inserts every 200 ms
(`HELION_ANALYTICS_FLUSH_MS`, default documented in the e2e
compose). We piggy-back on the same batch — no new per-event
writes, no new flush paths, no new back-pressure surface.

### Where to draw the line between audit and analytics

Both stores will carry similar event streams after this lands.
The distinction that's in the codebase today (but not explicitly
written down):

| Store | Retention | Purpose | Query shape |
|---|---|---|---|
| **Audit log** (BadgerDB) | forever (append-only) | accountability trail; security incident response; compliance | per-key lookups; `Scan(prefix)` |
| **Analytics** (PostgreSQL) | operational window (e.g. 30-90 days, configurable) | time-series aggregation; dashboards; capacity planning | `GROUP BY date_trunc` + percentiles |

Every audit event continues to land in BadgerDB with its raw
`detail` string. The analytics sink's job is **complementary**:
digest the same events into time-series-friendly shape
(numeric columns, indexed timestamps) for dashboards. Deleting
the oldest analytics rows at the retention edge never
compromises the audit trail.

New doc section in
[`docs/persistence.md`](../../docs/persistence.md) makes the
two-tier contract explicit so a reviewer can tell which store to
hit for a given question.

## Design

### 1. Wire up events the coordinator already publishes

Additive switch cases in `sink.go`:

```go
case "workflow.completed": err = s.upsertWorkflowOutcome(ctx, tx, evt, "completed")
case "workflow.failed":    err = s.upsertWorkflowOutcome(ctx, tx, evt, "failed")
case "job.unschedulable":  err = s.upsertUnschedulable(ctx, tx, evt)
case "ml.resolve_failed":  err = s.upsertMLResolveFailed(ctx, tx, evt)
case "dataset.registered": err = s.upsertRegistryMutation(ctx, tx, evt, "dataset", "registered")
case "dataset.deleted":    err = s.upsertRegistryMutation(ctx, tx, evt, "dataset", "deleted")
case "model.registered":   err = s.upsertRegistryMutation(ctx, tx, evt, "model",   "registered")
case "model.deleted":      err = s.upsertRegistryMutation(ctx, tx, evt, "model",   "deleted")
```

Each handler is a few lines — the shape matches
`upsertNodeRegistered` already in the file.

### 2. New tables (one migration)

```sql
-- 005_unified_sink.up.sql

CREATE TABLE IF NOT EXISTS submission_history (
  id              UUID        PRIMARY KEY,
  submitted_at    TIMESTAMPTZ NOT NULL,
  actor           TEXT        NOT NULL,     -- JWT subject
  operator_cn     TEXT,                     -- from feature 27 if present
  source          TEXT        NOT NULL,     -- "dashboard" | "cli" | "ci"
  kind            TEXT        NOT NULL,     -- "job" | "workflow"
  resource_id     TEXT        NOT NULL,
  dry_run         BOOLEAN     NOT NULL DEFAULT FALSE,
  accepted        BOOLEAN     NOT NULL,     -- false = 400 at validator
  reject_reason   TEXT,
  user_agent      TEXT
);
CREATE INDEX IF NOT EXISTS submission_history_at_idx ON submission_history (submitted_at DESC);
CREATE INDEX IF NOT EXISTS submission_history_actor_idx ON submission_history (actor, submitted_at DESC);

CREATE TABLE IF NOT EXISTS registry_mutations (
  occurred_at     TIMESTAMPTZ NOT NULL,
  kind            TEXT        NOT NULL,     -- "dataset" | "model"
  action          TEXT        NOT NULL,     -- "registered" | "deleted"
  name            TEXT        NOT NULL,
  version         TEXT        NOT NULL,
  uri             TEXT,
  actor           TEXT,
  size_bytes      BIGINT,
  PRIMARY KEY (kind, name, version, occurred_at)
);

CREATE TABLE IF NOT EXISTS unschedulable_events (
  occurred_at     TIMESTAMPTZ NOT NULL,
  job_id          TEXT        NOT NULL,
  selector        JSONB       NOT NULL,
  reason          TEXT        NOT NULL,
  PRIMARY KEY (job_id, occurred_at)
);

CREATE TABLE IF NOT EXISTS auth_events (
  occurred_at     TIMESTAMPTZ NOT NULL,
  event_type      TEXT        NOT NULL,     -- "login" | "token_mint" | "auth_fail" | "rate_limit"
  actor           TEXT,
  remote_ip       INET,
  user_agent      TEXT,
  reason          TEXT                      -- populated on failure
);
CREATE INDEX IF NOT EXISTS auth_events_at_idx ON auth_events (occurred_at DESC);
CREATE INDEX IF NOT EXISTS auth_events_fail_idx ON auth_events (event_type, occurred_at DESC)
  WHERE event_type IN ('auth_fail', 'rate_limit');

CREATE TABLE IF NOT EXISTS artifact_transfers (
  occurred_at     TIMESTAMPTZ NOT NULL,
  direction       TEXT        NOT NULL,     -- "upload" | "download"
  job_id          TEXT,
  uri             TEXT        NOT NULL,
  bytes           BIGINT      NOT NULL,
  sha256_ok       BOOLEAN,                  -- NULL = not verified; true/false = verified
  duration_ms     INT,
  PRIMARY KEY (occurred_at, job_id, direction, uri)
);

CREATE TABLE IF NOT EXISTS service_probe_events (
  occurred_at     TIMESTAMPTZ NOT NULL,
  job_id          TEXT        NOT NULL,
  new_state       TEXT        NOT NULL,     -- "ready" | "unhealthy" | "gone"
  consecutive_fails INT,
  PRIMARY KEY (job_id, occurred_at)
);
```

All tables are retention-capped via a cron task that prunes rows
older than `HELION_ANALYTICS_RETENTION_DAYS` (default 60).
Tuning this is an operator knob — audit trail is unaffected.

### 3. Publish the events that don't exist yet

The four new publishers (in order of effort):

1. **Submission history** — `handleSubmitJob` and
   `handleSubmitWorkflow` already know `actor`, the resource ID,
   the `dry_run` flag, and whether the request passed validation.
   Emit `submission.recorded` on every outcome (accepted OR
   rejected) after feature 24's dry-run branch. Includes
   `source: "dashboard"` when `User-Agent` matches the
   dashboard build (feature 22) — same discriminator the spec
   mentions.
2. **Auth events** — `authMiddleware` emits `auth.ok` on pass
   and `auth.fail` on reject (reason = `missing_token` |
   `invalid_signature` | `expired` | `revoked`).
   `rateLimitMiddleware` emits `auth.rate_limit` on 429.
   `handleMintToken` emits `auth.token_mint` with the minted
   TTL + role.
3. **Artifact transfers** — `internal/artifacts/`'s `Put` and
   `GetAndVerifyTo` wrap their existing calls with an event
   emit on completion, carrying bytes + duration + SHA verify
   result.
4. **Service probe transitions** — `internal/nodeserver/service_prober.go`
   already owns the state machine; add an `eventBus.Publish` on
   each edge.

Each publisher is guarded by `if s.eventBus != nil` (matches
existing style in the codebase — e.g.
`internal/cluster/job_transition.go`) so test builds that don't
wire a bus don't break.

### 4. New dashboard panels

Five new panels land in the Analytics tab, each reusing the
existing quick-range + live-polling + zero-fill scaffolding
(feature that's already shipped):

| Panel | Data source | Default range | Bucket |
|---|---|---|---|
| **Submission history** | `submission_history` | LAST 24 H | hour |
| **Registry growth** | `registry_mutations` | LAST 7 D | day |
| **Unschedulable jobs** | `unschedulable_events` | LAST 1 H | minute |
| **Auth events** (logins + fails + rate-limit) | `auth_events` | LAST 1 H | minute |
| **Artifact throughput** (bytes/s) | `artifact_transfers` | LAST 10 MIN | second |

Each panel uses the same zero-fill + chart config shared by the
throughput + queue-wait panels (feature we shipped in the most
recent analytics pass). No new chart framework.

The three dashboard tabs — **OVERVIEW** (existing throughput +
queue-wait), **ACTIVITY** (submission history + registry growth
+ auth events), **DIAGNOSTICS** (unschedulable + artifact
throughput + service probe) — keep the initial view digestible
and let operators deep-link to the one they care about via
`/analytics/activity`, `/analytics/diagnostics`.

### 5. Retention + privacy

Two policy knobs:

- `HELION_ANALYTICS_RETENTION_DAYS` — default 60. Rows older
  are DELETEd by a per-table cron (one `DELETE FROM … WHERE
  occurred_at < NOW() - INTERVAL '$1 days'` per table).
- `HELION_ANALYTICS_PII_MODE` — `off` (default) | `hash_actor`.
  In `hash_actor` mode the `actor` column is stored as
  `SHA256(HELION_ANALYTICS_PII_SALT || actor)` instead of the
  raw JWT subject. Operators still get per-subject trends (same
  actor = same hash) without the subject leaking into an
  analytics dump.

**Command-string privacy** — the submission history table
stores `resource_id` (a ULID, not sensitive) but NOT the full
command + args + env. A forensic reviewer who needs the full
submission goes to the audit log via `resource_id`. This keeps
any secret env values that slipped through feature 26's filter
out of the analytics store — defense in depth.

## Security plan

| Threat | Control |
|---|---|
| Secret values leak into analytics tables | `submission_history` stores the ULID, not the body. Secret redaction (feature 26) applies to the audit store too. Analytics dump ≠ secret exposure. |
| Auth-event table becomes a login-attempt oracle for password-spray | No password auth exists (JWT only). `auth_fail` rows carry `reason` not the token content. Rate-limit events carry `actor` if known, else `remote_ip` only. |
| PII leakage via `actor` = user email in the JWT sub | `HELION_ANALYTICS_PII_MODE=hash_actor` + a per-install salt. Dashboards group by hash; ops gets per-subject trends without identity. |
| Artifact-transfer table records sensitive URIs (e.g. S3 presigned links) | `uri` column stores the canonical artifact reference (`artifacts://<hash>`), not the presigned URL used for the actual byte transfer. |
| Massive write pressure from a noisy test suite flooding the bus | Same 200 ms batch + 10k buffer as today. A test flood fills the in-memory buffer then drops events with a metric — we never block the publisher. Existing back-pressure handling. |
| Long query against `submission_history` starves production queries | Per-subject rate limit already applies on every `/api/analytics/*` endpoint (0.5 rps burst 10). New panels reuse the same limiter. |

New `SECURITY.md` row: **"Analytics store is retention-bounded
(~60 days). Audit store is the forever-record. A compromised
analytics database does NOT compromise the audit trail. A
compromised audit store is treated as a full cluster
compromise."**

## Implementation order

Each step is an independent PR; 2-5 can land in any order once
1 is in.

1. **Migration 005** + `submission_history` table + retention
   cron. Smallest, lands first. (Tests: the retention cron.)
2. **Submission-history publisher + panel.** Piggybacks on
   feature 24's dry-run branch if already landed, degrades to
   real-submits-only if not. Delivers the feature-22 deferred
   item on its own.
3. **Workflow-outcome + registry-mutation + unschedulable sinks.**
   Pure additive — coordinator already publishes these; sink
   cases + a dashboard panel each.
4. **Auth-event publisher + panel.** New publisher in
   `authMiddleware` + `rateLimitMiddleware`. Panel shape:
   stacked bar of {login, token_mint, auth_fail, rate_limit}
   over time.
5. **Artifact-transfer publisher + panel.** Small wrapper on
   `Put` / `GetAndVerifyTo`. Bytes-per-second panel + verify-
   failure alert.
6. **Service-probe-transition publisher + panel.** Smallest
   ceremony.
7. **Retention + PII knobs.** Cron + `hash_actor` mode. Ships
   last so operators can tune once everything is landing.

## Tests

Each new sink case gets a table-driven test in
`internal/analytics/sink_test.go`:

- `TestSink_WorkflowCompleted_Upserts` — publish
  `workflow.completed`, flush, assert row exists in
  `workflow_outcomes` with correct workflow_id + day bucket.
- Mirror tests for every new event type.
- `TestSink_UnknownEventType_NoOp` — publish
  `totally.made.up` → sink does not crash, no row added. (The
  switch default must be `nil`, never `panic`.)

New backend handler tests:

- `TestHandleAnalyticsSubmissionHistory_Ordered` — three rows
  at different times → response in descending `submitted_at`.
- `TestHandleAnalyticsAuthEvents_FilterByType` —
  `?event_type=auth_fail` returns only fail rows.

Retention cron:

- `TestRetentionCron_DeletesOldRows` — insert rows at `now`,
  `now-30d`, `now-90d`; run cron with retention=60d; only the
  90d row deleted.

Privacy mode:

- `TestPIIMode_HashActor_ReplacesSubject` — `hash_actor`
  enabled; publish an event with `actor=alice@ops`; row has
  `SHA256(salt||"alice@ops")`, not `alice@ops`.

Dashboard:

- Reuse the existing analytics component spec scaffolding; add
  one spec per new panel asserting data → labels shape, zero-
  fill invariants, and the API call carries the right params.

Playwright:

- `analytics-activity-tab.spec.ts` — gated local-only like the
  MNIST walkthrough. Submit a workflow, visit `/analytics/activity`,
  assert a new row appears on the submission-history panel
  within the live-poll window.

## Acceptance criteria

1. Every row in the "currently dropped" list above now lands in
   analytics after the relevant workflow completes on a demo
   cluster.
2. `GET /api/analytics/submission-history?from=…&to=…` returns
   rows; new panel on the dashboard renders them ordered by
   time descending.
3. `GET /api/analytics/auth-events?event_type=auth_fail`
   returns only failed-auth rows; panel renders.
4. A job targeting a label no node carries (`runtime: unicorn`)
   triggers an `unschedulable_events` row visible on the
   Diagnostics tab within one poll cycle.
5. Audit log for the same events is unchanged — retention stays
   "forever", writes stay in BadgerDB.
6. With `HELION_ANALYTICS_PII_MODE=hash_actor` set, a dump of
   `submission_history.actor` shows hashes, never raw JWT
   subjects.
7. With `HELION_ANALYTICS_RETENTION_DAYS=1` + the cron running,
   yesterday's rows are gone this morning; audit still has
   them.

## Deferred (out of scope for this spec)

- **GPU time-series.** Node heartbeats already carry GPU
  utilisation; persisting a rolled-up sample-per-minute would
  need its own storage shape. Valuable but a bigger slice.
- **Cost attribution.** Requires a pricing model — separate
  project.
- **Per-job log ingestion into analytics.** The log store is
  already a separate persistence tier; indexing logs into PG
  would duplicate data. Use the existing job-detail log view.
- **Federated analytics across clusters.** Single-cluster only
  for now.
- **Alerting on thresholds.** "Notify me when auth_fail rate >
  X/min" is a monitoring concern; Prometheus + Alertmanager
  pair better than a hand-rolled UI alert system.
