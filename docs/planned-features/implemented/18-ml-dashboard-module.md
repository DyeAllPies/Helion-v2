# Feature: ML Dashboard Module

**Priority:** P1
**Status:** Done (all four views — Datasets / Models / Services / Pipelines — shipped; the Pipelines DAG view followed on from the initial slice, see [deferred/implemented/24](../deferred/implemented/24-ml-pipelines-dag-view.md))
**Affected files:**
`internal/cluster/dispatch.go` (ml.resolve_failed emit + classifyUnschedulable),
`internal/cluster/scheduler.go` (NodeSource.Snapshot accessor),
`internal/cluster/service_registry.go` (Snapshot for list view),
`internal/events/topics.go` (TopicMLResolveFailed + UnschedulableReason* constants + JobUnschedulable.reason field),
`internal/api/handlers_services.go` (GET /api/services list handler + ServiceListResponse type),
`internal/api/server.go` (route header note),
`dashboard/src/app/shared/models/index.ts` (Dataset, MLModel, ServiceEndpoint shapes),
`dashboard/src/app/core/services/api.service.ts` (datasets / models / services REST methods),
`dashboard/src/app/features/ml/` (new module — three list components + register-dataset dialog + shared SCSS),
`dashboard/src/app/app.routes.ts` (ml/* lazy routes),
`dashboard/src/app/shell/shell.component.ts` (sidebar links).
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## Dashboard: ML module

A lazy-loaded Angular module at `dashboard/src/app/features/ml/`
exposing three views, plus two backend follow-ups that were called
out in the spec for steps 3 and 4 of the parent slice.

### Views

- **Datasets** (`/ml/datasets`) — paginated list of registered datasets
  reading `GET /api/datasets`. Register-via-form modal posts to
  `POST /api/datasets`; delete button calls `DELETE /api/datasets/{name}/{version}`
  behind a confirm prompt. URI scheme + format hints surface in the
  modal; the coordinator's `validate.go` is the authoritative gate
  and 400 messages flow back into the table's error banner.
- **Models** (`/ml/models`) — paginated list reading `GET /api/models`.
  Surfaces lineage (link to source job + dataset name+version) and
  free-form metrics as labelled pills. Register-from-UI is omitted —
  models are expected to be registered by training jobs via the REST
  API, not by an operator clicking a form.
- **Services** (`/ml/services`) — live inference endpoints reading
  `GET /api/services` (new list endpoint added by this slice). Polls
  every 5 s via `interval(5000).pipe(startWith(0), switchMap(...))`
  so the table stays at most one node-prober tick stale. Renders a
  ready/unhealthy chip, the upstream URL, and a back-link to the job.

### Backend follow-ups (from the parent slice)

- **`ml.resolve_failed` event** (step-3 follow-up). When the dispatch
  loop's artifact resolver fails, the coordinator now emits a
  distinct `TopicMLResolveFailed` event in addition to transitioning
  the job to Failed. The event carries `(workflow_id, job_id,
  upstream, output_name, reason)` so a future Pipelines view (and
  the existing event feed) can surface ML pipeline breakage at a
  glance instead of the operator reading raw coordinator logs.
- **`job.unschedulable.reason`** (step-4 follow-up). The
  `JobUnschedulable` event grew a `reason` field with three stable
  values (`no_healthy_node` / `no_matching_label` /
  `all_matching_unhealthy`). The dispatch loop walks the registry's
  full snapshot to distinguish "wrong labels" from "matching nodes
  all stale", giving operators a triage signal without log-grepping.
  `events.UnschedulableReason*` constants pin the wire strings so a
  rename is a dashboard-breaking change, not a silent drift.

### REST surface added by this slice

| Method + Route             | Handler                  | Notes |
|----------------------------|--------------------------|-------|
| `GET /api/services`        | `handleListServices`     | Returns `{services, total}`; 404 if registry not wired. Memory-only read on the coordinator side; no rate limit needed beyond the standard auth middleware. |

## Security plan (this step)

See [`docs/SECURITY.md` § ML dashboard module surface](../../security/README.md#ml-dashboard-module-surface-feature-18) for the authoritative write-up. Summary:

- All three views inherit the dashboard's existing `authGuard` + JWT interceptor; no new auth surface.
- `GET /api/services` is the same data the per-job lookup already exposes, just batched. Same auth middleware.
- Dataset register modal hints at the URI allowlist; the coordinator's `validate.go` is the authoritative gate.
- Delete confirms in the UI are UX guards. The flat "any authenticated user can delete any entry" policy from feature 16 still applies — tightening tracked under [`deferred/17-registry-lineage-enforcement.md`](../deferred/17-registry-lineage-enforcement.md).

Two new audit-relevant events surface in this slice:

| Event | Emitter | Surfaced where |
|---|---|---|
| `ml.resolve_failed` | `internal/cluster/dispatch.go` (resolver failure path) | Future Pipelines view; today on `/events` and the audit log |
| `job.unschedulable` (with `reason`) | `internal/cluster/dispatch.go:maybeEmitUnschedulable` | Same — the existing event-feed view shows the reason verbatim |

## Tests

Backend:

- `internal/cluster/dispatch_unschedulable_reason_test.go` — covers the three reason classifications + the `firstFromRef` parser used to populate `ml.resolve_failed`.
- `internal/api/handlers_services_test.go` — three new cases: empty list, populated list, 404 when registry not wired.

Dashboard (25 new tests):

- `ml-datasets.component.spec.ts` — init-load, render, error path, pagination, register modal happy path (with a component-level `MatDialog` provider override so the spy reaches the same instance the component injects), cancel-modal path, delete confirm gate, byte formatter unit cases.
- `ml-models.component.spec.ts` — init-load, render, error path, pagination, lineage rendering, metrics sort + format, delete confirm gate.
- `ml-services.component.spec.ts` — `fakeAsync` + `tick` driving the 5-second poll, immediate emit on `startWith(0)`, error-then-recovery flow, `reload()` one-shot, and the unsubscribe-on-destroy invariant.

All 167 dashboard tests + the affected Go packages (`internal/cluster`, `internal/events`, `internal/api`, `internal/grpcserver`) green; no new lint warnings.

## Follow-ups that landed

- **Pipelines DAG view** — originally deferred (deferred/24), now
  implemented (see
  [`../deferred/implemented/24-ml-pipelines-dag-view.md`](../deferred/implemented/24-ml-pipelines-dag-view.md)
  for the full arc). Adds `GET /workflows/{id}/lineage` on the
  coordinator (one round-trip join across workflow + jobs +
  registered models), `/ml/pipelines` + `/ml/pipelines/:id` on the
  dashboard, and a mermaid-rendered DAG distinguishing
  dependency arrows from artifact-flow arrows.
- **Audit-pass cleanup.** A second-pass audit of the original
  spec found four gaps; three closed inline post-Pipelines:
  - Models view's `source_dataset` rendered as a clickable link
    into `/ml/datasets` (with name + version pre-filled in the
    query params) instead of plain text.
  - Register-dataset modal grew a tags input that parses
    comma-separated `key:value` pairs into the request body's
    `tags` map. Server-side validation remains the authoritative
    gate; the dialog surfaces parse errors inline.
  - Datasets view grew a tag-filter input with a client-side
    case-insensitive match against the loaded page (full-corpus
    filter is on the deferred backlog as part of registry
    indexed listing).
  - `register-dataset-dialog.component.ts` got its missing spec
    (8 cases covering canSubmit gating, request building, tag
    parsing, error surfacing, and cancel).
  Dashboard suite at **200 green** (was 167 → 186 with Pipelines
  → 200 with the audit-pass additions).

## Audit-pass deferrals

The same audit pass identified three items that were spec'd in the
original feature but are real-scope work, deferred with explicit
revisit triggers:

- [`../deferred/25-dataset-upload-modal.md`](../deferred/25-dataset-upload-modal.md) — register-via-browser-upload modal. Needs the signed-URL endpoint that's already on the SECURITY.md backlog; until then the URI-form flow covers the operator workflow.
- [`../deferred/26-pipelines-event-integration.md`](../deferred/26-pipelines-event-integration.md) — Pipelines rows surfacing `ml.resolve_failed` + `job.unschedulable` reason badges. The events are emitted, audited, and visible on `/events`; the per-workflow rollup is a query-shape decision better made once step 19's iris demo gives a real usage signal.
- [`../deferred/27-pipelines-produced-models-filter.md`](../deferred/27-pipelines-produced-models-filter.md) — "filter the Pipelines list to workflows that produced a registered model." Needs a `?has_registered_model=true` query on `/workflows`; not blocking the unfiltered list and no operator has asked yet.
