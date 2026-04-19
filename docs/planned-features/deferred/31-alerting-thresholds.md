## Deferred: Alerting on analytics thresholds

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 28 — unified analytics sink](../implemented/28-analytics-unified-sink.md)

## Context

"Notify me when `auth_fail` rate > 10 / minute sustained for 5
minutes" is a natural follow-up once the analytics panels exist.
Operators want the feature-28 data to **page them** rather than
require them to watch a dashboard.

A first-class alerting system would need:

- Rule storage + CRUD (probably another BadgerDB namespace).
- A background evaluator that polls the PG analytics tables and
  fires a handler when a threshold is met.
- Notification backends (email, PagerDuty, webhook, Slack).
- Rule UI in the dashboard.
- A silencing / snooze UI so operators don't fatigue under a
  stuck alert.

## Why deferred

- **Prometheus + Alertmanager pair better than a hand-rolled
  alerting UI.** Helion already exposes Prometheus metrics on
  `/metrics`; the feature-28 endpoints can feed Grafana (which
  has best-in-class alert UX). Reinventing alerting inside the
  dashboard duplicates tooling most shops already run.
- **An in-dashboard alerting system is a paging pipeline**, and
  the reliability bar on paging pipelines is very high —
  missed-alert bugs cost real time. Building that inside a
  dashboard component is the wrong place.
- **The Prometheus exporters are the load-bearing integration
  point.** A follow-up slice could expose feature-28 metrics as
  `helion_auth_fail_total` / `helion_submission_rejected_total` /
  etc. and let Alertmanager do the rest. Cheap + it reuses the
  operator's existing monitoring stack.

## Revisit trigger

- An operator explicitly asks for in-dashboard alerting and the
  "set up Alertmanager externally" workaround is blocked by org
  policy (e.g., no Prometheus in the environment).
- Feature-28 data becomes the primary source of truth for
  paging and Prometheus scraping is no longer sufficient
  (multi-cluster federation case — see
  [deferred/30-federated-analytics.md](30-federated-analytics.md)).

## Alternative path (what to do today)

Expose the feature-28 counters as Prometheus metrics in
`internal/metrics/` (small, ~20-line change per counter) and let
the operator configure Alertmanager rules against them. That
covers 95 % of the "notify me when X spikes" use case without
any new persistence or UI.
