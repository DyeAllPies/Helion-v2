## Deferred: GPU time-series (sample-per-minute rollup)

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 28 — unified analytics sink](../implemented/28-analytics-unified-sink.md)

## Context

Node heartbeats already carry per-GPU utilisation + memory figures
(feature 15). The analytics sink records them under the raw
`events` table only; there is no rolled-up `gpu_samples` time-series
the dashboard can query for "show me each GPU's utilisation over
the last hour as a line chart."

A dedicated rollup would let the Analytics tab add a "GPU
utilisation" panel without scanning the full events log, and
would feed capacity-planning dashboards (when do we saturate?).

## Why deferred

- The write-path data already exists in `events`, so the current
  feature-28 retention cron covers it. An operator running a query
  can aggregate at read time — slower, but no schema churn today.
- A proper rollup needs a storage shape decision: one row per
  (node_id, gpu_index, minute_bucket)? or per-second for 1 h +
  per-minute for 7 d (downsampling)? Each shape has real
  implications on retention cron cost and query latency that
  deserve their own design pass.
- Feature 28 already landed six new tables; another time-series
  table with a different retention cadence is scope creep for a
  spec that mostly shipped audit-mirror data.

## Revisit trigger

- A user asks for a "GPU over time" chart on the dashboard and
  the existing events-table query is too slow to satisfy
  interactive use (~1 s).
- Cluster has > 8 GPUs and the "current utilisation only" panel
  from feature 07 is no longer sufficient for capacity planning.
