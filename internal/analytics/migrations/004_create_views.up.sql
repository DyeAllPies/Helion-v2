-- 004_create_views.up.sql
--
-- Analytical views consumed by the /api/analytics/* endpoints.
-- These are plain views (not materialised) so they always reflect the
-- latest data without a refresh schedule.

-- Hourly job throughput: how many jobs completed/failed per hour?
CREATE OR REPLACE VIEW v_hourly_throughput AS
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
CREATE OR REPLACE VIEW v_node_reliability AS
SELECT
    node_id,
    address,
    jobs_completed,
    jobs_failed,
    ROUND(
        jobs_failed::numeric / NULLIF(jobs_completed + jobs_failed, 0) * 100, 2
    ) AS failure_rate_pct,
    times_stale,
    times_revoked
FROM node_summary
ORDER BY failure_rate_pct DESC;

-- Retry effectiveness: do retries actually help?
CREATE OR REPLACE VIEW v_retry_effectiveness AS
SELECT
    CASE WHEN attempts > 1 THEN 'retried' ELSE 'first_attempt' END AS category,
    final_status,
    COUNT(*)            AS job_count,
    AVG(duration_ms)    AS avg_duration_ms
FROM job_summary
WHERE final_status IN ('completed', 'failed')
GROUP BY 1, 2;

-- Queue wait time: how long from submitted to running?
CREATE OR REPLACE VIEW v_queue_wait AS
SELECT
    date_trunc('hour', submitted_at) AS hour,
    AVG(EXTRACT(EPOCH FROM (started_at - submitted_at)) * 1000)  AS avg_wait_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (started_at - submitted_at)) * 1000
    ) AS p95_wait_ms,
    COUNT(*) AS job_count
FROM job_summary
WHERE started_at IS NOT NULL
GROUP BY 1;

-- Workflow outcomes by day.
CREATE OR REPLACE VIEW v_workflow_outcomes AS
SELECT
    event_type,
    date_trunc('day', timestamp) AS day,
    COUNT(*) AS count
FROM events
WHERE event_type IN ('workflow.completed', 'workflow.failed')
GROUP BY 1, 2;
