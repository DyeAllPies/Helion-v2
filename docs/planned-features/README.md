# Planned Features

Feature specs for evolving Helion v2 into a minimal production orchestrator.
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
| Event system | Missing | P2 | [06-event-system.md](06-event-system.md) |
| Observability improvements | Partial | P2 | [07-observability.md](07-observability.md) |

### Priority definitions

- **P0** — Required for minimal orchestrator.
- **P1** — Required for production use.
- **P2** — High-impact improvements but not blockers.
