# Feature: ML Inference Jobs

**Priority:** P1
**Status:** Pending
**Affected files:** `internal/api/types.go`, `internal/cluster/persistence_jobs.go`, `internal/nodeserver/server.go`, `internal/api/handlers_services.go` (new).
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

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| Long-running serving process exposes a port | Health probe is `GET 127.0.0.1:<port><path>` only — no external bind from the Stager's perspective; service RPC lookup (`GET /api/services/:job_id`) requires JWT; rate-limit at the standard node-RPC limiter; the coordinator does **not** proxy traffic | — |

Threat additions handled here:

| Threat | Mitigation |
|---|---|
| Inference port collision / unauthorized bind | Bind to 127.0.0.1 only; coordinator records `node_address:port` but does not proxy without explicit route config |

Audit event taxonomy:

| Event | Actor | Target | Details |
|---|---|---|---|
| `service.ready` / `service.unhealthy` | `node:<node_id>` | `job:<job_id>` | `{port, health_path, consecutive_failures}` |
