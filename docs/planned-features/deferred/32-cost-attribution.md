## Deferred: Cost attribution

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 28 — unified analytics sink](../implemented/28-analytics-unified-sink.md)

## Context

Operators running Helion on paid infrastructure want to see "how
much did my team's jobs cost this month?" The data needed to
compute it is already in analytics:

- Per-job CPU / memory / GPU usage (feature 07 + heartbeats).
- Per-job runtime (job_summary.duration_ms).
- Per-operator submission attribution (feature 28 submission_history).

What's missing is a **pricing model**: how many $/hr is an A100?
What's the cost tier for a spot CPU? Does my deployment get
reserved-instance discounts?

## Why deferred

- **A pricing model is a deployment-specific input, not a
  Helion-generated artefact.** Every operator's cloud bill is
  different (region, spot pricing, reserved instances, committed
  use discounts, negotiated rates). Helion hard-coding a price
  list would be wrong for almost every user.
- **A correct cost model requires a billing-period concept**
  (monthly? by fiscal quarter?) that doesn't exist in the data
  model today. Choosing one is a design call that should happen
  alongside whatever dashboard consumes it.
- **The required data is already available for export.** An
  operator who wants cost attribution today can query the
  feature-28 endpoints + their own pricing spreadsheet.
  Building a first-class cost UI that assumes a model we
  haven't validated is speculative.

## Revisit trigger

- Helion gains a per-resource pricing config surface (whether
  via env, config file, or admin API — doesn't matter; the point
  is somewhere to put prices) and an operator requests
  first-class cost panels.
- An acquirer or production operator needs cost attribution for
  chargeback in a large shared deployment, and the
  "export-and-spreadsheet" workaround is insufficient.

## What would actually ship

- A `pricing` config section (YAML or env) with per-resource-type
  $/hr entries.
- A nightly rollup into a `cost_summary` PostgreSQL table:
  `(day, actor, node_id, cpu_cost, mem_cost, gpu_cost, total)`.
- An Analytics tab "Cost" panel, plus a per-operator breakdown
  that respects `HELION_ANALYTICS_PII_MODE=hash_actor` the same
  way submission history does.
- Retention policy matching the rest of feature 28 (default 60
  days; operator overrides for finance compliance).
