> **Audience:** engineers
> **Scope:** Angular 21 dashboard — stack, component structure, testing, local dev.
> **Depth:** reference

# Helion Dashboard — Angular 21

The control-plane UI for the Helion v2 minimal orchestrator.

## Tech stack

| Concern            | Choice                             |
|--------------------|------------------------------------|
| Framework          | Angular 21 (standalone components) |
| Component library  | Angular Material                   |
| Reactive streams   | RxJS 7                             |
| Charts             | Chart.js + ng2-charts              |
| Styling            | SCSS (no utility framework)        |
| Auth               | JWT in-memory — never localStorage |
| Real-time          | Native WebSocket wrapped as RxJS   |
| Build              | Angular CLI + Nginx Docker image   |

## Project structure

```
dashboard/
├── src/
│   ├── app/
│   │   ├── core/
│   │   │   ├── guards/          # authGuard — blocks unauthenticated routes
│   │   │   ├── interceptors/    # authInterceptor — attaches Bearer token, handles 401
│   │   │   └── services/
│   │   │       ├── auth.service.ts        # JWT lifecycle (in-memory only)
│   │   │       ├── api.service.ts         # REST client (GET /nodes /jobs /metrics /audit)
│   │   │       └── websocket.service.ts   # WS factory for /ws/jobs/{id}/logs and /ws/metrics
│   │   ├── shared/
│   │   │   └── models/          # TypeScript mirror of Go coordinator types
│   │   ├── shell/
│   │   │   └── shell.component  # Navigation sidebar + router outlet
│   │   └── features/
│   │       ├── auth/            # LoginComponent
│   │       ├── nodes/           # NodeListComponent (auto-refreshes every 10 s)
│   │       ├── jobs/            # JobListComponent, JobDetailComponent + live log viewer
│   │       ├── metrics/         # ClusterMetricsComponent (WebSocket + Chart.js)
│   │       ├── audit/           # AuditLogComponent (paginated, filterable)
│   │       └── ml/              # Feature-18 ML module (lazy-loaded /ml/*)
│   │           # Datasets / Models / Services / Pipelines list + detail
│   │           # See "ML module" section below.
│   ├── environments/            # environment.ts / environment.production.ts
│   └── styles.scss              # Global SCSS + Material dark theme + badge utilities
├── e2e/
│   ├── fixtures/
│   │   ├── cluster.fixture.ts   # Token reader, health/node waiters, job submitter
│   │   └── auth.fixture.ts      # Login-via-textarea Playwright fixture
│   └── specs/                   # Playwright E2E test specs (10 files)
├── playwright.config.ts         # Playwright config (Chromium, auto-starts ng serve)
├── Dockerfile                   # Two-stage: Node builder → Nginx runtime
├── nginx.conf                   # SPA serving + /api and /ws reverse proxy to coordinator
└── karma.conf.js
```

## Development

### Prerequisites
- Node.js 20+
- Angular CLI: `npm install -g @angular/cli`

### Run locally
```bash
npm ci
ng serve
# Dashboard at http://localhost:4200
# Coordinator must be running at the URL set in environment.ts
```

### Unit tests
```bash
ng test                         # Karma + Jasmine (watch mode)
ng test --watch=false --browsers=ChromeHeadless   # CI mode
ng test --watch=false --browsers=ChromeHeadless --code-coverage  # with coverage
```

#### Coverage thresholds

Coverage minimums: **85% statements · 60% branches · 85% functions · 85% lines**.
These match the Go `internal/` threshold policy so a failing dashboard build
surfaces the same way a failing Go build does.

Enforcement is done by `scripts/check-dashboard-coverage.sh` — it parses the
HTML report emitted by `ng test --code-coverage`. This is necessary because
`@angular-devkit/build-angular:karma` overrides the coverage reporter list
declared in `karma.conf.js` and ignores its `check:` block entirely. The
thresholds are duplicated in both `karma.conf.js` (documentation) and the
script (the actual gate) — keep them in sync.

The same script runs both in `make check` and as a dedicated step in
`.github/workflows/ci.yml`.

### E2E tests (full-stack)

