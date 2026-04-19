## Deferred: Federated analytics across clusters

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 28 — unified analytics sink](../implemented/28-analytics-unified-sink.md)

## Context

Helion is single-coordinator today. An organisation running two or
more coordinators (prod + staging, EU + US region, etc.) has N
separate analytics databases and no built-in way to see "total
submission history across all our clusters" in one dashboard.

Federated analytics would add:

- A **read-side federation endpoint** on each coordinator that
  returns its analytics rows to a central aggregator.
- An **aggregator service** that round-robins or concurrently
  queries every enrolled coordinator and merges the results for a
  unified view.
- **Cluster-scoped filters** on the dashboard (toggle cluster on /
  off in a single panel).

## Why deferred

- Only relevant once Helion runs in a multi-coordinator topology.
  The single-cluster case (everyone today) is adequately served
  by the per-coordinator analytics endpoint.
- Aggregation shape is non-trivial: a "jobs-per-hour" chart
  across clusters is additive; a "per-operator submission count"
  is NOT if different clusters have different PII salts (feature
  28 `HELION_ANALYTICS_PII_MODE=hash_actor`). Salt management
  across federated clusters is a larger identity story.
- Cross-cluster read auth (who can see cluster B's data?) opens
  a new trust-boundary design that feature 33 (per-operator
  accountability) hasn't landed yet to underpin.

## Revisit trigger

- Helion is deployed to a second production cluster in the same
  organisation and operators start asking for a unified
  dashboard view.
- Feature 33 (per-operator accountability) lands and we have a
  stable cross-cluster identity model to build on.
