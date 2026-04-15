# Deferred: Affinity / anti-affinity scheduling

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 03 — resource-aware scheduling](../03-resource-aware-scheduling.md)

## Context

Allow jobs to express preferences: "run on same node as job X" (affinity) or "never on same node as job Y" (anti-affinity).

## Why deferred

Not needed for the minimal orchestrator. Adds significant complexity to the scheduler.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
