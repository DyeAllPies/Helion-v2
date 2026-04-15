# Deferred: Registry integration test through a real coordinator

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 10 — minimal ML pipeline](../10-minimal-ml-pipeline.md)
**Audit reference:** [2026-04-14-01](../../audits/2026-04-14-01.md) finding L3

## Context

`internal/api/handlers_registry_test.go` exercises the handlers through the ServeMux in a single process with an in-memory BadgerDB. There is no test under `tests/integration/` that spins up the coordinator binary with mTLS + a real registered dataset end-to-end.

## Why deferred

The existing `tests/integration` harness is shaped around gRPC node registration and workflow dispatch. Registry is HTTP-only, and the handler tests already exercise the full validator / rate-limit / audit / event-emission chain within a single process. A new integration shape for this surface is boilerplate-heavy and would mostly catch wiring regressions in `cmd/helion-coordinator/main.go`. If a wiring regression ever lands it will also be caught by the step-10 iris example, which drives the registry end-to-end as part of its own acceptance test.

## Revisit trigger

Revisit if the step-10 iris example stops exercising the registry end-to-end, or if a wiring regression slips past the handler tests.
