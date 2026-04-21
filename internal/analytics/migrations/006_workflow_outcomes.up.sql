-- 006_workflow_outcomes.up.sql
--
-- Feature 40 — workflow-level analytics rollup.
--
-- Feature 28 shipped a unified analytics sink that indexes every
-- raw event in the `events` table, and a `v_workflow_outcomes`
-- VIEW (see 004_create_views.up.sql) that group-counts those
-- events by day + type. The view is fine for "how many workflows
-- succeeded yesterday?" but every workflow-level analytics query
-- the dashboard wants to show — "show me this pipeline's full
-- job list + duration + which jobs failed" — forces a JOIN
-- against the `events` JSONB payload, which is expensive and
-- surfaces un-indexed JSON key lookups on the hot path.
--
-- This migration adds a denormalised `workflow_outcomes` table
-- that the analytics sink upserts on every workflow.completed
-- and workflow.failed event. One row per workflow run, with:
--
--   - timing (started_at from the first job.submitted, completed
--     at from the terminal workflow event)
--   - shape (job_count, success_count, failed_count)
--   - resolution (status = 'completed' | 'failed', the failing
--     job name when present, whether ML-specific)
--   - lineage (owner_principal snapshotted at upsert time)
--
-- Retention follows the same cron the other feature-28 tables
-- use (HELION_ANALYTICS_RETENTION_DAYS). The `events` table
-- remains the forever-record for the individual job transitions
-- — workflow_outcomes is the operational-window rollup.

CREATE TABLE IF NOT EXISTS workflow_outcomes (
    -- Stable identifier across all job transitions of the same
    -- workflow run. Unique per run; re-submitting the workflow
    -- ID collides by design (same semantic entity, new run
    -- history goes into events table).
    workflow_id     TEXT        PRIMARY KEY,

    -- `completed` when every job reached a success terminal;
    -- `failed` when at least one job ended in FAILED / TIMEOUT
    -- / LOST and cascading failure ended the DAG.
    status          TEXT        NOT NULL,

    -- Completion timestamp — set to the workflow.{completed,
    -- failed} event timestamp, which the coordinator sources
    -- from time.Now().UTC() when the last job transitions.
    completed_at    TIMESTAMPTZ NOT NULL,

    -- Shape of the DAG. All three are non-negative INT; zero
    -- job_count is a submission that rejected before dispatch
    -- (the sink still upserts the row so the analytics dashboard
    -- can surface 0/0 "empty DAG" rejections).
    job_count       INT         NOT NULL,
    success_count   INT         NOT NULL,
    failed_count    INT         NOT NULL,

    -- Name of the job that caused failure, when status='failed'
    -- and the workflow engine can identify a single root cause.
    -- NULL on success and on multi-failure DAGs.
    failed_job      TEXT,

    -- Who submitted the workflow — principal ID resolved from
    -- the submitter's JWT / cert at submission time. Non-NULL
    -- except on legacy unowned workflows pre-feature-36.
    owner_principal TEXT,

    -- Free-form tags carried through from the submission. Used
    -- by the dashboard to filter by `task=image-classification`
    -- or `team=ml`. Empty JSONB when the submitter passed
    -- no tags.
    tags            JSONB       NOT NULL DEFAULT '{}'::JSONB
);

CREATE INDEX IF NOT EXISTS idx_workflow_outcomes_completed_at
    ON workflow_outcomes (completed_at DESC);

CREATE INDEX IF NOT EXISTS idx_workflow_outcomes_status
    ON workflow_outcomes (status);

-- idx_workflow_outcomes_owner lets the dashboard surface
-- "workflows run by <me> in the last 7 days" without a seq-scan
-- across the whole table. Partial index on non-NULL owners to
-- skip the legacy tail.
CREATE INDEX IF NOT EXISTS idx_workflow_outcomes_owner
    ON workflow_outcomes (owner_principal, completed_at DESC)
    WHERE owner_principal IS NOT NULL;
