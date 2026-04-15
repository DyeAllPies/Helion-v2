# Planned Features

Feature specs, one file per slice. See
[`../DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md) for how this folder
relates to `audits/` and `deferred/`.

- [`TEMPLATE.md`](TEMPLATE.md) — copy this when starting a new feature.
- `NN-kebab-slug.md` — active feature specs (next unused two-digit number).
- [`deferred/`](deferred/) — items consciously pushed past the current
  scope. Template: [`deferred/TEMPLATE.md`](deferred/TEMPLATE.md).

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
| 10 | Minimal ML pipeline (umbrella) | In progress (steps 1–6 done; 7–10 pending) | P1 | [10-minimal-ml-pipeline.md](10-minimal-ml-pipeline.md) |
| 11 | ML — Artifact store abstraction | **Done** | P1 | [11-ml-artifact-store.md](11-ml-artifact-store.md) |
| 12 | ML — Job spec: inputs/outputs/working_dir | **Done** | P1 | [12-ml-job-io-staging.md](12-ml-job-io-staging.md) |
| 13 | ML — Inter-job artifact passing in workflows | **Done** | P1 | [13-ml-workflow-artifact-passing.md](13-ml-workflow-artifact-passing.md) |
| 14 | ML — Node labels and selectors | **Done** | P1 | [14-ml-node-labels-and-selectors.md](14-ml-node-labels-and-selectors.md) |
| 15 | ML — GPU as a first-class resource | **Done** | P1 | [15-ml-gpu-first-class-resource.md](15-ml-gpu-first-class-resource.md) |
| 16 | ML — Dataset and model registry | **Done** | P1 | [16-ml-dataset-model-registry.md](16-ml-dataset-model-registry.md) |
| 17 | ML — Inference jobs | Pending | P1 | [17-ml-inference-jobs.md](17-ml-inference-jobs.md) |
| 18 | ML — Dashboard module | Pending | P2 | [18-ml-dashboard-module.md](18-ml-dashboard-module.md) |
| 19 | ML — End-to-end iris demo | Pending | P2 | [19-ml-end-to-end-demo.md](19-ml-end-to-end-demo.md) |
| 20 | ML — Documentation | Pending | P2 | [20-ml-documentation.md](20-ml-documentation.md) |

### Priority definitions

- **P0** — Required for minimal orchestrator.
- **P1** — Required for production use.
- **P2** — High-impact improvements but not blockers.
- **P3** — Backlog. Used on deferred items; see [`deferred/`](deferred/).
