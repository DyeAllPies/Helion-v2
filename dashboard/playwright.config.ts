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
// Video recording: set E2E_VIDEO=1 to record all tests. Playwright
// writes one webm per *context*, so the worker-scoped fixture
// produces one long video covering most specs plus small per-test
// videos for the `isolatedTest` fixture (login, route guards).
// Stitch chronologically with ffmpeg:
//   find test-results -name "*.webm" -printf "%T@ %p\n" | sort -n \
//     | awk '{print "file '\''" $2 "'\''"}' > concat.txt
//   ffmpeg -y -f concat -safe 0 -i concat.txt \
//     -c:v libx264 -preset medium -crf 23 -pix_fmt yuv420p \
//     -vf 'fps=25,scale=800:450' e2e-full-run.mp4
//   mv e2e-full-run.mp4 ../docs/e2e-full-run.mp4
//
// NOTE: the full suite now includes ml-iris.spec.ts (feature 19)
// which requires Python-capable nodes. Start the cluster with the
// iris overlay before recording the full-run video:
//   COMPOSE_PROFILES=analytics,ml docker compose \
//     -f ../docker-compose.yml -f ../docker-compose.e2e.yml \
//     -f ../docker-compose.iris.yml up -d --build
//
// Run:  npx playwright test
// UI:   npx playwright test --ui

import { defineConfig, devices } from '@playwright/test';

// Allow up to 10% of tests to fail before aborting the run.
// This balances catching regressions early vs. seeing the full picture.
// Some downstream tests depend on earlier ones (e.g. job detail tests
// need the job list to work), so cascading failures are expected when
// a core feature breaks.
const totalSpecs = 165;
const maxFailures = Math.ceil(totalSpecs * 0.1); // ~17 failures

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