E2E tests use Playwright to drive a real browser against the live dashboard backed by
a real coordinator + node cluster. 78 tests cover login, navigation, nodes, jobs,
metrics (WebSocket), and audit log.

```bash
# One command — boots cluster, runs tests, tears down (from project root)
make test-e2e

# With visible browser
make test-e2e-headed

# Playwright interactive UI (debug/step through tests)
make test-e2e-ui

# If the cluster is already running, run tests directly
npm run e2e

# View the HTML report after a run
npm run e2e:report
```

The cluster is started via `docker-compose.e2e.yml` (an overlay on the base compose
file that exposes the coordinator HTTP API on `:8080` and writes the root token to
`state/root-token`). See `scripts/run-e2e.sh` for the full lifecycle.

### Production build
```bash
ng build --configuration production
# Output: dist/helion-dashboard/browser/
```

## Docker

```bash
# Build
docker build -t helion-dashboard .

# Run (coordinator on localhost:8080)
docker run -p 3000:80 \
  -e COORDINATOR_HOST=coordinator \
  -e COORDINATOR_PORT=8080 \
  helion-dashboard
```

## Security contract

- **JWT stored in memory only** — never written to `localStorage`, `sessionStorage`, or cookies.
- Token is lost on page refresh — the user must re-enter the root token from coordinator stdout.
- The HTTP interceptor attaches `Authorization: Bearer <token>` to every outgoing request.
- A 401 response from any endpoint clears the token and redirects to `/login`.
- Route guards block access to all protected routes without a valid in-memory token.
- Nginx serves a strict Content-Security-Policy header: no inline scripts, no eval, same-origin only.
- Auto-logout fires 30 s before JWT expiry (configurable via `jwtExpiryBufferMs`).
- **WebSocket first-message auth** — the JWT is sent as the first frame after `onopen`
  (`{"type":"auth","token":"..."}`), never as a URL query parameter. The server replies
  `{"type":"auth_ok"}` before streaming data. This keeps tokens out of server logs and
  browser history.
- **Generic error messages** — all error banners display user-friendly text. Raw error
  details are logged to `console.error` for developer diagnostics only.

## Environment variables

| Variable           | Default        | Description                              |
|--------------------|----------------|------------------------------------------|
| `COORDINATOR_HOST` | `coordinator`  | Nginx upstream host (Docker service name)|
| `COORDINATOR_PORT` | `8080`         | Nginx upstream port                      |

In `environment.ts` (local dev):

| Key                 | Default                     | Description                  |
|---------------------|-----------------------------|------------------------------|
| `coordinatorUrl`    | `http://localhost:8080`     | REST base URL                |
| `wsUrl`             | `ws://localhost:8080`       | WebSocket base URL           |
| `tokenRefreshMs`    | `5000`                      | Node page poll interval (ms) |
| `jwtExpiryBufferMs` | `30000`                     | Auto-logout before expiry    |

## API endpoints consumed

| Method | Path                       | Component               |
|--------|----------------------------|-------------------------|
| GET    | `/nodes`                   | NodeListComponent       |
| GET    | `/jobs`                    | JobListComponent        |
| GET    | `/jobs/{id}`               | JobDetailComponent      |
| POST   | `/jobs`                    | (future submit form)    |
| GET    | `/metrics`                 | ClusterMetricsComponent |
| GET    | `/audit`                   | AuditLogComponent       |
| WS     | `/ws/jobs/{id}/logs`       | JobDetailComponent      |
| WS     | `/ws/metrics`              | ClusterMetricsComponent |
| WS     | `/ws/events`               | EventFeedComponent      |
| GET    | `/workflows`               | WorkflowListComponent   |
| GET    | `/workflows/{id}`          | WorkflowDetailComponent |
| GET    | `/api/analytics/throughput` | AnalyticsDashboardComponent |
| GET    | `/api/analytics/node-reliability` | AnalyticsDashboardComponent |
| GET    | `/api/analytics/retry-effectiveness` | AnalyticsDashboardComponent |
| GET    | `/api/analytics/queue-wait` | AnalyticsDashboardComponent |
| GET    | `/api/analytics/workflow-outcomes` | AnalyticsDashboardComponent |
| GET    | `/api/datasets`            | MlDatasetsComponent     |
| POST   | `/api/datasets`            | RegisterDatasetDialogComponent |
| DELETE | `/api/datasets/{name}/{version}` | MlDatasetsComponent |
| GET    | `/api/models`              | MlModelsComponent       |
| DELETE | `/api/models/{name}/{version}` | MlModelsComponent   |
| GET    | `/api/services`            | MlServicesComponent (5 s poll) |
| GET    | `/workflows/{id}/lineage`  | MlPipelineDetailComponent (DAG) |

