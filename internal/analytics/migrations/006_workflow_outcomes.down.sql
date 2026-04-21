-- 006_workflow_outcomes.down.sql
--
-- Rolls back the feature-40 workflow_outcomes denormalised table.
-- v_workflow_outcomes (view-based rollup from feature 28) is
-- untouched; the dashboard will fall back to that VIEW until the
-- sink is re-migrated up.

DROP INDEX IF EXISTS idx_workflow_outcomes_owner;
DROP INDEX IF EXISTS idx_workflow_outcomes_status;
DROP INDEX IF EXISTS idx_workflow_outcomes_completed_at;

DROP TABLE IF EXISTS workflow_outcomes;
