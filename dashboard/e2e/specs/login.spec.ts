// e2e/specs/login.spec.ts
//
// End-to-end tests for the login flow.
// Verifies: token entry, validation, auth redirect, logout, token
// lifecycle (refresh loss, 401 auto-logout), and route guards.

import { test, expect } from '@playwright/test';
import { getRootToken } from '../fixtures/cluster.fixture';

test.describe('Login Flow', () => {

  test('redirects unauthenticated users to /login', async ({ page }) => {
    await page.goto('/nodes');
    await expect(page).toHaveURL(/\/login/);
  });

  test('shows the login page with brand and token input', async ({ page }) => {
    await page.goto('/login');

    // ASCII brand is visible
    await expect(page.locator('.ascii-brand')).toBeVisible();

    // Token textarea is present
    await expect(page.locator('textarea.token-input')).toBeVisible();

    // Authenticate button is disabled when textarea is empty
    const btn = page.locator('button.login-btn');
    await expect(btn).toBeDisabled();
  });

  test('authenticate button enables when token is typed', async ({ page }) => {
    await page.goto('/login');

    const btn = page.locator('button.login-btn');
    await expect(btn).toBeDisabled();

    await page.fill('textarea.token-input', 'some-text');
    await expect(btn).toBeEnabled();
  });

  test('rejects an invalid token', async ({ page }) => {
    await page.goto('/login');

    await page.fill('textarea.token-input', 'not-a-valid-jwt');
    await page.click('button.login-btn');

    // Error message should appear
    await expect(page.locator('.error-msg')).toBeVisible({ timeout: 5_000 });
    await expect(page.locator('.error-msg')).toContainText('invalid or expired');

    // Should still be on login page
    await expect(page).toHaveURL(/\/login/);
  });

  test('rejects an expired token', async ({ page }) => {
    // Craft a JWT with exp in the past (header.payload.signature)
    const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
      .replace(/=/g, '');
    const payload = btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) - 3600 }))
      .replace(/=/g, '');
    const expiredToken = `${header}.${payload}.fake-signature`;

    await page.goto('/login');
    await page.fill('textarea.token-input', expiredToken);
    await page.click('button.login-btn');

    await expect(page.locator('.error-msg')).toBeVisible({ timeout: 5_000 });
    await expect(page).toHaveURL(/\/login/);
  });

  test('authenticates with valid root token and redirects to /nodes', async ({ page }) => {
    const token = getRootToken();

    await page.goto('/login');
    await page.fill('textarea.token-input', token);
    await page.click('button.login-btn');

    // Should redirect to /nodes
    await expect(page).toHaveURL(/\/nodes/, { timeout: 15_000 });

    // Sidebar should be visible with HELION brand
    await expect(page.locator('.brand-name')).toContainText('HELION');
  });

  test('logout clears token and returns to /login', async ({ page }) => {
    const token = getRootToken();

    // Login first
    await page.goto('/login');
    await page.fill('textarea.token-input', token);
    await page.click('button.login-btn');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 15_000 });

    // Click logout
    await page.click('button.logout-btn');

    // Should be back on login page
    await expect(page).toHaveURL(/\/login/, { timeout: 5_000 });

    // Attempting to navigate to a protected route should redirect back
    await page.goto('/nodes');
    await expect(page).toHaveURL(/\/login/);
  });

  test('page refresh loses in-memory token and redirects to login', async ({ page }) => {
    const token = getRootToken();

    // Login
    await page.goto('/login');
    await page.fill('textarea.token-input', token);
    await page.click('button.login-btn');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 15_000 });

    // Hard refresh — token is in memory only, so it must be lost
    await page.reload();

    // Should redirect to login because authGuard checks token
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });
  });

  test('401 response from API triggers auto-logout', async ({ page }) => {
    const token = getRootToken();

    // Login
    await page.goto('/login');
    await page.fill('textarea.token-input', token);
    await page.click('button.login-btn');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 15_000 });

    // Intercept the next /nodes request to return 401 (simulating token revocation)
    await page.route('**/nodes', route => {
      route.fulfill({ status: 401, body: 'Unauthorized' });
    });

    // Navigate away and back to trigger a new API call
    await page.goto('/jobs');
    await page.goto('/nodes');

    // The 401 should trigger the interceptor → auth.onUnauthorized() → redirect
    await expect(page).toHaveURL(/\/login/, { timeout: 15_000 });
  });
});

test.describe('Route Guards & Redirects', () => {

  test('all protected routes redirect to /login when unauthenticated', async ({ page }) => {
    const protectedRoutes = ['/nodes', '/jobs', '/metrics', '/audit'];
    for (const route of protectedRoutes) {
      await page.goto(route);
      await expect(page).toHaveURL(/\/login/);
    }
  });

  test('wildcard routes redirect to root', async ({ page }) => {
    const token = getRootToken();

    // Login first
    await page.goto('/login');
    await page.fill('textarea.token-input', token);
    await page.click('button.login-btn');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 15_000 });

    // Navigate to a nonexistent route
    await page.goto('/this-does-not-exist');

    // Should redirect to / which redirects to /nodes
    await expect(page).toHaveURL(/\/nodes/, { timeout: 10_000 });
  });

  test('root path redirects to /nodes when authenticated', async ({ page }) => {
    const token = getRootToken();

    await page.goto('/login');
    await page.fill('textarea.token-input', token);
    await page.click('button.login-btn');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 15_000 });

    // Navigate explicitly to root
    await page.goto('/');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 10_000 });
  });
});