## Analytics module

The analytics dashboard is a standalone Angular component at `/analytics`, lazy-loaded
via `app.routes.ts`. It queries the `/api/analytics/*` REST endpoints (which read from
PostgreSQL) and renders:

- **Throughput chart** — line chart: completed vs failed jobs per hour
- **Queue wait chart** — line chart: avg and p95 pending→running wait per hour
- **Node reliability table** — Material table with failure rates and stale counts
- **Retry effectiveness** — card grid: first-attempt vs retried outcomes
- **Workflow outcomes** — stacked bar chart: success/failure per day

All views share a date-range picker (default: last 7 days). Charts use Chart.js with
the same dark theme as the operational metrics page.

The analytics module only appears functional when the coordinator has analytics enabled
(`HELION_ANALYTICS_DSN` set). When disabled, the API endpoints are not registered and
the dashboard will show connection errors.

## ML module

Feature 18 adds four lazy-loaded routes under `/ml/*`, wired
through `app.routes.ts`. The module is the dashboard surface for
the full ML pipeline (features 11–19); see
[ml-pipelines.md](../guides/ml-pipelines.md) for the backend story.

| Route | Component | Reads | Writes | What it shows |
|---|---|---|---|---|
| `/ml/datasets` | `MlDatasetsComponent` | `GET /api/datasets` | `POST /api/datasets` (via `RegisterDatasetDialog`), `DELETE /api/datasets/{name}/{version}` (confirm prompt) | Paginated dataset list with URI + size + tags; register-via-form modal with URI scheme hint; delete with `window.confirm` gate |
| `/ml/models` | `MlModelsComponent` | `GET /api/models` | `DELETE /api/models/{name}/{version}` | Model table with lineage cell (source_job_id link + source_dataset link into `/ml/datasets?name=X&version=Y`) and metric pills rendering `accuracy`, `f1_macro`, etc. |
| `/ml/services` | `MlServicesComponent` | `GET /api/services` every 5 s (`interval(5000).pipe(startWith(0), switchMap(...))`) | — | Live inference endpoints; READY / UNHEALTHY chip; upstream URL (`mono ellipsis` with title tooltip); back-link into `/jobs/{id}` |
| `/ml/pipelines` | `MlPipelinesComponent` | `GET /workflows` | — | Paginated workflow table with a "View DAG" action that routes into the detail page |
| `/ml/pipelines/:id` | `MlPipelineDetailComponent` | `GET /workflows/{id}/lineage` | — | Mermaid-rendered DAG with dependency (solid) + artifact (dashed) arrows; job cards with status chips, command, deps, outputs (with byte-formatted sizes), and `models_produced` chips linking back to `/ml/models` |

### ML security model

The ML views inherit the dashboard's standard auth posture (JWT
in-memory, route guards, 401-triggered logout). Two feature-19
additions worth noting:

- **Workflow-scoped tokens** — `examples/ml-iris/submit.py`
  mints a short-lived `job`-role JWT per workflow via
  `POST /admin/tokens` and injects it into each job's env,
  rather than leaking the operator's root admin token. The
  dashboard itself doesn't interact with this flow, but the
  `/events` + `/audit` pages surface `workflow:<id>` as the
  actor on register / delete rows emitted by in-workflow
  scripts.
