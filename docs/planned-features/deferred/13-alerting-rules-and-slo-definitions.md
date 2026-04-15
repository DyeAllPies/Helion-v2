# Deferred: Alerting rules and SLO definitions

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 07 — observability](../07-observability.md)

## Context

Ship predefined Prometheus alerting rules (e.g., "alert if pending queue > 100 for > 5 min") and SLO templates.

## Why deferred

Users bring their own alerting stack. Providing metrics is sufficient; opinionated alerting rules may not match each deployment's SLOs.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
