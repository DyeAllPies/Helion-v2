-- 002_create_job_summary.up.sql
--
-- Materialised snapshot: one row per job, upserted on each job-related event.
-- Rebuilt entirely from events — analytics is self-contained, never reads
-- from BadgerDB at query time.

CREATE TABLE IF NOT EXISTS job_summary (
    job_id          TEXT        PRIMARY KEY,
    command         TEXT,
    priority        INT,
    workflow_id     TEXT,
    node_id         TEXT,
    final_status    TEXT,
    submitted_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    duration_ms     BIGINT,
    attempts        INT         DEFAULT 1,
    last_error      TEXT,
    last_exit_code  INT
);

CREATE INDEX idx_job_summary_status   ON job_summary (final_status);
CREATE INDEX idx_job_summary_node     ON job_summary (node_id)     WHERE node_id IS NOT NULL;
CREATE INDEX idx_job_summary_workflow ON job_summary (workflow_id)  WHERE workflow_id IS NOT NULL;
CREATE INDEX idx_job_summary_submitted ON job_summary (submitted_at);
