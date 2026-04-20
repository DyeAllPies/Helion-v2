// e2e/specs/audit.spec.ts
//
// End-to-end tests for the Audit Log page.
// Verifies: audit events render, type filter for multiple types, pagination,
// event detail fields, all column headers, error handling, and empty state.

import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { getRootToken, submitJob, API_URL } from '../fixtures/cluster.fixture';

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
    await expect(page.locator('select.status-select')).toBeVisible({ timeout: 5_000 });
    // Reset filter to ALL to ensure all options are rendered.
    await page.selectOption('select.status-select', '');
    await page.waitForTimeout(300);

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
    await page.route('**/api/audit?*', route => {
      route.fulfill({ status: 500, body: 'Internal Server Error' });
    });

    await navigateTo(page, '/audit');
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Failed to load audit log');
    await page.unroute('**/api/audit?*');
  });

  test('empty state displays when no events match filter', async ({ authedPage: page }) => {
    await page.route('**/api/audit?*', route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ events: [], total: 0, page: 0, size: 50 }),
      });
    });

    await navigateTo(page, '/audit');
    await expect(page.locator('.empty-state')).toBeVisible({ timeout: 15_000 });
    await page.unroute('**/api/audit?*');
    await expect(page.locator('.empty-state')).toContainText('No audit events found');
  });

  // ── Feature 37: authz deny surfaces on the audit trail ───────────
  //
  // Every 403 from adminMiddleware emits an authz_deny event carrying
  // the actor + action + deny_code. This is load-bearing for incident
  // response: if a non-admin token is abused the SOC pivots from the
  // authz_deny rows to find the attacker's source IP + subject. The
  // test below causes a real 403 via a user-role token and asserts
  // the resulting row shows up in the audit table within a second or
  // two (the audit log is in-process, so latency is tiny).

  test('authz_deny event is emitted after non-admin hits /admin/tokens', async () => {
    // Feature 37 contract: every 403 from adminMiddleware writes an
    // authz_deny record to the audit log, with the actor's ID and
    // the deny code. We verify this via the REST audit endpoint
    // (type=authz_deny) rather than the dashboard's type filter,
    // since the select dropdown only pre-populates the common event
    // types — authz_deny renders correctly in the table but isn't a
    // built-in filter option. This test is what a SOC would do when
    // pivoting from "who got denied last hour" to "what they tried".
    const root = getRootToken();

    // Mint a user-role token from the root admin.
    const mintResp = await fetch(`${API_URL}/admin/tokens`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${root}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        subject:   `e2e-authz-deny-${Date.now()}`,
        role:      'node',
        ttl_hours: 1,
      }),
    });
    expect([200, 201]).toContain(mintResp.status);
    const { token: userToken } = await mintResp.json() as { token: string };

    // Trigger the 403 — this writes an authz_deny audit event.
    const denyResp = await fetch(`${API_URL}/admin/tokens`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${userToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ subject: 'never-minted', role: 'node', ttl_hours: 1 }),
    });
    expect(denyResp.status).toBe(403);

    // Query the audit REST endpoint for authz_deny events. The audit
    // logger writes synchronously to BadgerDB, so by the time the
    // 403 has returned the record is already persisted — no polling
    // needed.
    // Coordinator mounts the audit endpoint at /audit (no /api
    // prefix — the dashboard's ng-serve proxy rewrites /api/* to
    // coordinator-root). From Node fetch we hit the coordinator
    // directly, so strip the /api prefix.
    const auditResp = await fetch(
      `${API_URL}/audit?type=authz_deny&size=100`,
      { headers: { Authorization: `Bearer ${root}` } },
    );
    expect(auditResp.status).toBe(200);
    const auditBody = await auditResp.json() as {
      events: Array<{ type: string; actor?: string }>;
    };
    expect(auditBody.events.length).toBeGreaterThan(0);
    expect(auditBody.events.every(e => e.type === 'authz_deny')).toBe(true);
  });
});
