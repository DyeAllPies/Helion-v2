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
// ── Feature 39 — strict TLS trust for the E2E coordinator ────────────────
//
// The E2E coordinator serves REST over hybrid-PQC TLS with a self-signed
// CA that's fresh per cluster boot. Rather than broadly disabling cert
// validation (`--ignore-certificate-errors`, `ignoreHTTPSErrors: true`),
// we compute the coordinator's SubjectPublicKeyInfo hash at Playwright
// startup and pin EXACTLY that SPKI via Chromium's
// `--ignore-certificate-errors-spki-list=<hash>` flag.
//
// Safety property: a swapped or MITMed cert fails the TLS handshake. The
// pin is effective for the duration of this E2E run only — nothing is
// written to the OS trust store, nothing leaks into subsequent runs.
// Production + unit tests exercise the real cert-chain validation path
// (see internal/pqcrypto + tests/integration/security).
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
// Run:  npx playwright test
// UI:   npx playwright test --ui

import { defineConfig, devices } from '@playwright/test';
import { execSync } from 'node:child_process';

// Allow up to 10% of tests to fail before aborting the run.
// This balances catching regressions early vs. seeing the full picture.
// Some downstream tests depend on earlier ones (e.g. job detail tests
// need the job list to work), so cascading failures are expected when
// a core feature breaks.
const totalSpecs = 165;
const maxFailures = Math.ceil(totalSpecs * 0.1); // ~17 failures

/**
 * Resolve the live coordinator cert's SubjectPublicKeyInfo hash and
 * return a `--ignore-certificate-errors-spki-list=<hash>` browser
 * arg. Returns an empty args list if the cluster isn't reachable —
 * the test itself will then fail with a clear cert error, which is a
 * better UX than silent broad bypass.
 *
 * The hash format Chromium expects: base64-encoded SHA-256 of the
 * server cert's SubjectPublicKeyInfo (DER-encoded). Fresh per
 * coordinator boot because the overlay regenerates the CA on every
 * `docker compose down -v`.
 */
function coordinatorSPKIArg(): string[] {
  const host = process.env['E2E_COORD_HOST'] || '127.0.0.1:8080';
  try {
    const hash = execSync(
      // openssl reads the TLS handshake's server cert, extracts the
      // public key, DER-encodes it, SHA-256 hashes it, base64s it.
      // All one line so we get a single trimmable string back.
      `echo | openssl s_client -connect ${host} -servername localhost 2>/dev/null ` +
        `| openssl x509 -noout -pubkey 2>/dev/null ` +
        `| openssl pkey -pubin -outform DER 2>/dev/null ` +
        `| openssl dgst -sha256 -binary 2>/dev/null ` +
        `| base64 -w0`,
      { encoding: 'utf-8', timeout: 5000, shell: 'bash' },
    ).trim();
    if (hash === '') return [];
    return [`--ignore-certificate-errors-spki-list=${hash}`];
  } catch {
    // Coordinator not up yet (common when running `playwright test`
    // without the cluster up). Don't fail config load — the test
    // itself will surface the connectivity problem.
    return [];
  }
}

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
    // No ignoreHTTPSErrors — we pin the coordinator's SPKI via
    // Chromium's launch args below so cert validation stays
    // strict. Cert-chain validation semantics are unit-tested in
    // internal/pqcrypto + tests/integration/security.
  },

  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: {
          args: coordinatorSPKIArg(),
        },
      },
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
