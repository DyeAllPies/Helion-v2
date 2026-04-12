# Feature: Observability Improvements

**Priority:** P2
**Status:** Partial — Prometheus metrics + audit logs exist, no distributed tracing
**Affected files:** `internal/metrics/`, `internal/api/`, `internal/grpcserver/`, `cmd/`

## Problem

Helion has basic metrics (Prometheus) and audit logging, but lacks:

1. **Distributed tracing** — no way to follow a job from submission through dispatch to execution across coordinator and node
2. **Request-level tracing** — no correlation between API call, scheduler decision, and dispatch RPC
3. **Custom metrics** — only built-in counters/gauges/histogram, no extension point
4. **Log correlation** — structured logs exist but no trace/span IDs to correlate across services
5. **Job log persistence** — `StreamLogs` RPC exists but logs aren't stored/queryable

## Current state

### What exists

- **Prometheus metrics** (`internal/metrics/prometheus.go`):
  - `helion_jobs_total{status}` — counter by terminal status
  - `helion_running_jobs`, `helion_pending_jobs` — gauges
  - `helion_healthy_nodes`, `helion_total_nodes` — gauges
  - `helion_job_duration_seconds` — histogram
- **Structured logging** — `log/slog` with fields (`job_id`, `node_id`, `err`, `duration`)
- **Audit log** — append-only BadgerDB with event types, timestamps, actors
- **WebSocket metrics** — `/ws/metrics` pushes cluster snapshot every 5s
- **Node metrics** — gRPC `GetMetrics` returns CPU/mem/uptime per node

### What's missing

- No OpenTelemetry integration
- No trace IDs in logs or API responses
- No job output/log storage
- No alerting rules or SLO definitions
- No per-job resource utilization tracking

## Design

### 1. Distributed tracing (OpenTelemetry)

Integrate OpenTelemetry SDK for trace propagation:

```
API request (trace starts)
  └── span: "submit_job"
        └── span: "persist_job" (BadgerDB write)

Dispatch loop
  └── span: "dispatch_job" (per job)
        ├── span: "scheduler.pick" (node selection)
        └── span: "grpc.dispatch" (RPC to node)
              └── span: "runtime.run" (job execution on node)
                    ├── span: "process.exec"
                    └── span: "report_result" (RPC back to coordinator)
```

#### Trace context propagation

- **API → coordinator**: trace ID generated on API request, stored on Job
- **Coordinator → node**: trace context in gRPC metadata (standard W3C traceparent)
- **Node → runtime**: trace context in RunRequest proto field

#### Configuration

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317    # OTLP gRPC endpoint
OTEL_SERVICE_NAME=helion-coordinator               # or helion-node
HELION_TRACING_ENABLED=true                        # default: false
HELION_TRACING_SAMPLE_RATE=0.1                     # 10% sampling (default: 1.0 in dev)
```

When tracing is disabled, all operations are no-ops (zero overhead).

### 2. Log correlation

Add trace and span IDs to all structured log output:

```go
// Before:
slog.Info("job dispatched", "job_id", id, "node_id", nodeID)

// After:
slog.Info("job dispatched", "job_id", id, "node_id", nodeID,
    "trace_id", span.SpanContext().TraceID(),
    "span_id", span.SpanContext().SpanID())
```

Also return trace ID in API responses:

```
HTTP/1.1 201 Created
X-Trace-ID: 4bf92f3577b34da6a3ce929d0e0e4736
```

### 3. Job log storage

Store job stdout/stderr for later retrieval:

```
Node executes job
  → runtime captures stdout/stderr
  → StreamLogs RPC sends chunks to coordinator
  → Coordinator stores in BadgerDB (key: log:<job_id>:<chunk_seq>)
  → TTL: same as job retention (configurable, default 7 days)
```

API endpoint: `GET /jobs/{id}/logs`
- Returns full job output (stdout + stderr interleaved with timestamps)
- Query params: `stream=stdout|stderr|both`, `tail=100`

WebSocket: `GET /ws/jobs/{id}/logs` (already defined, not yet implemented)
- Streams log chunks in real time while job is running
- Replays stored logs for completed jobs

### 4. Additional metrics

New Prometheus metrics to add:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `helion_dispatch_duration_seconds` | histogram | | Time from pending to dispatching |
| `helion_queue_wait_seconds` | histogram | priority | Time job spent in pending state |
| `helion_dispatch_errors_total` | counter | reason | Dispatch failures by reason |
| `helion_node_cpu_usage_ratio` | gauge | node_id | Per-node CPU utilization |
| `helion_node_memory_usage_ratio` | gauge | node_id | Per-node memory utilization |
| `helion_retry_total` | counter | | Jobs that entered retry state |
| `helion_workflow_duration_seconds` | histogram | | End-to-end workflow time |
| `helion_active_websockets` | gauge | type | Active WebSocket connections |

### 5. Health check improvements

Enhance readiness probe (`/readyz`) with subsystem checks:

```json
{
  "status": "ready",
  "checks": {
    "badgerdb": "ok",
    "scheduler": "ok",
    "grpc_server": "ok",
    "healthy_nodes": 3
  }
}
```

Return 503 if any critical subsystem is unhealthy.

## New/modified packages

### `internal/tracing/` (new)

```
tracing/
  provider.go    — OpenTelemetry TracerProvider setup, shutdown
  middleware.go  — HTTP middleware that starts spans + injects trace ID
  grpc.go        — gRPC interceptors for trace propagation
```

### `internal/logstore/` (new)

```
logstore/
  store.go       — BadgerDB storage for job logs (append, read, TTL)
```

## Implementation order

1. Job log storage + `GET /jobs/{id}/logs` endpoint (high user value, no external deps)
2. WebSocket log streaming (`/ws/jobs/{id}/logs`)
3. Additional Prometheus metrics
4. Health check improvements
5. OpenTelemetry integration (coordinator + node)
6. Log correlation (trace IDs in slog)
7. Dashboard: log viewer, trace link integration

## Dependencies

- `go.opentelemetry.io/otel` — OpenTelemetry SDK
- `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` — OTLP exporter
- `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` — gRPC interceptors
- `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` — HTTP middleware

## Open questions

- Should logs be stored in BadgerDB or on filesystem? (BadgerDB for consistency with existing persistence, filesystem if logs are large)
- Metrics cardinality: per-job-id metrics would explode. Only use bounded labels (status, node_id, priority tier).
- Grafana dashboard templates? (Defer — users bring their own dashboards, we provide the metrics)
