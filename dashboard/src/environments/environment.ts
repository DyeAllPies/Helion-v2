// src/environments/environment.ts
// Development environment configuration.
// In production the Angular build swaps this for environment.production.ts.

export const environment = {
  production: false,
  coordinatorUrl: 'http://localhost:8080',      // REST base URL
  wsUrl:          'ws://localhost:8080',         // WebSocket base URL
  tokenRefreshMs: 5_000,                        // poll interval for nodes page (ms)
  wsMetricsIntervalMs: 5_000,                   // coordinator pushes metrics every 5 s
  jwtExpiryBufferMs: 30_000,                    // logout 30 s before JWT expiry
};
