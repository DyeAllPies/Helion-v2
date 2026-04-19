// src/environments/environment.ts
// Development environment configuration.
// In production the Angular build swaps this for environment.production.ts.

export const environment = {
  production: false,
  coordinatorUrl: '/api',                          // proxied to coordinator via ng serve proxy
  wsUrl:          '',                             // same-origin (ng serve proxy handles WS)
  tokenRefreshMs: 5_000,                        // poll interval for nodes page (ms)
  // Analytics polls faster so the MNIST walkthrough shows each
  // job completion landing on the chart with minimal lag. The
  // read path (GROUP BY on a job_summary index) is cheap; this
  // does NOT change the analytics sink's write rate — events
  // still batch every 200 ms on the server side.
  analyticsRefreshMs: 2_000,
  wsMetricsIntervalMs: 5_000,                   // coordinator pushes metrics every 5 s
  jwtExpiryBufferMs: 30_000,                    // logout 30 s before JWT expiry
};
