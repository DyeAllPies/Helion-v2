// playwright.config.ts
//
// E2E test configuration for the Helion v2 dashboard.
//
// Prerequisites:
//   1. Cluster running: docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d
//   2. Dashboard dev server: ng serve  (port 4200)
//
// IMPORTANT: Tests submit jobs/workflows that persist in BadgerDB. If rerunning
// against an existing cluster, accumulated data may push new items off page 1
// and cause "element not found" failures. Always tear down with -v to reset:
//   docker compose -f docker-compose.yml -f docker-compose.e2e.yml down -v
//
// Run:  npx playwright test
// UI:   npx playwright test --ui

import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e/specs',
  fullyParallel: false,
  forbidOnly: !!process.env['CI'],
  retries: 0,
  workers: 1,
  reporter: process.env['CI']
    ? [['list'], ['html', { open: 'never' }], ['github']]
    : [['list'], ['html', { open: 'on-failure' }]],
  timeout: 30_000,
  expect: { timeout: 2_000 },

  use: {
    baseURL: process.env['E2E_BASE_URL'] || 'http://localhost:4200',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: process.env['E2E_VIDEO'] ? 'on' : 'retain-on-failure',
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
    command: 'npx ng serve --port 4200 --proxy-config proxy.conf.json',
    url: 'http://localhost:4200',
    reuseExistingServer: !process.env['CI'],
    timeout: 120_000,
  },
});
