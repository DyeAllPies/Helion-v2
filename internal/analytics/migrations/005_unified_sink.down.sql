-- 005_unified_sink.down.sql
--
-- Drop tables added by 005 in reverse dependency order. None of the
-- feature 28 tables reference each other (no FK constraints), so
-- ordering is only for human readability; PG will drop fine in any
-- order.

DROP TABLE IF EXISTS job_log_entries;
DROP TABLE IF EXISTS service_probe_events;
DROP TABLE IF EXISTS artifact_transfers;
DROP TABLE IF EXISTS auth_events;
DROP TABLE IF EXISTS unschedulable_events;
DROP TABLE IF EXISTS registry_mutations;
DROP TABLE IF EXISTS submission_history;
