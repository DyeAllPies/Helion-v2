# Deferred: Workflow parameters and templating

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 01 — workflow / DAG](../01-workflow-dag.md)

## Context

Allow workflows to accept parameters that are substituted into job commands/env:

```json
{
  "id": "deploy-v3",
  "params": { "version": "3.2.1", "env": "prod" },
  "jobs": [
    { "name": "build", "command": "make", "args": ["VERSION={{version}}"] }
  ]
}
```

## Why deferred

Adds complexity without core value for the minimal orchestrator.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
