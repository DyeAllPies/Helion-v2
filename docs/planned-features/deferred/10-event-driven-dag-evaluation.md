# Deferred: Event-driven DAG evaluation

**Priority:** P1
**Status:** Deferred
**Originating feature:** [feature 06 — event system](../06-event-system.md)

## Context

Replace the poll-based "check all pending workflow jobs every tick" with event-driven evaluation: subscribe to `job.completed` events and immediately check downstream eligibility.

## Why deferred

The polling approach works correctly. Event-driven evaluation is an optimisation that reduces latency for workflow job transitions but isn't required for correctness.

## Revisit trigger

Revisit when workflow job transition latency becomes user-visible.
