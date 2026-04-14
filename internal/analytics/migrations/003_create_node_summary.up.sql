-- 003_create_node_summary.up.sql
--
-- Materialised snapshot: one row per node, tracking registration history
-- and job outcome tallies for the node reliability dashboard.

CREATE TABLE IF NOT EXISTS node_summary (
    node_id         TEXT        PRIMARY KEY,
    address         TEXT,
    first_seen      TIMESTAMPTZ,
    last_seen       TIMESTAMPTZ,
    times_stale     INT         DEFAULT 0,
    times_revoked   INT         DEFAULT 0,
    jobs_completed  INT         DEFAULT 0,
    jobs_failed     INT         DEFAULT 0
);
