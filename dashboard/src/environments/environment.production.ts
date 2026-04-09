// src/environments/environment.production.ts
export const environment = {
  production: true,
  coordinatorUrl: '',       // same-origin — Nginx proxies /api → coordinator
  wsUrl:          '',       // same-origin — Nginx proxies /ws  → coordinator
  tokenRefreshMs: 10_000,
  wsMetricsIntervalMs: 5_000,
  jwtExpiryBufferMs: 30_000,
};
