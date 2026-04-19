-- 005_unified_sink.up.sql
--
-- Feature 28 — unified analytics sink.
--
-- Adds per-event-family tables for the data the coordinator already
-- emits on the event bus but the sink does not persist today, plus
-- new tables for event families the sink WILL emit once the matching
-- publishers land (submission history, auth events, artifact
-- transfers, service probe transitions, job logs).
--
-- Design invariants shared by every new table:
--
--   - timestamps are TIMESTAMPTZ. Every retention sweep + every
--     dashboard range-query filters on the time column. Never store
--     naïve timestamps.
--   - primary keys are composite where a single event can recur for
--     the same subject (e.g., unschedulable_events on the same job).
--     Strictly unique-per-row primary keys (PRIMARY KEY (id)) are
--     reserved for tables where the event itself has no natural key.
--   - no indexes on columns that are not part of a shipped query;
--     adding them later is cheap, removing them later is free only
--     in wall-clock terms.
--
-- Retention: each of these tables is subject to the retention cron
-- (`HELION_ANALYTICS_RETENTION_DAYS`, default 60). The audit log in
-- BadgerDB is the forever-record; analytics is the operational
-- window. See docs/persistence.md for the tier contract.
--
-- PII: the `actor` column is stored verbatim by default. When
-- `HELION_ANALYTICS_PII_MODE=hash_actor` is set, the sink writes
-- `sha256(HELION_ANALYTICS_PII_SALT || raw_actor)` instead. The
-- schema does not encode the mode — the sink decides at write time
-- — so toggling modes mid-deployment gives a mixed table, which the
-- operator must reconcile by truncating or letting retention age it
-- out. See docs/ARCHITECTURE.md for the tradeoff.

-- ── submission_history ──────────────────────────────────────────────
--
-- One row per POST /jobs and POST /workflows — success, rejection,
-- AND dry-run — with just enough metadata to answer "what did
-- <actor> submit in the last week?" without exposing the submit
-- body. The resource_id ties back to the full audit event when a
-- forensic reviewer needs the plaintext.

CREATE TABLE IF NOT EXISTS submission_history (
    id              UUID        PRIMARY KEY,
    submitted_at    TIMESTAMPTZ NOT NULL,
    actor           TEXT        NOT NULL,     -- JWT subject or 'anonymous'
    operator_cn     TEXT,                     -- feature 27 client-cert CN when present
    source          TEXT        NOT NULL,     -- 'dashboard' | 'cli' | 'ci' | 'unknown'
    kind            TEXT        NOT NULL,     -- 'job' | 'workflow'
    resource_id     TEXT        NOT NULL,     -- job_id or workflow_id
    dry_run         BOOLEAN     NOT NULL DEFAULT FALSE,
    accepted        BOOLEAN     NOT NULL,     -- false = 400 at validator
    reject_reason   TEXT,                     -- short server-side reason; NULL on accept
    user_agent      TEXT                      -- truncated; for source attribution
);
CREATE INDEX IF NOT EXISTS submission_history_at_idx    ON submission_history (submitted_at DESC);
CREATE INDEX IF NOT EXISTS submission_history_actor_idx ON submission_history (actor, submitted_at DESC);

-- ── registry_mutations ──────────────────────────────────────────────
--
-- Dataset + model register/delete events. Audit log already records
-- these per-event; this table is the time-series form so the
-- dashboard can render "growth of registry over time" without
-- replaying every audit key.

CREATE TABLE IF NOT EXISTS registry_mutations (
    occurred_at     TIMESTAMPTZ NOT NULL,
    kind            TEXT        NOT NULL,     -- 'dataset' | 'model'
    action          TEXT        NOT NULL,     -- 'registered' | 'deleted'
    name            TEXT        NOT NULL,
    version         TEXT        NOT NULL,
    uri             TEXT,
    actor           TEXT,
    size_bytes      BIGINT,
    PRIMARY KEY (kind, name, version, action, occurred_at)
);
CREATE INDEX IF NOT EXISTS registry_mutations_at_idx ON registry_mutations (occurred_at DESC);

-- ── unschedulable_events ────────────────────────────────────────────
--
-- Scheduler fires job.unschedulable when a submit's node_selector
-- matches no live node. High-value signal for capacity planning.

CREATE TABLE IF NOT EXISTS unschedulable_events (
    occurred_at     TIMESTAMPTZ NOT NULL,
    job_id          TEXT        NOT NULL,
    selector        JSONB       NOT NULL,     -- verbatim map[string]string
    reason          TEXT        NOT NULL,
    PRIMARY KEY (job_id, occurred_at)
);
CREATE INDEX IF NOT EXISTS unschedulable_events_at_idx ON unschedulable_events (occurred_at DESC);

