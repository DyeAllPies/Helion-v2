# Deferred: Resource overcommit

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 03 — resource-aware scheduling](../03-resource-aware-scheduling.md)

## Context

Allow scheduling more work than a node's physical capacity (e.g., 120% CPU). Useful for I/O-bound workloads that don't saturate CPU.

## Why deferred

Safety risk — start with strict no-overcommit. Revisit when usage patterns are better understood.

## Revisit trigger

Revisit when usage patterns are better understood.
