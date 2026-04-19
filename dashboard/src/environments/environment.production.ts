// src/environments/environment.production.ts
export const environment = {
  production: true,
  coordinatorUrl: '',       // same-origin — Nginx proxies /api → coordinator
  wsUrl:          '',       // same-origin — Nginx proxies /ws  → coordinator
  tokenRefreshMs: 10_000,
  // Analytics in prod polls half as often as dev (still real-time
  // by dashboard standards, still purely read-only). See
  // environment.ts for the write-rate invariant.
  analyticsRefreshMs: 5_000,
  wsMetricsIntervalMs: 5_000,
  jwtExpiryBufferMs: 30_000,
};