-- ── auth_events ─────────────────────────────────────────────────────
--
-- Feeds a "logins + token-mints + auth-failures + rate-limits over
-- time" panel. Populated by authMiddleware (feature 4+28),
-- rateLimitMiddleware (429s), and handleIssueToken (token_mint).
--
-- remote_ip is INET for efficient range lookups; note PostgreSQL's
-- INET accepts both IPv4 and IPv6. On ingest we cast from string;
-- a malformed IP drops the value (NULL column) rather than failing
-- the whole row.

CREATE TABLE IF NOT EXISTS auth_events (
    occurred_at     TIMESTAMPTZ NOT NULL,
    event_type      TEXT        NOT NULL,     -- 'login' | 'token_mint' | 'auth_fail' | 'rate_limit'
    actor           TEXT,
    remote_ip       INET,
    user_agent      TEXT,
    reason          TEXT                      -- 'missing_token'/'invalid_signature'/'expired'/'revoked' on fail; 'admin:<actor>' on token_mint
);
CREATE INDEX IF NOT EXISTS auth_events_at_idx   ON auth_events (occurred_at DESC);
CREATE INDEX IF NOT EXISTS auth_events_fail_idx ON auth_events (event_type, occurred_at DESC)
    WHERE event_type IN ('auth_fail', 'rate_limit');

-- ── artifact_transfers ──────────────────────────────────────────────
--
-- Bytes-per-second / SHA-verification-failure telemetry for the
-- artifact store. Emitted by the Store's Put + GetAndVerifyTo call
-- sites (wrapped at the caller, not in the Store interface itself —
-- see feature 28 spec §3).
--
-- uri is the canonical reference (e.g., 'artifacts://<hash>' or
-- 's3://bucket/path'), never an S3 presigned URL — those carry
-- capability and belong in the audit log only if at all.

CREATE TABLE IF NOT EXISTS artifact_transfers (
    occurred_at     TIMESTAMPTZ NOT NULL,
    direction       TEXT        NOT NULL,     -- 'upload' | 'download'
    job_id          TEXT,
    uri             TEXT        NOT NULL,
    bytes           BIGINT      NOT NULL,
    sha256_ok       BOOLEAN,                  -- NULL = not verified; true/false = verified result
    duration_ms     INT,
    PRIMARY KEY (occurred_at, job_id, direction, uri)
);
CREATE INDEX IF NOT EXISTS artifact_transfers_at_idx ON artifact_transfers (occurred_at DESC);

-- ── service_probe_events ────────────────────────────────────────────
--
-- Edge-triggered inference-service readiness transitions (feature
-- 17). We already emit one audit row per transition; this mirror
-- gives the dashboard a time-series form.

CREATE TABLE IF NOT EXISTS service_probe_events (
    occurred_at       TIMESTAMPTZ NOT NULL,
    job_id            TEXT        NOT NULL,
    new_state         TEXT        NOT NULL,   -- 'ready' | 'unhealthy' | 'gone'
    consecutive_fails INT,
    PRIMARY KEY (job_id, occurred_at)
);
CREATE INDEX IF NOT EXISTS service_probe_events_at_idx ON service_probe_events (occurred_at DESC);

-- ── job_log_entries ─────────────────────────────────────────────────
--
-- Feature 28 + user request: sink job stdout/stderr chunks into
-- PostgreSQL so the operational window can eventually free
-- BadgerDB's log storage. The write path dual-sinks (BadgerDB stays
-- the read path for now); once this store has a full retention
-- window of coverage, a follow-up slice can switch reads over and
-- drop the Badger TTL.
--
-- data is TEXT, not BYTEA — job stdout/stderr today is UTF-8 text
-- (runtime enforces via pipe reader). Storing as TEXT lets us apply
-- `ILIKE` filters in future "search my logs" endpoints without a
-- pg_strom-style extension.
--
-- Composite PK is (job_id, seq) — seq is the in-job line number
-- already carried by logstore.LogEntry. A second ingest of the same
-- (job_id, seq) is idempotent via ON CONFLICT DO NOTHING at sink
-- time; we never overwrite a captured line.

CREATE TABLE IF NOT EXISTS job_log_entries (
    occurred_at     TIMESTAMPTZ NOT NULL,
    job_id          TEXT        NOT NULL,
    seq             BIGINT      NOT NULL,
    data            TEXT        NOT NULL,
    PRIMARY KEY (job_id, seq)
);
CREATE INDEX IF NOT EXISTS job_log_entries_at_idx ON job_log_entries (occurred_at DESC);
CREATE INDEX IF NOT EXISTS job_log_entries_job_idx ON job_log_entries (job_id, seq);
