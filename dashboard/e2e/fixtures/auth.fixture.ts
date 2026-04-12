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
  await page.waitForURL('**/nodes', { timeout: 10_000 });
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
