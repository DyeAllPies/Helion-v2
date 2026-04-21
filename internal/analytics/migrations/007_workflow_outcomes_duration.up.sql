-- 007_workflow_outcomes_duration.up.sql
--
-- Feature 40c — adds a nullable `duration_ms` column + a
-- matching `started_at` column to the feature-40
-- workflow_outcomes rollup. Both were deferred from migration
-- 006 because the coordinator's workflow lifecycle didn't yet
-- thread the workflow's StartedAt into the event payload; now
-- it does (see internal/cluster/workflow_lifecycle.go — feature
-- 40c snapshot block).
--
-- Nullable on purpose:
--
--   - `started_at` is zero for workflows that were rejected at
--     submission before the dispatcher assigned a start
--     timestamp. Writing NULL keeps "never ran" distinguishable
--     from "ran for 0 ms".
--   - `duration_ms` is NULL when either endpoint is zero, when
--     finished_at precedes started_at (which the event
--     constructor guards against anyway), or when the legacy
--     WorkflowCompleted/WorkflowFailed constructors were used.
--     Dashboards should treat NULL as "unknown" rather than as
--     zero.
--
-- No index on duration_ms by default — "sort by duration" is a
-- feature-40d dashboard-only query that the default retention
-- (60 days) keeps bounded enough to full-scan. Adding the index
-- later is cheap.

ALTER TABLE workflow_outcomes
    ADD COLUMN IF NOT EXISTS started_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS duration_ms BIGINT;
