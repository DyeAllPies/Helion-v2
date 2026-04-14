// e2e/specs/navigation.spec.ts
//
// End-to-end tests for sidebar navigation.
// Verifies: all nav links work, active state highlights correctly,
// and the shell layout renders sidebar + main content.

import { test, expect, navigateTo } from '../fixtures/auth.fixture';

test.describe('Sidebar Navigation', () => {

  test('shell layout shows sidebar and main content area', async ({ authedPage: page }) => {
    // After login we're on /nodes
    await expect(page.locator('.sidebar')).toBeVisible();
    await expect(page.locator('.main-content')).toBeVisible();

    // Brand
    await expect(page.locator('.brand-name')).toContainText('HELION');
    await expect(page.locator('.brand-version')).toContainText('v2');
  });

  test('all seven nav items are present', async ({ authedPage: page }) => {
    const navLinks = page.locator('a.nav-link');
    await expect(navLinks).toHaveCount(7);

    const labels = await navLinks.allTextContents();
    const joined = labels.map(l => l.trim().toUpperCase()).join(' ');
    expect(joined).toContain('NODES');
    expect(joined).toContain('JOBS');
    expect(joined).toContain('WORKFLOWS');
    expect(joined).toContain('EVENTS');
    expect(joined).toContain('METRICS');
    expect(joined).toContain('AUDIT');
    expect(joined).toContain('ANALYTICS');
  });

  test('nodes link is active when on /nodes', async ({ authedPage: page }) => {
    await navigateTo(page, '/nodes');
    const activeLink = page.locator('a.nav-link--active');
    await expect(activeLink).toContainText(/nodes/i);
  });

  test('clicking Jobs navigates to /jobs', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs/, );
    await expect(page.locator('h1.page-title')).toContainText('JOBS');
  });

  test('clicking Metrics navigates to /metrics', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Metrics');
    await expect(page).toHaveURL(/\/metrics/, );
    await expect(page.locator('h1.page-title')).toContainText('METRICS');
  });

  test('clicking Audit navigates to /audit', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Audit');
    await expect(page).toHaveURL(/\/audit/, );
    await expect(page.locator('h1.page-title')).toContainText('AUDIT LOG');
  });

  test('clicking Nodes navigates back to /nodes', async ({ authedPage: page }) => {
    // Go somewhere else first
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs/, );

    // Now back to Nodes
    await page.click('a.nav-link >> text=Nodes');
    await expect(page).toHaveURL(/\/nodes/, );
    await expect(page.locator('h1.page-title')).toContainText('NODES');
  });

  test('active link updates on navigation', async ({ authedPage: page }) => {
    // Navigate to Metrics
    await page.click('a.nav-link >> text=Metrics');
    await expect(page).toHaveURL(/\/metrics/, );

    // Metrics link should be active
    const activeLink = page.locator('a.nav-link--active');
    await expect(activeLink).toContainText(/metrics/i);
  });
});
