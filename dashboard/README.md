# Helion Dashboard — Angular 18

The control-plane UI for the Helion v2 distributed job scheduler.

## Tech stack

| Concern            | Choice                             |
|--------------------|------------------------------------|
| Framework          | Angular 18 (standalone components) |
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
├── Dockerfile                   # Two-stage: Node builder → Nginx runtime
├── nginx.conf                   # SPA serving + /api and /ws reverse proxy to coordinator
└── karma.conf.js
```

## Development

### Prerequisites
- Node.js 20+
- Angular CLI 18: `npm install -g @angular/cli@18`

### Run locally
```bash
npm ci
ng serve
# Dashboard at http://localhost:4200
# Coordinator must be running at the URL set in environment.ts
```

### Tests
```bash
ng test                         # Karma + Jasmine (watch mode)
ng test --watch=false --browsers=ChromeHeadless   # CI mode
```

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