- **Event payload observers** — `ml.resolve_failed` and
  `job.unschedulable` (with `reason` field distinguishing
  `no_healthy_node` / `no_matching_label` /
  `all_matching_unhealthy`) are emitted on the event bus and
  audit log. Feature 18's Pipelines event rollup that renders
  these as inline badges on pipeline rows is deferred
  (`deferred/26-pipelines-event-integration.md`); today the raw
  events are visible on `/events`.

## Submit module

Feature 22 adds a `/submit` route group — the dashboard's single
place to start a run. Four lazy-loaded tabs under one shell:

| Route | Component | Writes | What it shows |
|---|---|---|---|
| `/submit` | `SubmitShellComponent` | — | Tab bar + `<router-outlet>`; redirects `/submit` → `/submit/job` |
| `/submit/job` | `SubmitJobComponent` | `POST /jobs` | Reactive form mirroring `internal/api/handlers_jobs.go#SubmitRequest`. Fields: id, command, args, env (typed entries with a secret toggle), timeout, priority, gpus, node_selector. Client-side env-key denylist + shape validators; Validate → Preview → Submit flow |
| `/submit/workflow` | `SubmitWorkflowComponent` | `POST /workflows` | Textarea editor accepting JSON bodies matching `SubmitWorkflowRequest`. Pure-function shape validator (`workflow-shape-validator.ts`) covers required fields, per-job shape, depends_on resolvability, env denylist |
| `/submit/ml-workflow` | `SubmitMlWorkflowComponent` | `POST /workflows` | Template picker (Iris / MNIST / Custom) above the same editor. MNIST template preserves feature-21 heterogeneous scheduling (`train → runtime: rust`) + the `PATH` env the Rust runtime env_clear()s |
| `/submit/dag-builder` | `SubmitDagBuilderComponent` | `POST /workflows` | Form-driven DAG builder: per-job rows on the left, editor on the right, live JSON preview at the bottom. depends_on is a multi-select over the other job names; removing an upstream cascades into downstream depends_on |

### Submit security model

Every submit tab enforces the same client-side posture:

- **Two-click Validate → Preview → Submit** is deliberate: the
  Preview modal renders the exact JSON that will be POSTed so
  the operator sees the committed shape, and requires a second
  click to fire the network call. Accidental submits from
  muscle memory should be hard.
- **Client-side env denylist** mirrors the future feature 25
  server-side list (`LD_*`, `DYLD_*`, `GCONV_PATH`,
  `GIO_EXTRA_MODULES`, `HOSTALIASES`, `NLSPATH`, `RES_OPTIONS`).
  This is UX, not a security boundary — any authenticated
  client can POST raw JSON directly. Load-bearing rejection
  lands with feature 25 on the server.
- **Secret env toggle** flips the value `<input>` to
  `type="password"` so clipboard-glance / screenshare leakage
  is reduced. Until feature 26 lands, the coordinator still
  echoes secret values on a subsequent `GET /jobs/{id}`; every
  secret-flagged row shows a tooltip spelling this out.
- **Validate button runs client-side only** today. When
  feature 24 (dry-run preflight) ships, the Validate handler
  swaps to `POST /jobs?dry_run=true` /
  `POST /workflows?dry_run=true` — the shape validator
  becomes a fallback for offline typing.

### E2E

Four iris-specific Angular + Playwright test files cover the
ML module:

- `ml-datasets.component.spec.ts`, `ml-models.component.spec.ts`,
  `ml-services.component.spec.ts`, `ml-pipelines.component.spec.ts`
  (unit, Karma + Jasmine).
- [`dashboard/e2e/specs/ml-iris.spec.ts`](../../dashboard/e2e/specs/ml-iris.spec.ts)
  — Playwright UI acceptance for feature 19. Submits the iris
  workflow + serve job via REST in `beforeAll`, then asserts
  all five dashboard views render the resulting state
  correctly.
- [`dashboard/e2e/specs/ml-iris-walkthrough.spec.ts`](../../dashboard/e2e/specs/ml-iris-walkthrough.spec.ts)
  — paced walkthrough spec gated behind
  `E2E_RECORD_IRIS_WALKTHROUGH=1`. Not part of CI; used to
  regenerate `docs/e2e-iris-run.mp4`.

