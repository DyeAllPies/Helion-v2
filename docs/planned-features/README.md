# Planned Features

Feature specs, one file per slice. See
[`../DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md) for how this folder
relates to `audits/` and `deferred/`.

- [`TEMPLATE.md`](TEMPLATE.md) — copy this when starting a new feature.
- `NN-kebab-slug.md` — active feature specs (next unused two-digit number).
- [`deferred/`](deferred/) — items consciously pushed past the current
  scope. Template: [`deferred/TEMPLATE.md`](deferred/TEMPLATE.md).
- [`implemented/`](implemented/) — features that have fully shipped
  and passed an audit reconciling spec vs reality. Moving them out of
  the active list keeps the index focused on what's still in flight.

## Active features

| #  | Feature | Status | Priority | Doc |
|---:|---------|--------|----------|-----|
| 01 | Workflow / DAG support | **Done** | P0 | [01-workflow-dag.md](01-workflow-dag.md) |
| 02 | Retry + failure policies | **Done** | P0 | [02-retry-failure-policies.md](02-retry-failure-policies.md) |
| 03 | Resource-aware scheduling | **Done** | P1 | [03-resource-aware-scheduling.md](03-resource-aware-scheduling.md) |
| 04 | Job state machine improvements | **Done** | P1 | [04-job-state-machine.md](04-job-state-machine.md) |
| 05 | Priority queues | **Done** | P1 | [05-priority-queues.md](05-priority-queues.md) |
| 06 | Event system | **Done** | P2 | [06-event-system.md](06-event-system.md) |
| 07 | Observability improvements | **Done** | P2 | [07-observability.md](07-observability.md) |
| 08 | Deferred-enhancements index (legacy) | Archived | — | [08-deferred-enhancements.md](08-deferred-enhancements.md) |
| 09 | Analytics pipeline (BadgerDB → PostgreSQL) | **Done** | P1 | [09-analytics-pipeline.md](09-analytics-pipeline.md) |
| 10 | Minimal ML pipeline (umbrella) | In progress (steps 1–9 done; 10 pending) | P1 | [10-minimal-ml-pipeline.md](10-minimal-ml-pipeline.md) |
| ~~11~~ | ~~ML — Artifact store abstraction~~ | **Implemented**; moved to [`implemented/11-ml-artifact-store.md`](implemented/11-ml-artifact-store.md) | P1 | — |
| ~~12~~ | ~~ML — Job spec: inputs/outputs/working_dir~~ | **Implemented**; moved to [`implemented/12-ml-job-io-staging.md`](implemented/12-ml-job-io-staging.md) | P1 | — |
| ~~13~~ | ~~ML — Inter-job artifact passing in workflows~~ | **Implemented**; moved to [`implemented/13-ml-workflow-artifact-passing.md`](implemented/13-ml-workflow-artifact-passing.md) | P1 | — |
| ~~14~~ | ~~ML — Node labels and selectors~~ | **Implemented**; moved to [`implemented/14-ml-node-labels-and-selectors.md`](implemented/14-ml-node-labels-and-selectors.md) | P1 | — |
| ~~15~~ | ~~ML — GPU as a first-class resource~~ | **Implemented**; moved to [`implemented/15-ml-gpu-first-class-resource.md`](implemented/15-ml-gpu-first-class-resource.md) | P1 | — |
| ~~16~~ | ~~ML — Dataset and model registry~~ | **Implemented** (Go runtime; audit 2026-04-14-01 M3/M4/L1/L3 deferred); moved to [`implemented/16-ml-dataset-model-registry.md`](implemented/16-ml-dataset-model-registry.md) | P1 | — |
| ~~17~~ | ~~ML — Inference jobs~~ | **Implemented** (Go runtime; Rust parity deferred/20); moved to [`implemented/17-ml-inference-jobs.md`](implemented/17-ml-inference-jobs.md) | P1 | — |
| ~~18~~ | ~~ML — Dashboard module~~ | **Implemented + audited**; moved to [`implemented/18-ml-dashboard-module.md`](implemented/18-ml-dashboard-module.md) | P2 | — |
| ~~19~~ | ~~ML — End-to-end iris demo~~ | **Implemented + acceptance-green (2026-04-18)**; moved to [`implemented/19-ml-end-to-end-demo.md`](implemented/19-ml-end-to-end-demo.md) | P1 | — |
| 20 | ML — Documentation | Pending | P2 | [20-ml-documentation.md](20-ml-documentation.md) |

### Priority definitions

- **P0** — Required for minimal orchestrator.
- **P1** — Required for production use.
- **P2** — High-impact improvements but not blockers.
- **P3** — Backlog. Used on deferred items; see [`deferred/`](deferred/).
