# Deferred: End-to-end integration test for inference-service dispatch

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 17 — ML inference jobs](../17-ml-inference-jobs.md)
**Audit reference:** [2026-04-14-02](../../audits/2026-04-14-02.md) finding T1

## Context

Feature 17 landed with unit tests covering every piece of the slice in isolation:

- `internal/cluster/service_registry_test.go` — upsert/get/delete semantics.
- `internal/api/handlers_services_test.go` + `handlers_services_validation_test.go` — HTTP lookup + submit-time validation.
- `internal/grpcserver/report_service_event_test.go` — RPC upsert + cross-node poison rejection + registry-unwired soft accept.

What is **not** covered end-to-end is the full dispatch flow: coordinator submits a service job → dispatcher forwards the ServiceSpec → node runs the process → prober goroutine starts → HTTP probe hits a real socket → state-transition RPC fires → `GET /api/services/{id}` returns the correct upstream URL.

The existing `tests/integration/` harness drives node registration, heartbeat, job dispatch, and result reporting over real mTLS. A service integration test would stand up a mock HTTP server on the node side, dispatch a service job whose `command` wraps that mock's lifetime, and assert the registry reflects readiness within a bounded wait.

## Why deferred

The moving parts are covered at unit-test depth; the integration gap is narrow:

- The dispatcher's `ServiceSpec` plumb is a three-line `if req.Service != nil { ... }` block — either the tests in `node_dispatcher_test.go` pass or they don't.
- The prober loop is pure goroutine + `net/http.Client` code that wouldn't behave differently under mTLS than under the unit harness.
- The gap is in main-wiring only (that all the `Set*` calls in `cmd/helion-coordinator/main.go` line up correctly), which tends to surface in the first end-user smoke test regardless.

Step 19 (iris end-to-end demo) drives the whole pipeline through an actual train → register → serve path as its acceptance test; a service-specific integration test would largely duplicate step 19's coverage. Revisit if step 19 ships and the service portion still isn't exercised.

## Revisit trigger

- Step 19 (iris demo) ships but skips the serve portion for logistics reasons.
- Or: a wiring regression lands in main.go between slices and breaks the production flow without tripping a unit test — that's the canary for adding this integration test sooner.
