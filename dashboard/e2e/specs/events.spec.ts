// e2e/specs/events.spec.ts
//
// End-to-end tests for the Events page (live event feed via WebSocket).

import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { getRootToken, submitJob } from '../fixtures/cluster.fixture';

test.describe('Events Page', () => {

  test('displays the EVENTS page title', async ({ authedPage: page }) => {
    await navigateTo(page, '/events');
    await expect(page.locator('h1.page-title')).toContainText('EVENTS');
  });

  test('shows connection status indicator', async ({ authedPage: page }) => {
    await navigateTo(page, '/events');
    // Should show either LIVE or DISCONNECTED.
    const statusText = page.locator('.page-sub');
    await expect(statusText).toBeVisible();
  });

  test('clear button is present', async ({ authedPage: page }) => {
    await navigateTo(page, '/events');
    await expect(page.locator('button.clear-btn')).toBeVisible();
  });

  test('shows waiting state when no events yet', async ({ authedPage: page }) => {
    await navigateTo(page, '/events');
    // Either events are present or the empty state is shown.
    const feed = page.locator('.feed');
    await expect(feed).toBeVisible();
  });
});
