# Planned Features

Feature specs that evolved Helion v2 from a job scheduler into a **minimal
orchestrator**. All 7 core features (01–07) are implemented. Feature 08
consolidates deferred enhancements as a backlog for future work.

Each file follows the template below.

## Feature template

When creating a new feature spec, copy this structure:

```markdown
# Feature: <name>

**Priority:** P0 / P1 / P2
**Status:** Missing / Partial / Implemented
**Affected files:** `path/to/file.go`, ...

## Problem
What's wrong or missing today.

## Current state
What exists, with file references.

## Design
Types, algorithms, API changes, state machine changes.

## Implementation order
Numbered steps from easiest to hardest.

## Open questions
Decisions deferred or needing input.

## Implementation status
(Added after implementation — list what was built, file paths, test counts.)
```

## Feature index

| Feature | Status | Priority | Doc |
|---------|--------|----------|-----|
| Workflow / DAG support | **Implemented** | P0 | [01-workflow-dag.md](01-workflow-dag.md) |
| Retry + failure policies | **Implemented** | P0 | [02-retry-failure-policies.md](02-retry-failure-policies.md) |
| Resource-aware scheduling | **Implemented** | P1 | [03-resource-aware-scheduling.md](03-resource-aware-scheduling.md) |
| Job state machine improvements | **Implemented** | P1 | [04-job-state-machine.md](04-job-state-machine.md) |
| Priority queues | **Implemented** | P1 | [05-priority-queues.md](05-priority-queues.md) |
| Event system | **Implemented** | P2 | [06-event-system.md](06-event-system.md) |
| Observability improvements | **Implemented** | P2 | [07-observability.md](07-observability.md) |
| Deferred enhancements | Not started | P3 | [08-deferred-enhancements.md](08-deferred-enhancements.md) |
| Analytics pipeline (BadgerDB → PostgreSQL) | **Implemented** | P1 | [09-analytics-pipeline.md](09-analytics-pipeline.md) |
| Minimal ML pipeline | Not started | P1 | [10-minimal-ml-pipeline.md](10-minimal-ml-pipeline.md) |

### Priority definitions

- **P0** — Required for minimal orchestrator.
- **P1** — Required for production use.
- **P2** — High-impact improvements but not blockers.
