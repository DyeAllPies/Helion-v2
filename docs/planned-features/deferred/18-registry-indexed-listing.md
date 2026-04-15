# Deferred: Registry indexed listing

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 10 — minimal ML pipeline](../10-minimal-ml-pipeline.md)
**Audit reference:** [2026-04-14-01](../../audits/2026-04-14-01.md) finding L1

## Context

`ListDatasets` / `ListModels` currently full-scan the prefix, JSON-decode every entry, sort by `CreatedAt`, and slice to the requested page. Cost is O(n) in total registered entries per list call regardless of page size.

## Why deferred

The handler is behind the registry rate limiter (2/s per subject, burst 30) and even at 100k entries the scan is sub-50 ms with BadgerDB's LSM layout. The fix — either a secondary `CreatedAt` index or cursor-based pagination — is a meaningful scope increase that earns nothing at current traffic. Revisit if a real operator reports registry size past the 10k mark, or if the dashboard's ML module (step 8) starts driving a lot of parallel list requests.

## Revisit trigger

Registry size past the 10k mark, or the dashboard ML module starts driving parallel list requests.
