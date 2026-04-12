// e2e/fixtures/auth.fixture.ts
//
// Playwright fixture that extends the base test with a pre-authenticated
// page.  Login is performed once per worker via the token-in-textarea flow,
// then the token is injected into subsequent tests via storageState.
//
// Because AuthService stores the JWT only in memory (never localStorage),
// we can't use Playwright's storageState for persistence across navigations.
// Instead, every test that needs auth calls `authenticate(page)`.

import { test as base, expect, Page } from '@playwright/test';
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

  // Fill the token textarea
  await page.fill('textarea.token-input', token);

  // Click the authenticate button
  await page.click('button.login-btn');

  // Wait for redirect to /nodes (post-login default)
  await page.waitForURL('**/nodes', { timeout: 15_000 });
}

/** Map of route paths to sidebar link text for Angular router navigation. */
const NAV_LINKS: Record<string, string> = {
  '/nodes':   'Nodes',
  '/jobs':    'Jobs',
  '/metrics': 'Metrics',
  '/audit':   'Audit',
};

/**
 * Navigate to a route without losing the in-memory JWT.
 * Uses sidebar link clicks (Angular router) instead of page.goto()
 * which causes a full page reload and destroys the auth state.
 * Falls back to re-authentication for routes not in the sidebar.
 */
export async function navigateTo(page: Page, path: string): Promise<void> {
  const linkText = NAV_LINKS[path];
  if (linkText) {
    await page.click(`a.nav-link >> text=${linkText}`);
    await page.waitForURL(`**${path}`, { timeout: 5_000 });
  } else if (path.startsWith('/jobs/')) {
    // Job detail — navigate via jobs list link or direct URL with re-auth
    await authenticate(page);
    await page.click(`a.nav-link >> text=Jobs`);
    await page.waitForURL('**/jobs', { timeout: 5_000 });
  } else {
    // Unknown route — re-authenticate and goto
    await authenticate(page);
  }
}

/**
 * Extended Playwright test fixture that provides an `authedPage` — a page
 * that has already completed the login flow.
 */
export const test = base.extend<{ authedPage: Page }>({
  authedPage: async ({ page }, use) => {
    await authenticate(page);
    await use(page);
  },
});
