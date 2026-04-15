# Deferred: Webhook registration and delivery

**Priority:** P1
**Status:** Deferred
**Originating feature:** [feature 06 — event system](../06-event-system.md)

## Context

HTTP webhook endpoints for external integrations:

```
POST /webhooks { "url": "https://...", "topics": ["job.completed"], "secret": "..." }
GET  /webhooks
DELETE /webhooks/{id}
```

Delivery guarantees: at-least-once, exponential backoff (5 attempts), HMAC-SHA256 signature, auto-disable after 50 consecutive failures.

## Why deferred

The in-memory event bus + WebSocket stream covers the dashboard use case. Webhooks are a standalone add-on for external integrations.

## Revisit trigger

Revisit when CI/CD integration demand surfaces.
