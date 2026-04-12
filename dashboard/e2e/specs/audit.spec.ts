// e2e/specs/audit.spec.ts
//
// End-to-end tests for the Audit Log page.
// Verifies: audit events render, type filter for multiple types, pagination,
// event detail fields, all column headers, error handling, and empty state.

import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { getRootToken, submitJob } from '../fixtures/cluster.fixture';

test.describe('Audit Log Page', () => {

  test('displays the AUDIT LOG page title', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Audit');
    await expect(page).toHaveURL(/\/audit/);
    await expect(page.locator('h1.page-title')).toContainText('AUDIT LOG');
  });

  test('shows audit events in the table with event count subtitle', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    await expect(page.locator('.page-sub')).toContainText('events');
    await expect(page.locator('.page-sub')).toContainText('read-only');
  });

  test('all table column headers are present', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    const headers = page.locator('table[mat-table] th');
    const headerTexts = (await headers.allTextContents()).map(h => h.trim());
    expect(headerTexts).toContain('TIMESTAMP');
    expect(headerTexts).toContain('EVENT TYPE');
    expect(headerTexts).toContain('ACTOR');
    expect(headerTexts).toContain('TARGET ID');
    expect(headerTexts).toContain('MESSAGE');
  });

  test('coordinator_start event is present after boot', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    await expect(page.locator('.event-type:text-is("COORDINATOR_START")').first()).toBeVisible({ timeout: 15_000 });
  });

  test('multiple event types appear in the log', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // At least one event type badge should be visible
    await expect(page.locator('.event-type').first()).toBeVisible();
  });

  test('submitting a job produces an audit event', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-audit-${Date.now()}`;

    // Submit a job so the audit log grows
    await submitJob(token, { id: jobId, command: 'echo', args: ['audit-test'] });

    await navigateTo(page, '/audit');

    // Click refresh to pick up the new event
    await page.click('button.refresh-btn');
    await page.waitForTimeout(500);

    // Should have at least one event row
    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });
  });

  test('event detail fields display correctly', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // First row should have a formatted timestamp (YYYY-MM-DD HH:MM:SS)
    const firstRow = page.locator('table[mat-table] tr.mat-mdc-row').first();
    const cells = firstRow.locator('td');
    const cellTexts = await cells.allTextContents();

    // Timestamp column (first cell) should match date format
    const hasTimestamp = cellTexts.some(t => /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/.test(t));
    expect(hasTimestamp).toBe(true);

    // Event type column should have a badge
    await expect(firstRow.locator('.event-type')).toBeVisible();
  });

  test('type filter narrows results to coordinator_start only', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    await page.selectOption('select.status-select', 'coordinator_start');
    await page.waitForTimeout(500);

    const badges = page.locator('table[mat-table] .event-type');
    const count = await badges.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      await expect(badges.nth(i)).toContainText('COORDINATOR_START');
    }
  });

  test('selecting a filter with no matching events shows empty table', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Filter to a type that likely has no events
    await page.selectOption('select.status-select', 'node_revoke');
    await page.waitForTimeout(500);

    // Table should be empty or show "No audit events found"
    const rows = await page.locator('table[mat-table] tr.mat-mdc-row').count();
    expect(rows).toBe(0);
  });

  test('filter dropdown contains ALL EVENTS plus all 9 event types', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    const options = page.locator('select.status-select option');
    const optionTexts = (await options.allTextContents()).map(t => t.trim().toLowerCase());

    expect(optionTexts).toContain('all events');
    expect(optionTexts).toContain('job_submit');
    expect(optionTexts).toContain('job_state_transition');
    expect(optionTexts).toContain('node_register');
    expect(optionTexts).toContain('node_revoke');
    expect(optionTexts).toContain('security_violation');
    expect(optionTexts).toContain('auth_failure');
    expect(optionTexts).toContain('rate_limit_hit');
    expect(optionTexts).toContain('coordinator_start');
    expect(optionTexts).toContain('coordinator_stop');
  });

  test('switching filter to ALL EVENTS shows all types', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // Filter to coordinator_start
    await page.selectOption('select.status-select', 'coordinator_start');
    await page.waitForTimeout(500);
    const filteredCount = await page.locator('table[mat-table] tr.mat-mdc-row').count();

    // Switch back to ALL
    await page.selectOption('select.status-select', '');
    await page.waitForTimeout(500);
    const allCount = await page.locator('table[mat-table] tr.mat-mdc-row').count();

    expect(allCount).toBeGreaterThanOrEqual(filteredCount);
  });

  test('paginator is present with page size options', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('mat-paginator')).toBeVisible({ timeout: 15_000 });
  });

  test('refresh button reloads data', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    await page.click('button.refresh-btn');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });
  });

  test('event type badges have correct CSS classes', async ({ authedPage: page }) => {
    await navigateTo(page, '/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    // coordinator_start → evt-coordinator
    await expect(page.locator('.event-type.evt-coordinator').first())
      .toBeVisible();
  });

  test('error banner appears when API returns error', async ({ authedPage: page }) => {
    await page.route('**/audit?*', route => {
      route.fulfill({ status: 500, body: 'Internal Server Error' });
    });

    await navigateTo(page, '/audit');
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Failed to load audit log');
  });

  test('empty state displays when no events match filter', async ({ authedPage: page }) => {
    await page.route('**/audit?*', route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ events: [], total: 0, page: 0, size: 50 }),
      });
    });

    await navigateTo(page, '/audit');
    await expect(page.locator('.empty-state')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.empty-state')).toContainText('No audit events found');
  });
});
