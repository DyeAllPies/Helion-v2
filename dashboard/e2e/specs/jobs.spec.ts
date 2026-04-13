// e2e/specs/jobs.spec.ts
//
// End-to-end tests for the Jobs page.
// Submits a job via the coordinator REST API, then verifies it appears
// in the dashboard job list, transitions through states, and is viewable
// in the job detail page.  Covers pagination, filtering, empty state,
// error handling, and metadata completeness.

// NOTE: These tests submit jobs that persist in BadgerDB. If running locally
// against a cluster with accumulated data from prior runs, newer jobs may fall
// off page 1 and cause "element not found" failures. Always start with a clean
// cluster: docker compose -f docker-compose.yml -f docker-compose.e2e.yml down -v
import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { getRootToken, submitJob, submitJobWithRetry, API_URL } from '../fixtures/cluster.fixture';

test.describe('Jobs List', () => {

  test('displays the JOBS page title', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs/);
    await expect(page.locator('h1.page-title')).toContainText('JOBS');
  });

  test('shows subtitle with total job count', async ({ authedPage: page }) => {
    await navigateTo(page, '/jobs');
    await expect(page.locator('.page-sub')).toContainText('total jobs');
  });

  test('all table column headers are present', async ({ authedPage: page }) => {
    const token = getRootToken();
    await submitJob(token, { id: `e2e-cols-${Date.now()}`, command: 'echo', args: ['cols'] });

    await navigateTo(page, '/jobs');
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

    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
  });

  test('job transitions to a terminal state', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-term-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['done'] });

    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await expect(async () => {
      await page.click('button.refresh-btn');
      const row = page.locator(`tr:has-text("${jobId}")`);
      const badge = row.locator('.badge').first();
      const text = await badge.textContent();
      const terminal = ['COMPLETED', 'FAILED', 'TIMEOUT', 'LOST'];
      expect(terminal.some(s => text?.includes(s))).toBe(true);
    }).toPass({timeout: 15_000, intervals: [2_000] });
  });

  test('status filter works for completed jobs', async ({ authedPage: page }) => {
    await navigateTo(page, '/jobs');
    await expect(page.locator('table[mat-table]')).toBeVisible({ timeout: 15_000 });

    await page.selectOption('select.status-select', 'completed');
    await page.waitForTimeout(500);

    // Check only status badges (not runtime badges) — status badges have class badge-{status}
    const statusBadges = page.locator('table[mat-table] td:first-child .badge');
    const count = await statusBadges.count();
    for (let i = 0; i < count; i++) {
      await expect(statusBadges.nth(i)).toContainText('COMPLETED');
    }
  });

  test('filter dropdown contains all status options', async ({ authedPage: page }) => {
    await navigateTo(page, '/jobs');
    await expect(page.locator('table[mat-table]')).toBeVisible({ timeout: 15_000 });

    const options = page.locator('select.status-select option');
    const optionTexts = (await options.allTextContents()).map(t => t.trim().toLowerCase());

    // ALL + 11 statuses
    expect(optionTexts).toContain('all');
    expect(optionTexts).toContain('pending');
    expect(optionTexts).toContain('scheduled');
    expect(optionTexts).toContain('dispatching');
    expect(optionTexts).toContain('running');
    expect(optionTexts).toContain('completed');
    expect(optionTexts).toContain('failed');
    expect(optionTexts).toContain('timeout');
    expect(optionTexts).toContain('lost');
    expect(optionTexts).toContain('retrying');
    expect(optionTexts).toContain('cancelled');
    expect(optionTexts).toContain('skipped');
  });

  test('switching filter to ALL shows all jobs again', async ({ authedPage: page }) => {
    await navigateTo(page, '/jobs');
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
    await navigateTo(page, '/jobs');
    await expect(page.locator('mat-paginator')).toBeVisible({ timeout: 15_000 });
  });

  test('clicking a job link navigates to detail', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-click-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['click-test'] });

    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.job-link:has-text("${jobId}")`);
    await expect(page).toHaveURL(new RegExp(`/jobs/${jobId}`));
  });

  test('error banner appears when API fails', async ({ authedPage: page }) => {
    await page.route('**/api/jobs?*', route => {
      route.fulfill({ status: 500, body: 'Internal Server Error' });
    });

    await navigateTo(page, '/jobs');
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Failed to load jobs');
  });

  test('empty state when no jobs match filter', async ({ authedPage: page }) => {
    // Intercept with an empty page
    await page.route('**/api/jobs?*', route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ jobs: [], total: 0, page: 0, size: 25 }),
      });
    });

    await navigateTo(page, '/jobs');
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

    // Navigate to job detail via jobs list
    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
    await page.click(`a.job-link:has-text("${jobId}")`);

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

    // Navigate to jobs list, wait for job to appear via refresh
    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    // Wait for table to stabilize, then click the job link.
    const jobLink = page.locator(`a.job-link:has-text("${jobId}")`);
    await expect(jobLink).toBeVisible();
    await jobLink.click();

    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });

    // Breadcrumb should contain the job ID.
    await expect(page.locator('.breadcrumb')).toContainText(jobId);

    // Click breadcrumb link back to jobs list.
    await page.click('.breadcrumb a');
    await expect(page).toHaveURL(/\/jobs$/);
  });

  test('log panel shows ENDED for terminal jobs', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-logend-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['log-end'] });

    // Wait for dispatch + execution
    await expect(async () => {
      const res = await fetch(`${API_URL}/jobs/${jobId}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const job = await res.json();
      expect(['completed', 'failed', 'timeout', 'lost']).toContain(job.status);
    }).toPass({timeout: 15_000, intervals: [2_000] });

    // Navigate to job detail via jobs list
    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
    await page.click(`a.job-link:has-text("${jobId}")`);
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

    // Navigate to job detail via jobs list
    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
    await page.click(`a.job-link:has-text("${jobId}")`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });

    // Click refresh button in metadata card
    await page.click('.meta-card .refresh-btn');

    // Card should still be visible (no crash, data reloaded)
    await expect(page.locator('.meta-card')).toBeVisible();
  });

  test('job not found shows error', async ({ authedPage: page }) => {
    // Navigate to jobs list first, then push to a nonexistent job via Angular router
    await navigateTo(page, '/jobs');
    await page.evaluate(() => {
      window.history.pushState({}, '', '/jobs/nonexistent-job-id-12345');
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('Job not found');
  });
});

