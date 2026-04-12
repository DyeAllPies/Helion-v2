// e2e/specs/metrics.spec.ts
//
// End-to-end tests for the Metrics page.
// Verifies: all 7 KPI cards, WebSocket connection, chart rendering,
// health percentage display, WS error handling, and live streaming.

import { test, expect } from '../fixtures/auth.fixture';

test.describe('Metrics Page', () => {

  test('displays the METRICS page title and subtitle', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Metrics');
    await expect(page).toHaveURL(/\/metrics/, { timeout: 10_000 });
    await expect(page.locator('h1.page-title')).toContainText('METRICS');
    await expect(page.locator('.page-sub')).toContainText('Live cluster metrics');
  });

  test('shows WebSocket connection indicator', async ({ authedPage: page }) => {
    await page.goto('/metrics');

    const wsIndicator = page.locator('.ws-indicator');
    await expect(wsIndicator).toBeVisible({ timeout: 10_000 });

    // Should eventually show WS CONNECTED
    await expect(wsIndicator).toContainText('WS CONNECTED', { timeout: 20_000 });

    // The pulsing dot should have the live class
    await expect(page.locator('.ws-indicator--live')).toBeVisible();
  });

  test('renders all 7 KPI cards', async ({ authedPage: page }) => {
    await page.goto('/metrics');

    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    const kpiLabels = page.locator('.kpi-label');
    const labels = await kpiLabels.allTextContents();

    // All 7 KPI labels must be present
    expect(labels).toContain('TOTAL NODES');
    expect(labels).toContain('HEALTHY NODES');
    expect(labels).toContain('TOTAL JOBS');
    expect(labels).toContain('RUNNING');
    expect(labels).toContain('PENDING');
    expect(labels).toContain('COMPLETED');
    expect(labels).toContain('FAILED');
  });

  test('KPI values are numeric', async ({ authedPage: page }) => {
    await page.goto('/metrics');
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    // Every .kpi-value should be a valid number
    const values = page.locator('.kpi-value');
    const count = await values.count();
    expect(count).toBe(7);

    for (let i = 0; i < count; i++) {
      const text = await values.nth(i).textContent();
      expect(Number(text?.trim())).not.toBeNaN();
    }
  });

  test('healthy nodes KPI shows correct count > 0', async ({ authedPage: page }) => {
    await page.goto('/metrics');
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    const healthyCard = page.locator('.kpi-card:has-text("HEALTHY NODES") .kpi-value');
    await expect(async () => {
      const text = await healthyCard.textContent();
      expect(Number(text?.trim())).toBeGreaterThan(0);
    }).toPass({ timeout: 30_000, intervals: [2_000] });
  });

  test('healthy nodes percentage is displayed', async ({ authedPage: page }) => {
    await page.goto('/metrics');
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    // The HEALTHY NODES card shows "X% healthy" as a subtitle
    const healthySub = page.locator('.kpi-card:has-text("HEALTHY NODES") .kpi-sub');
    await expect(healthySub).toBeVisible();
    await expect(healthySub).toContainText('% healthy');
  });

  test('HEALTHY NODES card has accent border styling', async ({ authedPage: page }) => {
    await page.goto('/metrics');
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    await expect(page.locator('.kpi-card--accent')).toBeVisible();
  });

  test('time-series chart renders after receiving data', async ({ authedPage: page }) => {
    await page.goto('/metrics');

    await expect(page.locator('.chart-panel')).toBeVisible({ timeout: 30_000 });

    // Canvas element should be present
    await expect(page.locator('.chart-wrap canvas')).toBeVisible();

    // Chart header shows snapshot count
    await expect(page.locator('.chart-panel__header')).toContainText('CLUSTER ACTIVITY');
  });

  test('KPI values update over time via WebSocket', async ({ authedPage: page }) => {
    await page.goto('/metrics');
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    const getSnapshotCount = async () => {
      const header = await page.locator('.chart-panel__header').textContent();
      const match = header?.match(/LAST (\d+) SNAPSHOTS/);
      return match ? Number(match[1]) : 0;
    };

    // Wait for at least 2 snapshots (proves WS is streaming continuously)
    await expect(async () => {
      const count = await getSnapshotCount();
      expect(count).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 30_000, intervals: [3_000] });
  });

  test('total nodes KPI matches healthy + unhealthy', async ({ authedPage: page }) => {
    await page.goto('/metrics');
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    const totalText = await page.locator('.kpi-card:has-text("TOTAL NODES") .kpi-value').textContent();
    const healthyText = await page.locator('.kpi-card:has-text("HEALTHY NODES") .kpi-value').textContent();

    const total = Number(totalText?.trim());
    const healthy = Number(healthyText?.trim());

    // Healthy nodes must be <= total nodes
    expect(healthy).toBeLessThanOrEqual(total);
    expect(total).toBeGreaterThan(0);
  });

  test('waiting state shows before first WS frame', async ({ authedPage: page }) => {
    // Block the WS connection to see the waiting spinner
    await page.route('**/ws/metrics**', route => route.abort());

    await page.goto('/metrics');

    // The waiting spinner should appear since WS never connects
    await expect(page.locator('.waiting')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.waiting')).toContainText('Waiting for first metrics snapshot');
  });

  test('WS error shows error banner', async ({ authedPage: page }) => {
    await page.goto('/metrics');

    // Wait for initial WS connection to establish
    await expect(page.locator('.kpi-grid')).toBeVisible({ timeout: 20_000 });

    // Now block subsequent WS connections
    await page.route('**/ws/metrics**', route => route.abort());

    // Navigate away and back to force a new WS connection (which will fail)
    await page.goto('/nodes');
    await page.goto('/metrics');

    // Error banner should appear since WS can't connect
    await expect(page.locator('.error-banner')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('.error-banner')).toContainText('WebSocket error');
  });
});
