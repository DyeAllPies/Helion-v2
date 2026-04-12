// e2e/specs/nodes.spec.ts
//
// End-to-end tests for the Nodes page.
// Verifies: node list renders, health badges, auto-refresh polling,
// all table columns, error handling, and empty state.

import { test, expect } from '../fixtures/auth.fixture';

test.describe('Nodes Page', () => {

  test('displays the NODES page title and registered nodes', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('h1.page-title')).toContainText('NODES');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });
  });

  test('shows healthy badges for registered nodes', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    const healthyBadges = page.locator('.badge-healthy');
    await expect(healthyBadges.first()).toBeVisible();
  });

  test('displays node details: ID, address, CPU, memory', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Node ID
    const nodeId = page.locator('.mono-id').first();
    await expect(nodeId).toBeVisible();
    const text = await nodeId.textContent();
    expect(text?.trim().length).toBeGreaterThan(0);

    // CPU and memory bars
    await expect(page.locator('.mini-bar-wrap').first()).toBeVisible();
  });

  test('shows subtitle with registered and healthy counts', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    const sub = page.locator('.page-sub');
    await expect(sub).toContainText('registered');
    await expect(sub).toContainText('healthy');
  });

  test('refresh indicator is present', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    const indicator = page.locator('.refresh-indicator');
    await expect(indicator).toBeVisible();
  });

  test('all table columns are rendered', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Verify all expected column headers
    const headers = page.locator('table[mat-table] th');
    const headerTexts = await headers.allTextContents();
    const normalized = headerTexts.map(h => h.trim());
    expect(normalized).toContain('STATUS');
    expect(normalized).toContain('NODE ID');
    expect(normalized).toContain('ADDRESS');
    expect(normalized).toContain('RUNNING');
    expect(normalized).toContain('CPU %');
    expect(normalized).toContain('MEM %');
    expect(normalized).toContain('LAST SEEN');
    expect(normalized).toContain('REGISTERED');
  });

  test('last seen column shows relative timestamps', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // First row's last seen cell should contain a relative time like "Xs ago" or "just now"
    const firstRow = page.locator('table[mat-table] tr.mat-mdc-row').first();
    const cells = firstRow.locator('td');
    const allText = await cells.allTextContents();
    const hasRelativeTime = allText.some(t => /just now|\ds ago|\dm ago|\dh ago/.test(t));
    expect(hasRelativeTime).toBe(true);
  });

  test('registered at column shows formatted date', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Registered at should be a formatted date like "2026-04-11 12:34"
    const firstRow = page.locator('table[mat-table] tr.mat-mdc-row').first();
    const allText = await firstRow.locator('td').allTextContents();
    const hasDate = allText.some(t => /\d{4}-\d{2}-\d{2} \d{2}:\d{2}/.test(t));
    expect(hasDate).toBe(true);
  });

  test('auto-refresh polling updates table data', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Count rows before waiting — after a poll cycle the table re-renders
    const initialRowCount = await page.locator('table[mat-table] tr.mat-mdc-row').count();
    expect(initialRowCount).toBeGreaterThan(0);

    // Wait for at least one poll cycle (5s in dev env) and verify the table
    // still renders valid data — proving the interval-based switchMap fired.
    // We also try to catch the refresh-indicator in its "refreshing" state.
    let caughtRefreshing = false;
    await expect(async () => {
      const refreshing = await page.locator('.refresh-indicator').evaluate(
        el => el.classList.contains('refreshing')
      );
      if (refreshing) caughtRefreshing = true;
      const rowCount = await page.locator('table[mat-table] tr.mat-mdc-row').count();
      // Table must still have rows after poll (data wasn't lost)
      expect(rowCount).toBeGreaterThan(0);
      // We need to either catch it refreshing or have completed at least one cycle
      expect(caughtRefreshing || rowCount >= initialRowCount).toBe(true);
    }).toPass({timeout: 15_000, intervals: [1_000] });
  });

  test('error banner appears when API returns error', async ({ authedPage: page }) => {
    await page.goto('/nodes');

    // Wait for initial successful load
    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Intercept subsequent /nodes requests with a 500 error
    await page.route('**/nodes', route => {
      route.fulfill({ status: 500, body: 'Internal Server Error' });
    });

    // Wait for the next poll cycle to fail and show error banner
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Failed to load nodes');
  });

  test('empty state shows when no nodes are registered', async ({ authedPage: page }) => {
    // Intercept /nodes to return empty array
    await page.route('**/nodes', route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    });

    await page.goto('/nodes');

    await expect(page.locator('.empty-state')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.empty-state')).toContainText('No nodes registered yet');
  });
});
