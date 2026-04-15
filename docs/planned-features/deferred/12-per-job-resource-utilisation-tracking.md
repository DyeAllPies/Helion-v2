# Deferred: Per-job resource utilisation tracking

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 07 — observability](../07-observability.md)

## Context

Track actual CPU/memory usage per job (via cgroup stats from the Rust runtime) and expose as metrics.

## Why deferred

Per-job-ID metrics cause cardinality explosion in Prometheus. Requires a different metric model (e.g., histograms by priority tier, not per-job counters).

## Revisit trigger

Revisit once a non-per-job-ID metric model is designed.
