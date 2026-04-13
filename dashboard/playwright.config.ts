// playwright.config.ts
//
// E2E test configuration for the Helion v2 dashboard.
//
// Prerequisites:
//   1. Cluster running: docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d
//   2. Dashboard dev server: ng serve  (port 4200)
//
// The E2E overlay uses a named Docker volume (e2e-state) instead of the host
// bind mount (./state), so tests use isolated storage and never pollute
// the user's local BadgerDB. Tear down with -v to reset:
//   docker compose -f docker-compose.yml -f docker-compose.e2e.yml down -v
//
// Video recording: set E2E_VIDEO=1 to record all tests. Videos are saved
// per-test in test-results/ and can be combined into a single MP4:
//   find test-results -name "*.webm" | sort | sed 's/^/file /' > concat.txt
//   ffmpeg -f concat -safe 0 -i concat.txt -c:v libx264 docs/e2e-full-run.mp4
//
// Run:  npx playwright test
// UI:   npx playwright test --ui

import { defineConfig, devices } from '@playwright/test';

// Allow up to 10% of tests to fail before aborting the run.
// This balances catching regressions early vs. seeing the full picture.
// Some downstream tests depend on earlier ones (e.g. job detail tests
// need the job list to work), so cascading failures are expected when
// a core feature breaks.
const totalSpecs = 122;
const maxFailures = Math.ceil(totalSpecs * 0.1); // ~10 failures

export default defineConfig({
  testDir: './e2e/specs',
  fullyParallel: false,
  forbidOnly: !!process.env['CI'],
  retries: 0,
  workers: 1,
  maxFailures,
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
