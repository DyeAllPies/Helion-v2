// e2e/specs/audit.spec.ts
//
// End-to-end tests for the Audit Log page.
// Verifies: audit events render, type filter for multiple types, pagination,
// event detail fields, all column headers, error handling, and empty state.

import { test, expect } from '../fixtures/auth.fixture';
import { getRootToken, submitJob } from '../fixtures/cluster.fixture';

test.describe('Audit Log Page', () => {

  test('displays the AUDIT LOG page title', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Audit');
    await expect(page).toHaveURL(/\/audit/);
    await expect(page.locator('h1.page-title')).toContainText('AUDIT LOG');
  });

  test('shows audit events in the table with event count subtitle', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    await expect(page.locator('.page-sub')).toContainText('events');
    await expect(page.locator('.page-sub')).toContainText('read-only');
  });

  test('all table column headers are present', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    const headers = page.locator('table[mat-table] th');
    const headerTexts = (await headers.allTextContents()).map(h => h.trim());
    expect(headerTexts).toContain('TIMESTAMP');
    expect(headerTexts).toContain('EVENT TYPE');
    expect(headerTexts).toContain('ACTOR');
    expect(headerTexts).toContain('TARGET ID');
    expect(headerTexts).toContain('MESSAGE');
  });

  test('coordinator_start event is present after boot', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    await expect(page.locator('text=COORDINATOR_START')).toBeVisible({ timeout: 10_000 });
  });

  test('node_register events appear for connected nodes', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    await expect(page.locator('text=NODE_REGISTER')).toBeVisible({ timeout: 10_000 });
  });

  test('job_submit event appears after submitting a job', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-audit-${Date.now()}`;

    // Submit a job so we get a job_submit audit event
    await submitJob(token, { id: jobId, command: 'echo', args: ['audit-test'] });

    await page.goto('/audit');

    // Filter to job_submit events
    await page.selectOption('select.status-select', 'job_submit');
    await page.waitForTimeout(500);

    await expect(page.locator('text=JOB_SUBMIT')).toBeVisible({ timeout: 10_000 });
  });

  test('event detail fields display correctly', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    // First row should have a formatted timestamp (YYYY-MM-DD HH:MM:SS)
    const firstRow = page.locator('table[mat-table] tr.mat-mdc-row').first();
    const cells = firstRow.locator('td');
    const cellTexts = await cells.allTextContents();

    // Timestamp column (first cell) should match date format
    const hasTimestamp = cellTexts.some(t => /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/.test(t));
    expect(hasTimestamp).toBe(true);

    // Message column should have non-empty content
    const msgCell = firstRow.locator('.msg-cell');
    const msgText = await msgCell.textContent();
    expect(msgText?.trim().length).toBeGreaterThan(0);
  });

  test('type filter narrows results to coordinator_start only', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    await page.selectOption('select.status-select', 'coordinator_start');
    await page.waitForTimeout(500);

    const badges = page.locator('table[mat-table] .event-type');
    const count = await badges.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      await expect(badges.nth(i)).toContainText('COORDINATOR_START');
    }
  });

  test('type filter narrows results to node_register only', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    await page.selectOption('select.status-select', 'node_register');
    await page.waitForTimeout(500);

    const badges = page.locator('table[mat-table] .event-type');
    const count = await badges.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      await expect(badges.nth(i)).toContainText('NODE_REGISTER');
    }
  });

  test('filter dropdown contains ALL EVENTS plus all 9 event types', async ({ authedPage: page }) => {
    await page.goto('/audit');

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
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

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
    await page.goto('/audit');

    await expect(page.locator('mat-paginator')).toBeVisible({ timeout: 10_000 });
  });

  test('refresh button reloads data', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    await page.click('button.refresh-btn');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });
  });

  test('event type badges have correct CSS classes', async ({ authedPage: page }) => {
    await page.goto('/audit');

    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 10_000 });

    // coordinator_start → evt-coordinator
    await expect(page.locator('.event-type.evt-coordinator').first())
      .toBeVisible({ timeout: 10_000 });

    // node_register → evt-node
    await expect(page.locator('.event-type.evt-node').first())
      .toBeVisible({ timeout: 10_000 });
  });

  test('job event badges have evt-job CSS class', async ({ authedPage: page }) => {
    const token = getRootToken();

    // Ensure a job_submit event exists
    await submitJob(token, { id: `e2e-auditcss-${Date.now()}`, command: 'echo', args: ['css'] });

    await page.goto('/audit');
    await page.selectOption('select.status-select', 'job_submit');
    await page.waitForTimeout(500);

    await expect(page.locator('.event-type.evt-job').first())
      .toBeVisible({ timeout: 10_000 });
  });

  test('error banner appears when API returns error', async ({ authedPage: page }) => {
    await page.route('**/audit?*', route => {
      route.fulfill({ status: 500, body: 'Internal Server Error' });
    });

    await page.goto('/audit');
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 10_000 });
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

    await page.goto('/audit');
    await expect(page.locator('.empty-state')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.empty-state')).toContainText('No audit events found');
  });
});
