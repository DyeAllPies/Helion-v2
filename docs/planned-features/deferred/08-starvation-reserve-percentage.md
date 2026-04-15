# Deferred: Starvation reserve percentage

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 05 — priority queues](../05-priority-queues.md)

## Context

Reserve a configurable fraction (default 20%) of dispatch slots for the oldest pending jobs regardless of priority, preventing complete starvation of low-priority work.

## Why deferred

The age-based priority boost (+1/min) already provides starvation prevention. The reserve adds complexity and may cause priority inversion.

## Revisit trigger

No explicit trigger — revisit during the next quarterly planning sweep.
