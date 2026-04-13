// e2e/fixtures/auth.fixture.ts
//
// Playwright fixture that provides a shared authenticated page per spec file.
//
// The Angular dashboard stores the JWT in memory only (never localStorage),
// so we can't use Playwright's storageState. Instead, we authenticate once
// per worker (spec file) in beforeAll and share the page across all tests
// in that file. This mirrors real user behaviour: login once, navigate
// via sidebar links, never re-enter the token.
//
// Tests within a file run serially on the shared page. A failure in one
// test may affect subsequent tests — this is acceptable for E2E where
// cascading failures signal a real problem.

import { test as base, expect, Page, BrowserContext } from '@playwright/test';
import { getRootToken } from './cluster.fixture';

export { expect };

/**
 * Paste the root JWT into the login textarea and click AUTHENTICATE.
 * Waits until the page navigates to /nodes (the default post-login route).
 */
export async function authenticate(page: Page): Promise<void> {
  const token = getRootToken();
  await page.goto('/login');
  await page.waitForSelector('textarea.token-input', { state: 'visible' });
  await page.fill('textarea.token-input', token);
  await page.click('button.login-btn');
  await page.waitForURL('**/nodes', { timeout: 15_000 });
}

/** Map of route paths to sidebar link text for Angular router navigation. */
const NAV_LINKS: Record<string, string> = {
  '/nodes':     'Nodes',
  '/jobs':      'Jobs',
  '/workflows': 'Workflows',
  '/events':    'Events',
  '/metrics':   'Metrics',
  '/audit':     'Audit',
};

/**
 * Navigate to a route without losing the in-memory JWT.
 * Uses sidebar link clicks (Angular router) instead of page.goto()
 * which causes a full page reload and destroys the auth state.
 */
export async function navigateTo(page: Page, path: string): Promise<void> {
  const linkText = NAV_LINKS[path];
  if (!linkText) {
    await authenticate(page);
    return;
  }

  // If we're already on this route, bounce to a different route first
  // to force Angular to destroy + recreate the component. This resets
  // local state (filters, pagination) like a real user clicking away
  // and back.
  const currentPath = new URL(page.url()).pathname;
  if (currentPath === path) {
    const bounceLink = path === '/nodes' ? 'Jobs' : 'Nodes';
    await page.click(`a.nav-link >> text=${bounceLink}`);
    await page.waitForTimeout(200);
  }

  try {
    await page.click(`a.nav-link >> text=${linkText}`);
    await page.waitForURL(`**${path}`, { timeout: 5_000 });
  } catch {
    // Shared page may be in an inconsistent state from a prior test.
    await authenticate(page);
    await page.click(`a.nav-link >> text=${linkText}`);
    await page.waitForURL(`**${path}`, { timeout: 5_000 });
  }
}

/**
 * Extended Playwright test fixture that provides a shared `authedPage`.
 *
 * The page is created once per worker and authenticated in the first test.
 * All tests in the same spec file share this page — login happens once,
 * just like a real user session.
 */
export const test = base.extend<{ authedPage: Page }, { sharedContext: BrowserContext; sharedPage: Page }>({
  // Worker-scoped: one context + page per spec file.
  sharedContext: [async ({ browser }, use) => {
    const ctx = await browser.newContext({
      recordVideo: process.env['E2E_VIDEO']
        ? { dir: 'test-results' }
        : undefined,
    });
    await use(ctx);
    await ctx.close();
  }, { scope: 'worker' }],

  sharedPage: [async ({ sharedContext }, use) => {
    const page = await sharedContext.newPage();
    await authenticate(page);
    await use(page);
  }, { scope: 'worker' }],

  // Test-scoped: just returns the shared page.
  authedPage: async ({ sharedPage }, use) => {
    await use(sharedPage);
  },
});

/**
 * Isolated test fixture — authenticates a fresh page per test.
 * Use this for destructive tests (error states, empty states) that
 * leave the page in a broken state and would affect subsequent tests.
 */
export const isolatedTest = base.extend<{ authedPage: Page }>({
  authedPage: async ({ page }, use) => {
    await authenticate(page);
    await use(page);
  },
});
