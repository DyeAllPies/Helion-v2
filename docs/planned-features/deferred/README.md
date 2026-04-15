# Deferred Enhancements — Index

This folder holds the running backlog of items that were identified mid-slice
during feature work but consciously left out of the active implementation.
Each item lives in its own numbered file so it can be linked, edited, and
eventually promoted to an active slice independently. Use [`TEMPLATE.md`](./TEMPLATE.md)
as the starting point for new entries.

**When to add here vs. a new numbered feature doc:** if an item is identified
mid-slice as "we could do this but it's not core to the feature we're
shipping," file it here under the feature it came from. New numbered docs
under `docs/planned-features/` are for *active slices*. The backlog is
append-only — moving an item into a numbered slice (as happened with GPU
resources → feature 10 step 5) replaces the entry here with a pointer.

Items that have since been implemented move to
[`implemented/`](./implemented/) keeping their original number; the
deferral write-up + the landed-implementation write-up both live in
the single moved file so future reviewers can see the full arc.

## Backlog

| # | Item | From | Priority |
|---|------|------|----------|
| 01 | [Workflow parameters and templating](./01-workflow-parameters-and-templating.md) | feature 01 | P3 |
| 02 | [Workflow re-run from failed step](./02-workflow-rerun-from-failed-step.md) | feature 01 | P2 |
| 03 | [Permanent failure classification](./03-permanent-failure-classification.md) | feature 02 | P2 |
| 04 | [Resource overcommit](./04-resource-overcommit.md) | feature 03 | P3 |
| 05 | [Affinity / anti-affinity scheduling](./05-affinity-anti-affinity-scheduling.md) | feature 03 | P3 |
| 06 | [Mutable priority after submission](./06-mutable-priority-after-submission.md) | feature 05 | P2 |
| 07 | [Per-user priority limits](./07-per-user-priority-limits.md) | feature 05 | P3 |
| 08 | [Starvation reserve percentage](./08-starvation-reserve-percentage.md) | feature 05 | P3 |
| 09 | [Webhook registration and delivery](./09-webhook-registration-and-delivery.md) | feature 06 | P1 |
| 10 | [Event-driven DAG evaluation](./10-event-driven-dag-evaluation.md) | feature 06 | P1 |
| 11 | [OpenTelemetry distributed tracing](./11-opentelemetry-distributed-tracing.md) | feature 07 | P3 |
| 12 | [Per-job resource utilisation tracking](./12-per-job-resource-utilisation-tracking.md) | feature 07 | P3 |
| 13 | [Alerting rules and SLO definitions](./13-alerting-rules-and-slo-definitions.md) | feature 07 | P3 |
| 14 | [Grafana dashboard templates](./14-grafana-dashboard-templates.md) | feature 07 | P3 |
| 15 | [Coordinator-side in-use resource tracking](./15-coordinator-side-in-use-resource-tracking.md) | feature 10 | P2 |
| 16 | [Hardware attestation of node labels](./16-hardware-attestation-of-node-labels.md) | feature 10 | P3 |
| 17 | [Registry lineage enforcement](./17-registry-lineage-enforcement.md) | feature 10 (audit 2026-04-14-01, M3/M4) | P3 |
| 18 | [Registry indexed listing](./18-registry-indexed-listing.md) | feature 10 (audit 2026-04-14-01, L1) | P3 |
| 19 | [Registry integration test](./19-registry-integration-test.md) | feature 10 (audit 2026-04-14-01, L3) | P3 |
| 20 | [Rust runtime parity for inference services](./20-rust-runtime-service-parity.md) | feature 17 | P2 |
| 21 | [Service jobs in workflow specs](./21-service-jobs-in-workflow.md) | feature 17 (audit 2026-04-14-02, M1) | P3 |
| 22 | [Rate-limit ReportServiceEvent](./22-report-service-event-rate-limit.md) | feature 17 (audit 2026-04-14-02, L1) | P3 |
| 23 | [Service integration e2e test](./23-service-integration-e2e.md) | feature 17 (audit 2026-04-14-02, T1) | P3 |
| ~~24~~ | ~~ML Pipelines DAG view~~ — **Implemented**; moved to [`implemented/24-ml-pipelines-dag-view.md`](./implemented/24-ml-pipelines-dag-view.md) | feature 18 | — |

> **GPU / accelerator resources:** moved to [feature 10 step 5](../10-minimal-ml-pipeline.md) — GPU as a first-class resource. The pointer is retained so older commits and audits still resolve.
