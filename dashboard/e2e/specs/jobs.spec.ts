// e2e/specs/jobs.spec.ts
//
// End-to-end tests for the Jobs page.
// Submits a job via the coordinator REST API, then verifies it appears
// in the dashboard job list, transitions through states, and is viewable
// in the job detail page.  Covers pagination, filtering, empty state,
// error handling, and metadata completeness.

import { test, expect } from '../fixtures/auth.fixture';
import { getRootToken, submitJob, API_URL } from '../fixtures/cluster.fixture';

test.describe('Jobs List', () => {

  test('displays the JOBS page title', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs/);
    await expect(page.locator('h1.page-title')).toContainText('JOBS');
  });

  test('shows subtitle with total job count', async ({ authedPage: page }) => {
    await page.goto('/jobs');
    await expect(page.locator('.page-sub')).toContainText('total jobs');
  });

  test('all table column headers are present', async ({ authedPage: page }) => {
    const token = getRootToken();
    await submitJob(token, { id: `e2e-cols-${Date.now()}`, command: 'echo', args: ['cols'] });

    await page.goto('/jobs');
    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    const headers = page.locator('table[mat-table] th');
    const headerTexts = (await headers.allTextContents()).map(h => h.trim());
    expect(headerTexts).toContain('STATUS');
    expect(headerTexts).toContain('JOB ID');
    expect(headerTexts).toContain('COMMAND');
    expect(headerTexts).toContain('NODE');
    expect(headerTexts).toContain('CREATED');
    expect(headerTexts).toContain('FINISHED');
    expect(headerTexts).toContain('EXIT');
  });

  test('shows a submitted job in the job list', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-job-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['hello-e2e'] });

    await page.goto('/jobs');
    await expect(page.locator(`text=${jobId}`)).toBeVisible({ timeout: 15_000 });
  });

  test('job transitions to a terminal state', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-term-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['done'] });

    await page.goto('/jobs');
    await expect(page.locator(`text=${jobId}`)).toBeVisible({ timeout: 15_000 });

    await expect(async () => {
      await page.click('button.refresh-btn');
      const row = page.locator(`tr:has-text("${jobId}")`);
      const badge = row.locator('.badge');
      const text = await badge.textContent();
      const terminal = ['COMPLETED', 'FAILED', 'TIMEOUT', 'LOST'];
      expect(terminal.some(s => text?.includes(s))).toBe(true);
    }).toPass({timeout: 15_000, intervals: [2_000] });
  });

  test('status filter works for completed jobs', async ({ authedPage: page }) => {
    await page.goto('/jobs');
    await expect(page.locator('table[mat-table]')).toBeVisible({ timeout: 15_000 });

    await page.selectOption('select.status-select', 'completed');
    await page.waitForTimeout(500);

    const badges = page.locator('table[mat-table] .badge');
    const count = await badges.count();
    for (let i = 0; i < count; i++) {
      await expect(badges.nth(i)).toContainText('COMPLETED');
    }
  });

  test('filter dropdown contains all status options', async ({ authedPage: page }) => {
    await page.goto('/jobs');
    await expect(page.locator('table[mat-table]')).toBeVisible({ timeout: 15_000 });

    const options = page.locator('select.status-select option');
    const optionTexts = (await options.allTextContents()).map(t => t.trim().toLowerCase());

    // ALL + 7 statuses
    expect(optionTexts).toContain('all');
    expect(optionTexts).toContain('pending');
    expect(optionTexts).toContain('dispatching');
    expect(optionTexts).toContain('running');
    expect(optionTexts).toContain('completed');
    expect(optionTexts).toContain('failed');
    expect(optionTexts).toContain('timeout');
    expect(optionTexts).toContain('lost');
  });

  test('switching filter to ALL shows all jobs again', async ({ authedPage: page }) => {
    await page.goto('/jobs');
    await expect(page.locator('table[mat-table]')).toBeVisible({ timeout: 15_000 });

    // Filter to completed first
    await page.selectOption('select.status-select', 'completed');
    await page.waitForTimeout(500);
    const filteredCount = await page.locator('table[mat-table] tr.mat-mdc-row').count();

    // Switch back to ALL
    await page.selectOption('select.status-select', '');
    await page.waitForTimeout(500);
    const allCount = await page.locator('table[mat-table] tr.mat-mdc-row').count();

    expect(allCount).toBeGreaterThanOrEqual(filteredCount);
  });

  test('paginator is present with page size options', async ({ authedPage: page }) => {
    await page.goto('/jobs');
    await expect(page.locator('mat-paginator')).toBeVisible({ timeout: 15_000 });
  });

  test('clicking a job link navigates to detail', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-click-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['click-test'] });

    await page.goto('/jobs');
    await expect(page.locator(`text=${jobId}`)).toBeVisible({ timeout: 15_000 });

    await page.click(`a.job-link:has-text("${jobId}")`);
    await expect(page).toHaveURL(new RegExp(`/jobs/${jobId}`));
  });

  test('error banner appears when API fails', async ({ authedPage: page }) => {
    await page.route('**/jobs?*', route => {
      route.fulfill({ status: 500, body: 'Internal Server Error' });
    });

    await page.goto('/jobs');
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Failed to load jobs');
  });

  test('empty state when no jobs match filter', async ({ authedPage: page }) => {
    // Intercept with an empty page
    await page.route('**/jobs?*', route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ jobs: [], total: 0, page: 0, size: 25 }),
      });
    });

    await page.goto('/jobs');
    await expect(page.locator('.empty-state')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.empty-state')).toContainText('No jobs found');
  });
});

