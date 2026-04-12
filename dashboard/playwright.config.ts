// playwright.config.ts
//
// E2E test configuration for the Helion v2 dashboard.
//
// Prerequisites:
//   1. Cluster running: docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d
//   2. Dashboard dev server: ng serve  (port 4200)
//
// Run:  npx playwright test
// UI:   npx playwright test --ui

import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e/specs',
  fullyParallel: false,               // tests depend on shared cluster state
  forbidOnly: !!process.env['CI'],
  retries: process.env['CI'] ? 1 : 0,
  workers: 1,                         // sequential — one cluster
  reporter: process.env['CI']
    ? [['html', { open: 'never' }], ['github']]
    : [['html', { open: 'on-failure' }]],
  timeout: 60_000,

  use: {
    baseURL: process.env['E2E_BASE_URL'] || 'http://localhost:4200',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  // Optionally start the Angular dev server automatically.
  // Comment out if you prefer to start it manually.
  webServer: {
    command: 'npx ng serve --port 4200',
    url: 'http://localhost:4200',
    reuseExistingServer: !process.env['CI'],
    timeout: 120_000,
  },
});
