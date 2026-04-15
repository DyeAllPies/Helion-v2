# Feature: ML Inference Jobs

**Priority:** P1
**Status:** Done (Go runtime; Rust-runtime parity deferred to [deferred/20](deferred/20-rust-runtime-service-parity.md))
**Affected files:**
`proto/node.proto` (ServiceSpec + DispatchRequest.service),
`proto/coordinator.proto` (ServiceEvent + ReportServiceEvent RPC),
`internal/proto/coordinatorpb/types.go` (Go Job.Service, ServiceEndpoint),
`internal/api/types.go` (ServiceSpecRequest on SubmitRequest / JobResponse),
`internal/api/handlers_jobs.go` (validateServiceSpec),
`internal/api/handlers_services.go` (new — GET /api/services/{job_id}),
`internal/api/server.go` (SetServiceRegistry, services field),
`internal/api/helpers.go` (jobToResponse service plumbing),
`internal/cluster/service_registry.go` (new — in-memory endpoint map),
`internal/cluster/node_dispatcher.go` (forward ServiceSpec to DispatchRequest),
`internal/runtime/runtime.go` + `internal/runtime/go_runtime.go` (IsService flag, no-timeout path),
`internal/nodeserver/server.go` + `internal/nodeserver/service_prober.go` (new — probe loop + event emission),
`internal/grpcserver/server.go` + `internal/grpcserver/handlers.go` (WithServiceRegistry, ReportServiceEvent handler),
`internal/grpcclient/client.go` (ReportServiceEvent client call),
`internal/audit/logger.go` (LogServiceEvent),
`cmd/helion-coordinator/main.go` + `cmd/helion-node/main.go` (wiring + advertise address).
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## Inference jobs

The minimum viable serving story is a long-running job with a port mapping
and a readiness probe. We add **one** thing to the job spec:

```go
type SubmitRequest struct {
    // ...
    Service *ServiceSpec `json:"service,omitempty"`
}

type ServiceSpec struct {
    Port            int    `json:"port"`              // port the job binds to
    HealthPath      string `json:"health_path"`       // e.g. "/healthz"
    HealthInitialMS int    `json:"health_initial_ms"` // grace period before first probe
}
```

When `Service` is set:

- The job is treated as long-running (no timeout enforcement, terminal
  state is only reached on explicit stop or process exit).
- After `HealthInitialMS`, the node polls `http://127.0.0.1:<Port><HealthPath>`
  every 5s and reports `service.ready` / `service.unhealthy` events.
- The coordinator records the `node_address:port` mapping and exposes a
  read-only lookup: `GET /api/services/:job_id` returns the upstream URL.

Routing, load balancing across replicas, blue/green, autoscaling — **all
out of scope**. A user who wants those puts an Nginx in front. The point
of this step is "you can train a model and serve it without leaving Helion."

## Security plan (this step)

See [`docs/SECURITY.md` § Inference service surface](../SECURITY.md#inference-service-surface-feature-17) for the authoritative write-up. Summary:

- Prober binds to `127.0.0.1` only; coordinator never proxies.
- `ReportServiceEvent` validates the reporting node against the dispatched job's `NodeID` — same cross-node-poison defence as `ReportResult`.
- Submit-time validator rejects privileged ports, non-absolute health paths, out-of-range ports, and unbounded grace periods.
- Two new audit event types (`service.ready`, `service.unhealthy`) land through the same append-only BadgerDB audit log; edge-triggered so healthy services don't bloat it.

## Implementation notes

### Architecture

```
  POST /jobs {service: {...}}
        │
        ▼
  JobStore.Submit           (persists Job.Service)
        │
        ▼
  Coordinator dispatcher ─► node.Dispatch(DispatchRequest{..., service: ...})
        │                        │
        │                        ├─► runtime.Run(RunRequest{IsService: true, ...})
        │                        │       └─ no timeout, cancel-only context
        │                        │
        │                        └─► go probeService(ctx, jobID, spec)
        │                                └─ every 5s: GET 127.0.0.1:port/health
        │                                   on flip: ReportServiceEvent RPC
        │                                         │
        ▼                                         ▼
  JobCompletionCallback               grpcserver.ReportServiceEvent
      serviceRegistry.Delete(id)          serviceRegistry.Upsert({...})
                                                  │
                                                  ▼
                                      GET /api/services/{id}
                                          200 → {upstream_url: "http://host:port/path"}
```

### Probe-loop edge triggering

The prober emits `ReportServiceEvent` only on state transitions
(`unknown → ready`, `ready → unhealthy`, `unhealthy → ready`), never
on every tick. A happy service produces exactly one event for its
entire lifetime (the initial `ready`). An unhealthy service produces
one `unhealthy` event per outage, plus one `ready` event when it
recovers. This keeps the audit log's cardinality bounded by
transition count, not by uptime.

The `consecutive_failures` counter is *advisory* — the coordinator
does not currently take action on a high value, and the prober
reports the transition immediately rather than after N consecutive
failures. Operators who need a grace window tune
`service.health_initial_ms` instead; for per-probe tolerance the
right place to add it is the prober's transition condition, not the
event emitter.

### Cancel + terminal cleanup

When the underlying process exits (service crashed, or the
coordinator cancelled the dispatch RPC), `rt.Run` returns and the
`defer probeCancel()` in `Dispatch` fires. The prober goroutine
observes `ctx.Done()` on its next `select` and exits without
emitting a final event — `ReportServiceEvent` only records live
state, not "the service is gone."

The terminal `serviceRegistry.Delete(jobID)` is driven by the
coordinator's existing `JobCompletionCallback` (see
`cmd/helion-coordinator/main.go`). That same callback already
drives workflow DAG progression, so feature 17 rides a single
delete-on-terminal hook rather than adding a new lifecycle event.

### Tests

- `internal/cluster/service_registry_test.go` — covers `Upsert / Get / Delete / Count`, empty-job-ID guard, caller-supplied `UpdatedAt` preservation.
- `internal/api/handlers_services_test.go` — lookup returns the correct `upstream_url`, 404 for missing jobs, 404 when the registry is not wired (`SetServiceRegistry` not called).
- `internal/api/handlers_services_validation_test.go` — submit-time rejection of privileged ports, out-of-range ports, empty/relative health paths.
- `internal/grpcserver/testhelpers_test.go` — mock audit logger implements the new `LogServiceEvent` interface method so downstream tests still build.

Full race suite + golangci-lint green on commit.

## Deferred

- [deferred/20](deferred/20-rust-runtime-service-parity.md) — Rust runtime `IsService` handling. The Go backend is the default and covers feature 17 fully; the Rust backend needs a matching `proto/runtime.proto` field and a cooperative timeout-skip in the Rust process supervisor.
