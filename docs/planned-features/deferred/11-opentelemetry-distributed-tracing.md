# Deferred: OpenTelemetry distributed tracing

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 07 — observability](../07-observability.md)

## Context

Full trace propagation: API request → scheduler → dispatch RPC → node runtime → result. Trace IDs in logs and `X-Trace-ID` response headers.

Dependencies: `go.opentelemetry.io/otel`, OTLP exporter, gRPC/HTTP interceptors.

## Why deferred

Adds external dependencies. The structured logging with `job_id`/`node_id` fields and the event bus provide adequate correlation for single-coordinator deployments.

## Revisit trigger

Revisit when multi-coordinator or cross-service tracing becomes necessary.
