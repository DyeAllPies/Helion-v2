// e2e/specs/ml-artifacts.spec.ts
//
// Feature 11 — artifact store — through-the-UI coverage.
//
// The artifact Store is a backend library; the dashboard doesn't
// touch it directly. What the dashboard DOES touch is dataset
// registration, which accepts a URI whose scheme must match what the
// coordinator's configured Store understands (`s3://` when
// HELION_ARTIFACTS_BACKEND=s3, `file://` when =local). The e2e
// harness sets the backend to s3 (see docker-compose.e2e.yml), so
// registering an `s3://helion/...` URI exercises the full
// dashboard → REST → validator → registry chain against a real
// MinIO-backed config.
//
// Covered here:
//   - Register a dataset with an s3:// URI; verify it appears in
//     the Datasets list.
//   - Tag filter narrows the list to the new entry; clearing it
//     restores the full list.
//   - Delete removes it.
//   - Registering with an http:// URI is rejected by the server
//     and the error banner surfaces the message.
//
// Not covered here (belongs to features 12/13/19):
//   - Actually uploading bytes to MinIO and having a downstream
//     job consume them. That exercises the Stager end-to-end, not
//     just the Store's URI-shape contract.

import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { API_URL, getRootToken } from '../fixtures/cluster.fixture';

// Datasets are (name, version)-keyed; use a per-run suffix so
// repeat runs don't 409 on each other when the BadgerDB volume
// isn't torn down between them.
const runId = Date.now().toString(36);
const DS_NAME = `e2e-iris-${runId}`;
const DS_VERSION = 'v1';
// The URI doesn't have to resolve to real bytes for this spec —
// feature 11's registry validator only checks the scheme + key
// shape. Actual byte-level flow belongs to the iris demo test.
const DS_URI = `s3://helion/e2e-artifacts/${runId}/iris.csv`;

async function deleteDatasetIfExists(name: string, version: string): Promise<void> {
  try {
    await fetch(`${API_URL}/api/datasets/${name}/${version}`, {
      method: 'DELETE',
      headers: { Authorization: `Bearer ${getRootToken()}` },
    });
  } catch {
    // Best-effort cleanup; errors don't fail the test.
  }
}

test.describe('ML Artifacts — Datasets view against live MinIO config', () => {
  test.afterAll(async () => {
    await deleteDatasetIfExists(DS_NAME, DS_VERSION);
  });

  test('loads the Datasets page with the ML · DATASETS title', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets/);
    await expect(page.locator('h1.page-title')).toContainText('ML · DATASETS');
  });

  test('registers a dataset with an s3:// URI and lists it', async ({ authedPage: page }) => {
    await navigateTo(page, '/ml/datasets');

    // Open the register modal.
    await page.click('button.btn-primary:has-text("Register")');
    await expect(page.locator('h2[mat-dialog-title]')).toContainText('Register dataset');

    // Fill the form. Angular Material inputs bind via ngModel, so
    // targeting by formcontrolname/name works across releases.
    await page.fill('input[name="name"]', DS_NAME);
    await page.fill('input[name="version"]', DS_VERSION);
    await page.fill('input[name="uri"]', DS_URI);
    await page.fill('input[name="tags"]', `team:ml, env:e2e, run:${runId}`);

    // Submit. Dialog closes on 200; parent list reloads.
    await page.click('button.mat-mdc-raised-button:has-text("Register"), button[color="primary"]:has-text("Register")');
    // The register button in the page header matches the same
    // selector, so wait for the dialog to close as the signal
    // instead of just waiting on the second click.
    await expect(page.locator('h2[mat-dialog-title]')).toHaveCount(0, { timeout: 10_000 });

    // Row appears in the table. Use hasText to scope to the row
    // with our unique name.
    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: DS_NAME });
    await expect(row).toBeVisible({ timeout: 10_000 });
    await expect(row).toContainText(DS_VERSION);
    await expect(row).toContainText('s3://helion/e2e-artifacts');
  });

  test('tag filter narrows the list to the registered entry and then restores it', async ({ authedPage: page }) => {
    await navigateTo(page, '/ml/datasets');
    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: DS_NAME });
    await expect(row).toBeVisible({ timeout: 10_000 });

    // Filter by a unique tag from the previous test (run:<runId>).
    // The filter-bar input has class .filter-input (see ml-shared.scss).
    await page.fill('.filter-input', `run:${runId}`);
    // Row should still be present.
    await expect(row).toBeVisible();
    // Clear filter.
    await page.fill('.filter-input', '');
    await expect(row).toBeVisible();

    // Filter by a non-matching value. Our row should disappear OR
    // the empty-filter-state message should appear.
    await page.fill('.filter-input', 'team:never-registered');
    await expect(row).toHaveCount(0);
  });

  test('rejects an http:// URI with the server-side error message', async ({ authedPage: page }) => {
    await navigateTo(page, '/ml/datasets');
    await page.click('button.btn-primary:has-text("Register")');
    await page.fill('input[name="name"]', `${DS_NAME}-bad`);
    await page.fill('input[name="version"]', 'v1');
    // http:// is not file:// or s3:// — the registry validator
    // rejects this at POST time with a 400 whose message contains
    // "file://". The dashboard surfaces that message verbatim.
    await page.fill('input[name="uri"]', 'http://evil.example.com/x');

    // Client-side canSubmit() also gates on the scheme prefix, so
    // the Register button may be disabled entirely. That's also a
    // valid rejection — assert one or the other.
    const submitBtn = page.locator('button[color="primary"]:has-text("Register")').last();
    const disabled = await submitBtn.isDisabled();
    if (!disabled) {
      await submitBtn.click();
      // Expect an error banner on the parent page once the dialog
      // closes and the API returns 400.
      await expect(page.locator('.error-banner')).toContainText(/file:\/\/|s3:\/\/|scheme/i, { timeout: 5_000 });
    }

    // Close the dialog either way so the afterAll doesn't find it open.
    const cancel = page.locator('button:has-text("Cancel")');
    if (await cancel.isVisible()) await cancel.click();
  });

  test('deletes the registered dataset', async ({ authedPage: page }) => {
    await navigateTo(page, '/ml/datasets');
    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: DS_NAME });
    await expect(row).toBeVisible({ timeout: 10_000 });

    // Delete uses window.confirm; auto-accept so the click proceeds.
    page.once('dialog', (d) => d.accept());
    await row.locator('button.btn-danger').click();

    // Row disappears (reload after success removes it).
    await expect(row).toHaveCount(0, { timeout: 10_000 });
  });
});
