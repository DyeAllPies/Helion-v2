// e2e/specs/navigation.spec.ts
//
// End-to-end tests for sidebar navigation.
// Verifies: all nav links work, active state highlights correctly,
// and the shell layout renders sidebar + main content.

import { test, expect } from '../fixtures/auth.fixture';

test.describe('Sidebar Navigation', () => {

  test('shell layout shows sidebar and main content area', async ({ authedPage: page }) => {
    // After login we're on /nodes
    await expect(page.locator('.sidebar')).toBeVisible();
    await expect(page.locator('.main-content')).toBeVisible();

    // Brand
    await expect(page.locator('.brand-name')).toContainText('HELION');
    await expect(page.locator('.brand-version')).toContainText('v2');
  });

  test('all four nav items are present', async ({ authedPage: page }) => {
    const navLinks = page.locator('a.nav-link');
    await expect(navLinks).toHaveCount(4);

    const labels = await navLinks.allTextContents();
    const cleaned = labels.map(l => l.trim().toUpperCase());
    expect(cleaned).toContain('NODES');
    expect(cleaned).toContain('JOBS');
    expect(cleaned).toContain('METRICS');
    expect(cleaned).toContain('AUDIT');
  });

  test('nodes link is active by default after login', async ({ authedPage: page }) => {
    const activeLink = page.locator('a.nav-link--active');
    await expect(activeLink).toContainText(/nodes/i);
  });

  test('clicking Jobs navigates to /jobs', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs/, { timeout: 10_000 });
    await expect(page.locator('h1.page-title')).toContainText('JOBS');
  });

  test('clicking Metrics navigates to /metrics', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Metrics');
    await expect(page).toHaveURL(/\/metrics/, { timeout: 10_000 });
    await expect(page.locator('h1.page-title')).toContainText('METRICS');
  });

  test('clicking Audit navigates to /audit', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Audit');
    await expect(page).toHaveURL(/\/audit/, { timeout: 10_000 });
    await expect(page.locator('h1.page-title')).toContainText('AUDIT LOG');
  });

  test('clicking Nodes navigates back to /nodes', async ({ authedPage: page }) => {
    // Go somewhere else first
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs/, { timeout: 10_000 });

    // Now back to Nodes
    await page.click('a.nav-link >> text=Nodes');
    await expect(page).toHaveURL(/\/nodes/, { timeout: 10_000 });
    await expect(page.locator('h1.page-title')).toContainText('NODES');
  });

  test('active link updates on navigation', async ({ authedPage: page }) => {
    // Navigate to Metrics
    await page.click('a.nav-link >> text=Metrics');
    await expect(page).toHaveURL(/\/metrics/, { timeout: 10_000 });

    // Metrics link should be active
    const activeLink = page.locator('a.nav-link--active');
    await expect(activeLink).toContainText(/metrics/i);
  });
});
