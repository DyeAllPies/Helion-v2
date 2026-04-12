# Helion Dashboard — Angular 21

The control-plane UI for the Helion v2 distributed job scheduler.

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
│   │       └── audit/           # AuditLogComponent (paginated, filterable)
│   ├── environments/            # environment.ts / environment.production.ts
│   └── styles.scss              # Global SCSS + Material dark theme + badge utilities
├── e2e/
│   ├── fixtures/
│   │   ├── cluster.fixture.ts   # Token reader, health/node waiters, job submitter
│   │   └── auth.fixture.ts      # Login-via-textarea Playwright fixture
│   └── specs/                   # 97 Playwright E2E test specs (6 files)
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
```

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

