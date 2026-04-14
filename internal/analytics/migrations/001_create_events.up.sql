-- 001_create_events.up.sql
--
-- Core analytics fact table: one immutable row per event emitted by the
-- coordinator.  The raw JSONB payload is stored alongside denormalised
-- columns that the dashboard queries filter/group on, so the most common
-- queries hit indexes instead of scanning JSONB.

CREATE TABLE IF NOT EXISTS events (
    id          UUID        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    timestamp   TIMESTAMPTZ NOT NULL,
    data        JSONB       NOT NULL DEFAULT '{}',

    -- Denormalised from data for fast filtered queries.
    job_id      TEXT,
    node_id     TEXT,
    workflow_id TEXT
);

-- Type filter: "show me all job.failed events in the last hour."
CREATE INDEX idx_events_type ON events (event_type);

-- Time-range scans: every dashboard query filters by timestamp.
CREATE INDEX idx_events_timestamp ON events (timestamp);

-- Per-entity lookups (partial indexes — only index non-NULL rows).
CREATE INDEX idx_events_job      ON events (job_id)      WHERE job_id IS NOT NULL;
CREATE INDEX idx_events_node     ON events (node_id)     WHERE node_id IS NOT NULL;
CREATE INDEX idx_events_workflow ON events (workflow_id)  WHERE workflow_id IS NOT NULL;
