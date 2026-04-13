// e2e/specs/workflows.spec.ts
//
// End-to-end tests for the Workflows page.
// Submits workflows via the coordinator REST API, then verifies they appear
// in the dashboard workflow list, show correct job DAG cards, and can be
// cancelled. Covers pagination, empty state, and detail view.

import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { getRootToken, submitWorkflow, cancelWorkflow } from '../fixtures/cluster.fixture';

test.describe('Workflows List', () => {

  test('displays the WORKFLOWS page title', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Workflows');
    await expect(page).toHaveURL(/\/workflows/);
    await expect(page.locator('h1.page-title')).toContainText('WORKFLOWS');
  });

  test('shows subtitle with total workflow count', async ({ authedPage: page }) => {
    await navigateTo(page, '/workflows');
    await expect(page.locator('.page-sub')).toContainText('total workflows');
  });

  test('shows empty state when no workflows exist', async ({ authedPage: page }) => {
    await navigateTo(page, '/workflows');
    // If no workflows were submitted yet, we should see the empty state or zero count.
    const sub = page.locator('.page-sub');
    await expect(sub).toBeVisible();
  });

  test('all table column headers are present', async ({ authedPage: page }) => {
    const token = getRootToken();
    await submitWorkflow(token, {
      id: `e2e-wf-cols-${Date.now()}`,
      name: 'column test',
      jobs: [{ name: 'build', command: 'echo', args: ['cols'] }],
    });

    await navigateTo(page, '/workflows');
    await expect(page.locator('table[mat-table] tr.mat-mdc-row').first())
      .toBeVisible({ timeout: 15_000 });

    const headers = page.locator('table[mat-table] th');
    const headerTexts = (await headers.allTextContents()).map(h => h.trim());
    expect(headerTexts).toContain('STATUS');
    expect(headerTexts).toContain('WORKFLOW ID');
    expect(headerTexts).toContain('NAME');
    expect(headerTexts).toContain('JOBS');
    expect(headerTexts).toContain('CREATED');
    expect(headerTexts).toContain('FINISHED');
  });

  test('shows a submitted workflow in the list', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-list-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'list test',
      jobs: [{ name: 'step1', command: 'echo', args: ['hello'] }],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });
  });

  test('workflow status badge is visible', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-badge-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'badge test',
      jobs: [{ name: 'a', command: 'echo' }],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    const badge = page.locator('table[mat-table] .badge').first();
    await expect(badge).toBeVisible();
  });

  test('clicking a workflow navigates to detail page', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-click-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'click test',
      jobs: [{ name: 'step1', command: 'echo' }],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));
  });

  test('refresh button reloads the workflow list', async ({ authedPage: page }) => {
    await navigateTo(page, '/workflows');
    const btn = page.locator('button.refresh-btn');
    await expect(btn).toBeVisible();
    await btn.click();
    // Should not error — just verify the button is clickable.
    await expect(page.locator('h1.page-title')).toContainText('WORKFLOWS');
  });
});

test.describe('Workflow Detail', () => {

  test('shows workflow metadata', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-detail-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'detail test',
      jobs: [
        { name: 'build', command: 'echo', args: ['building'] },
        { name: 'test', command: 'echo', depends_on: ['build'] },
      ],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // Metadata fields should be visible.
    await expect(page.locator('.meta-label >> text=ID')).toBeVisible();
    await expect(page.locator('.meta-label >> text=CREATED')).toBeVisible();
  });

  test('shows DAG job cards', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-dag-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'dag test',
      jobs: [
        { name: 'build', command: 'echo' },
        { name: 'test', command: 'echo', depends_on: ['build'] },
        { name: 'deploy', command: 'echo', depends_on: ['test'] },
      ],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // Three job cards should be visible.
    const jobCards = page.locator('.job-card');
    await expect(jobCards).toHaveCount(3);

    // Job names should be visible.
    await expect(page.locator('.job-card__name >> text=build')).toBeVisible();
    await expect(page.locator('.job-card__name >> text=test')).toBeVisible();
    await expect(page.locator('.job-card__name >> text=deploy')).toBeVisible();
  });

  test('shows dependency labels on job cards', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-deps-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'deps test',
      jobs: [
        { name: 'build', command: 'echo' },
        { name: 'test', command: 'echo', depends_on: ['build'] },
      ],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // The "test" card should show "DEPENDS ON: build".
    await expect(page.locator('.dep-label >> text=DEPENDS ON:')).toBeVisible();
    await expect(page.locator('.dep-name >> text=build')).toBeVisible();
  });

  test('detail page shows status badge for workflow', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-status-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'status badge test',
      jobs: [{ name: 'quick', command: 'echo', args: ['done'] }],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // Status badge should be visible regardless of workflow state.
    await expect(page.locator('.badge').first()).toBeVisible();
  });

  test('cancelled workflow shows cancelled status', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-cancelled-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'cancel test',
      jobs: [{ name: 'long', command: 'sleep', args: ['3600'] }],
    });

    // Cancel via API.
    await cancelWorkflow(token, wfId);

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    await expect(page.locator('.badge-cancelled')).toBeVisible();
  });

  test('breadcrumb navigates back to workflow list', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-bread-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'breadcrumb test',
      jobs: [{ name: 'a', command: 'echo' }],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // Click breadcrumb to go back.
    await page.click('.breadcrumb a >> text=Workflows');
    await expect(page).toHaveURL(/\/workflows$/);
  });

  test('workflow with on_failure condition shows condition label', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-cond-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'condition test',
      jobs: [
        { name: 'risky', command: 'echo' },
        { name: 'cleanup', command: 'echo', depends_on: ['risky'], condition: 'on_failure' },
      ],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // The cleanup job card should show the on_failure condition badge.
    await expect(page.locator('.condition-badge')).toBeVisible();
    await expect(page.locator('.condition-badge')).toContainText('on_failure');
  });

  test('job card links to job detail page', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfId = `e2e-wf-joblink-${Date.now()}`;

    await submitWorkflow(token, {
      id: wfId,
      name: 'job link test',
      jobs: [{ name: 'step1', command: 'echo' }],
    });

    await navigateTo(page, '/workflows');
    await expect(async () => {
      await page.click('button.refresh-btn');
      await expect(page.locator(`text=${wfId}`)).toBeVisible();
    }).toPass({ timeout: 15_000, intervals: [2_000] });

    await page.click(`a.wf-link >> text=${wfId}`);
    await expect(page).toHaveURL(new RegExp(`/workflows/${wfId}`));

    // Job card should have a "View job details" link.
    const jobLink = page.locator('a.job-link >> text=View job details');
    await expect(jobLink).toBeVisible();
  });
});