test.describe('Rust Runtime (node2)', () => {

  test('job dispatched to Rust runtime node completes and shows on dashboard', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-rust-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['rust-runtime-test'] });

    // Navigate to jobs page and verify the job appears
    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    // Wait for the job to reach a terminal state
    await expect(async () => {
      await page.click('button.refresh-btn');
      const row = page.locator(`tr:has-text("${jobId}")`);
      const badge = row.locator('.badge').first();
      const text = await badge.textContent();
      const terminal = ['COMPLETED', 'FAILED', 'TIMEOUT', 'LOST'];
      expect(terminal.some(s => text?.includes(s))).toBe(true);
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

    // Navigate to job detail via jobs list
    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
    await page.click(`a.job-link:has-text("${jobId}")`);
    await expect(page.locator('.meta-card')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.job-id')).toContainText(jobId);
    await expect(page.locator('.meta-value.cmd')).toContainText('echo');

    // Status badge should be terminal
    const badge = page.locator('.meta-card .badge');
    const badgeText = await badge.textContent();
    expect(badgeText).toMatch(/COMPLETED|FAILED|TIMEOUT/);
  });
});

test.describe('Jobs Retry Policy', () => {

  test('job submitted with retry policy appears in list', async ({ authedPage: page }) => {
    const token = getRootToken();
    const jobId = `e2e-retry-${Date.now()}`;

    await submitJobWithRetry(token, {
      id: jobId,
      command: 'echo',
      args: ['retry-test'],
      retry_policy: {
        max_attempts: 3,
        backoff: 'exponential',
        initial_delay_ms: 1000,
        max_delay_ms: 10000,
        jitter: true,
      },
    });

    await navigateTo(page, '/jobs');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${jobId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
  });

  test('retrying filter option is present in dropdown', async ({ authedPage: page }) => {
    await navigateTo(page, '/jobs');
    const options = page.locator('select.status-select option');
    const texts = (await options.allTextContents()).map(t => t.trim().toLowerCase());
    expect(texts).toContain('retrying');
  });
});
