-- 007_workflow_outcomes_duration.down.sql
--
-- Rolls back the feature-40c started_at + duration_ms columns.
-- The workflow_outcomes table from migration 006 stays
-- populated but loses the timing columns; the ml-runs endpoint
-- handles their absence by returning nil-valued fields.

ALTER TABLE workflow_outcomes
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS started_at;