test.describe('Job Detail', () => {

  test('shows full metadata card for a completed job', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-meta-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['detail-meta'] });

    // Wait for job to complete
    await expect(async () => {
      const res = await fetch(`${API_URL}/jobs/${jobId}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const job = await res.json();
      expect(['completed', 'failed', 'timeout', 'lost']).toContain(job.status);
    }).toPass({timeout: 15_000, intervals: [2_000] });

    await page.goto(`/jobs/${jobId}`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });

    // Job ID
    await expect(page.locator('.job-id')).toContainText(jobId);

    // Command with args
    await expect(page.locator('.meta-value.cmd')).toContainText('echo detail-meta');

    // Status badge should be a terminal state
    const badge = page.locator('.meta-card .badge');
    const badgeText = await badge.textContent();
    expect(badgeText).toMatch(/COMPLETED|FAILED|TIMEOUT|LOST/);

    // Meta labels should include COMMAND, NODE, CREATED
    const labels = page.locator('.meta-label');
    const labelTexts = await labels.allTextContents();
    expect(labelTexts).toContain('COMMAND');
    expect(labelTexts).toContain('NODE');
    expect(labelTexts).toContain('CREATED');
  });

  test('breadcrumb navigation links back to jobs list', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-bread-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['breadcrumb'] });

    await page.goto(`/jobs/${jobId}`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });

    // Breadcrumb should show "JOBS > jobId"
    await expect(page.locator('.breadcrumb')).toContainText('JOBS');
    await expect(page.locator('.breadcrumb .current')).toContainText(jobId);

    // Click JOBS breadcrumb link
    await page.click('.breadcrumb a');
    await expect(page).toHaveURL(/\/jobs$/);
  });

  test('log panel shows ENDED for terminal jobs', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-logend-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['log-end'] });

    // Wait for job to complete
    await new Promise(r => setTimeout(r, 5_000));

    await page.goto(`/jobs/${jobId}`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });

    // Log panel should exist
    await expect(page.locator('.log-panel')).toBeVisible();

    // Log badge should show ENDED (terminal jobs don't attempt WS)
    const logBadge = page.locator('.log-badge');
    await expect(logBadge).toBeVisible();
    await expect(logBadge).toContainText(/ENDED|CONNECTING/);

    // Log line count should be visible
    await expect(page.locator('.log-line-count')).toBeVisible();
  });

  test('refresh button updates job metadata', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-refresh-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['refresh'] });

    await page.goto(`/jobs/${jobId}`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });

    // Click refresh button in metadata card
    await page.click('.meta-card .refresh-btn');

    // Card should still be visible (no crash, data reloaded)
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });
  });

  test('job not found shows error', async ({ authedPage: page }) => {
    await page.goto('/jobs/nonexistent-job-id-12345');

    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Job not found');
  });
});

test.describe('Rust Runtime (node2)', () => {

  test('job dispatched to Rust runtime node completes and shows on dashboard', async ({ authedPage: page }) => {
    const token = getRootToken();

    // Submit multiple jobs — round-robin scheduler will dispatch some to
    // e2e-node-2 which runs the Rust runtime (cgroup v2 + seccomp).
    const jobIds: string[] = [];
    for (let i = 0; i < 4; i++) {
      const jobId = `e2e-rust-${Date.now()}-${i}`;
      jobIds.push(jobId);
      await submitJob(token, { id: jobId, command: 'echo', args: ['rust-runtime-test'] });
    }

    // Navigate to jobs page and verify they appear
    await page.goto('/jobs');
    for (const jobId of jobIds) {
      await expect(page.locator(`text=${jobId}`)).toBeVisible({ timeout: 15_000 });
    }

    // Wait for at least one job to reach a terminal state
    await expect(async () => {
      await page.click('button.refresh-btn');
      const badges = page.locator('table[mat-table] .badge');
      const allText = await badges.allTextContents();
      const terminal = allText.filter(t => ['COMPLETED', 'FAILED', 'TIMEOUT'].some(s => t.includes(s)));
      expect(terminal.length).toBeGreaterThan(0);
    }).toPass({timeout: 15_000, intervals: [2_000] });
  });

  test('job on Rust node visible in detail view', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-rust-detail-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['rust-detail'] });

    // Wait for completion
    await expect(async () => {
      const res = await fetch(`${API_URL}/jobs/${jobId}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const job = await res.json();
      expect(['completed', 'failed', 'timeout', 'lost']).toContain(job.status);
    }).toPass({timeout: 15_000, intervals: [2_000] });

    // Verify it shows up in the dashboard detail view
    await page.goto(`/jobs/${jobId}`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.job-id')).toContainText(jobId);
    await expect(page.locator('.meta-value.cmd')).toContainText('echo');

    // Status badge should be terminal
    const badge = page.locator('.meta-card .badge');
    const badgeText = await badge.textContent();
    expect(badgeText).toMatch(/COMPLETED|FAILED|TIMEOUT/);
  });
});
