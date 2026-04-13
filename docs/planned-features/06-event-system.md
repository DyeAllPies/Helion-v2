# Feature: Event System

**Priority:** P2
**Status:** Implemented (core bus + WS stream; webhooks deferred)
**Affected files:** `internal/events/bus.go`, `internal/events/topics.go`, `internal/cluster/job.go`, `internal/cluster/job_transition.go`, `internal/api/handlers_ws.go`, `internal/api/server.go`

## Problem

The system has no way to react to events in real time. The audit log captures events but is read-only and pull-based. Users and integrations cannot:

1. Get notified when a job completes (must poll `GET /jobs/{id}`)
2. Trigger downstream actions on events (must build external polling)
3. Subscribe to a filtered stream of events (e.g., "all failures on node-3")
4. Integrate with external systems (webhooks, message queues)

## Current state

- Audit logger (`internal/audit/logger.go`) appends events to BadgerDB
- REST `GET /audit` returns paginated, filterable events (pull-based)
- WebSocket `GET /ws/metrics` pushes cluster metrics every 5s (not events)
- Job transitions emit audit records but no live notifications
- No webhook, SSE, or message queue integration

## Design

### Event bus (internal)

A lightweight in-process pub/sub bus that decouples event producers from consumers:

```go
// internal/events/bus.go
type Event struct {
    ID        string            `json:"id"`
    Type      string            `json:"type"`
    Timestamp time.Time         `json:"timestamp"`
    Data      map[string]any    `json:"data"`
}

type Bus struct {
    subscribers map[string][]chan Event // topic → channels
    mu          sync.RWMutex
    bufferSize  int
}

func (b *Bus) Publish(topic string, event Event)
func (b *Bus) Subscribe(topic string) (<-chan Event, func()) // returns channel + unsubscribe func
```

### Event topics

| Topic | Emitted when | Data fields |
|-------|-------------|-------------|
| `job.submitted` | Job created | job_id, command, priority |
| `job.transition` | Any state change | job_id, from_status, to_status, node_id |
| `job.completed` | Job succeeded | job_id, duration_ms, node_id |
| `job.failed` | Job failed | job_id, error, exit_code, attempt |
| `job.retrying` | Job entering retry | job_id, attempt, next_retry_at |
| `node.registered` | Node joins cluster | node_id, address |
| `node.stale` | Node missed heartbeats | node_id, last_seen |
| `node.revoked` | Node removed | node_id, reason |
| `workflow.completed` | All workflow jobs done | workflow_id, status |
| `workflow.failed` | Workflow failed | workflow_id, failed_job |

### WebSocket event stream

New endpoint: `GET /ws/events`

```
Client connects → sends JWT token as first message
Client sends subscription: {"subscribe": ["job.*", "node.stale"]}
Server pushes matching events as JSON frames
Client can update subscription: {"subscribe": ["workflow.*"]}
```

Supports glob patterns on topic names. Multiple subscriptions are merged (OR).

### Webhook delivery

Users register webhook endpoints via API:

```
POST /webhooks
{
  "url": "https://example.com/hook",
  "topics": ["job.completed", "job.failed"],
  "secret": "hmac-secret-for-signature"
}
```

Delivery guarantees:
- At-least-once delivery (retry on non-2xx response)
- Exponential backoff: 1s, 2s, 4s, 8s, 16s (5 attempts max)
- HMAC-SHA256 signature in `X-Helion-Signature` header
- Webhook disabled after 50 consecutive failures (re-enable via API)

### Integration with existing audit

The event bus does NOT replace the audit logger. Both receive events:

```
Job transition
  ├── audit.Append(event)     // existing: durable, append-only
  └── bus.Publish(topic, event) // new: ephemeral, real-time
```

Audit log = compliance/forensics (durable). Event bus = real-time reactions (ephemeral).

### DAG trigger integration

The event bus enables DAG execution without polling:

```go
// In dispatch loop or workflow manager:
bus.Subscribe("job.completed", func(e Event) {
    workflowID := e.Data["workflow_id"]
    if workflowID != "" {
        // Check if downstream jobs are now eligible
        wm.EvaluateWorkflow(workflowID)
    }
})
```

This replaces the poll-based "check all pending jobs every tick" approach for workflow jobs, making DAG execution event-driven.

## New internal package

### `internal/events/`

```
events/
  bus.go          — pub/sub bus (in-memory, fan-out to subscribers)
  topics.go       — topic constants and event constructors
  webhook.go      — HTTP webhook delivery with retry
  webhook_store.go — webhook registration persistence (BadgerDB)
```

## API changes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/ws/events` | WebSocket event stream (subscribe with topics) |
| POST | `/webhooks` | Register a webhook |
| GET | `/webhooks` | List registered webhooks |
| DELETE | `/webhooks/{id}` | Remove a webhook |
| GET | `/webhooks/{id}/deliveries` | Recent delivery attempts (debug) |

## Implementation order

1. `internal/events/bus.go` — in-memory pub/sub (no persistence)
2. Emit events from job transitions and node lifecycle
3. WebSocket `/ws/events` endpoint with topic filtering
4. Webhook registration API + delivery with retry
5. DAG trigger integration (event-driven workflow evaluation)
6. Dashboard: live event feed panel

## Scalability notes

- In-memory bus is fine for single-coordinator architecture
- If Helion ever supports multiple coordinators, replace with Redis pub/sub or NATS
- Webhook delivery runs in a goroutine pool (default: 10 workers) to avoid blocking the event bus
- Channel buffer size: 256 events per subscriber (drops oldest on overflow, logs warning)

## Implementation status

Core event system implemented. Webhooks deferred as a standalone add-on.

1. **Event bus** — `internal/events/bus.go`: in-memory pub/sub with glob topic matching. Non-blocking publish, 256-event channel buffer per subscriber, drops on overflow.
2. **Event topics** — `internal/events/topics.go`: 10 topic constants + typed constructors (JobSubmitted, JobTransition, JobCompleted, JobFailed, JobRetrying, NodeRegistered, NodeStale, NodeRevoked, WorkflowCompleted, WorkflowFailed).
3. **Emission** — `JobStore.Submit` emits `job.submitted`, `Transition` emits `job.transition` + `job.completed`/`job.failed` for terminal states. Bus wired via `SetEventBus()`.
4. **WebSocket endpoint** — `GET /ws/events`: first-message JWT auth, then client sends `{"subscribe":["job.*"]}`, server streams matching events as JSON frames.
5. **Dashboard** — Events page at `/events` with live feed, color-coded by category (job/node/workflow), clear button, LIVE/DISCONNECTED indicator.
6. **Coordinator wiring** — Bus created at startup, injected into JobStore and API server.
7. **Tests** — 9 bus unit tests (pub/sub, filtering, wildcard, cancel, subscriber count), 3 event emission integration tests, 10 topic constructor tests. Events package at 95.7% coverage.

Deferred:
- Webhook registration + delivery (POST /webhooks API)
- Event-driven DAG evaluation (replacing poll-based dispatch for workflows)
