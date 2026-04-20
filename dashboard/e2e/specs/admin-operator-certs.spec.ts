// e2e/specs/admin-operator-certs.spec.ts
//
// Feature 32 — operator-cert admin page. Covers everything the page
// can assert WITHOUT the POST /admin/operator-certs route, which is
// only registered when the REST listener runs on TLS (see
// cmd/helion-coordinator/main.go — SetOperatorCA is gated by the
// TLS branch because issuing a cert bundle + password over a
// plaintext wire is itself a security incident).
//
// The current E2E overlay runs plain HTTP (HELION_REST_TLS=off —
// tracked for removal in docs/planned-features/39-remove-rest-tls-
// opt-out.md). The full issue → download → revoke round-trip will
// unskip itself automatically once feature 39 lands and the
// overlay flips to HTTPS. Today we verify:
//
//   - Sidebar link is visible to admins (isAdmin$ wiring).
//   - Route loads the component + renders its three panels.
//   - Password-generation helper is wired (crypto.getRandomValues
//     path doesn't throw).
//   - Form-field validators fire on bad input (client-side mirror
//     of the server validator — stops obvious typos before round-
//     tripping).
//   - Server-side admin gate: non-admin POST is either 403 (route
//     registered, adminMiddleware refuses) OR 404 (route not
//     registered). Either is a safe outcome — what we guard
//     against is 200/201 on a non-admin token.
//
// Related features validated in passing:
//   - Feature 35 — isAdmin$ observable derives from the JWT role
//     claim (operator-certs nav link only renders when true).
//   - Feature 37 — adminMiddleware / mux 404 both refuse a
//     non-admin POST.
//   - Feature 39 — this spec's skipped tests are the canonical
//     regression set for the TLS flip.

import { test, expect } from '../fixtures/auth.fixture';
import { getRootToken, API_URL } from '../fixtures/cluster.fixture';

/**
 * The issue/revoke round-trip requires POST /admin/operator-certs
 * to be registered, which only happens under TLS. Detect once at
 * file scope so each skipped test has a clear, uniform reason.
 */
async function issuanceEndpointAvailable(): Promise<boolean> {
  // HEAD won't help (handler is POST-only), but a POST with a
  // deliberately bad body returns 400 when the route exists and
  // 404 when it doesn't. That's enough to distinguish the two
  // states without minting anything.
  const probe = await fetch(`${API_URL}/admin/operator-certs`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${getRootToken()}`,
      'Content-Type': 'application/json',
    },
    body: '{bad json',
  });
  return probe.status !== 404;
}

test.describe('Operator Certs admin page (feature 32)', () => {

  test('sidebar shows Operator Certs link for admin', async ({ authedPage: page }) => {
    // The shell renders admin-only nav entries behind isAdmin$. The
    // root token carries role=admin so the link MUST be present;
    // its absence is a regression in shell.component.ts isAdmin$
    // wiring or the app.routes.ts route registration.
    const link = page.locator('a.nav-link:has(.nav-link__label:text-is("Operator Certs"))');
    await expect(link).toBeVisible();
    await expect(link.locator('.material-icons')).toHaveText('verified_user');
  });

  test('clicking the link loads the page with all three panels', async ({ authedPage: page }) => {
    await page.click('a.nav-link:has(.nav-link__label:text-is("Operator Certs"))');
    await expect(page).toHaveURL(/\/admin\/operator-certs/);

    await expect(page.locator('h1:text-is("Operator Certificates")')).toBeVisible();
    await expect(page.locator('section.panel h2:text-is("Issue new cert")')).toBeVisible();
    await expect(page.locator('section.panel h2:text-is("Revoke a cert")')).toBeVisible();
    // Revocations list (empty state on fresh E2E volume).
    await expect(
      page.locator('section.panel h2').filter({ hasText: /Revoked certs/ }),
    ).toBeVisible();
  });

  test('generate-password button fills a ≥8-char value', async ({ authedPage: page }) => {
    await page.click('a.nav-link:has(.nav-link__label:text-is("Operator Certs"))');
    await expect(page).toHaveURL(/\/admin\/operator-certs/);

    const pwInput = page.locator('input[formControlName="p12_password"]');
    // Defensive: GEN wires to crypto.getRandomValues — a length
    // assertion lets a future entropy-source swap (e.g. to Web
    // Crypto deriveKey) remain spec-compliant without breaking
    // the test.
    await page.click('button.btn-ghost:has(.material-icons:text-is("casino"))');
    const value = await pwInput.inputValue();
    expect(value.length).toBeGreaterThanOrEqual(8);
  });

  test('bogus common name triggers the client-side validator', async ({ authedPage: page }) => {
    await page.click('a.nav-link:has(.nav-link__label:text-is("Operator Certs"))');
    await expect(page).toHaveURL(/\/admin\/operator-certs/);

    // CN with '=' is rejected by the client mirror (the server
    // validator refuses it too; mirror is defense-in-depth UX).
    await page.fill('input[formControlName="common_name"]', 'bad=cn@ops');
    await page.fill('input[formControlName="p12_password"]', 'longenoughpassword');
    // Issue button must be disabled when CN is invalid, regardless
    // of the rest of the form being filled.
    await expect(
      page.locator('button.btn--primary:has-text("Issue cert")'),
    ).toBeDisabled();
  });

  test('non-admin token cannot reach /admin/operator-certs POST handler', async () => {
    // Feature 37 contract: any /admin/* route refuses a non-admin
    // JWT server-side. This is load-bearing independent of the
    // client-side route guard (the route guard is UX-only — a
    // compromised dashboard must still fail closed server-side).
    // Mintable non-admin roles today are `node` and `job`.
    const rootToken = getRootToken();
    const mintResp = await fetch(`${API_URL}/admin/tokens`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${rootToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        subject:   `e2e-opcert-nonadmin-${Date.now()}`,
        role:      'node',
        ttl_hours: 1,
      }),
    });
    expect([200, 201]).toContain(mintResp.status);
    const { token: nodeToken } = await mintResp.json() as { token: string };

    const denyResp = await fetch(`${API_URL}/admin/operator-certs`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${nodeToken}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        common_name:  'deny-me@ops',
        p12_password: 'correcthorsebatterystaple',
      }),
    });
    // Two safe outcomes:
    //   - 403: TLS-on, route registered, adminMiddleware refuses.
    //   - 404: TLS-off (current E2E), route not registered; any
    //     caller including admins gets 404, which denies the
    //     non-admin by absence rather than by authz decision.
    // The invariant we guard is: NEVER 200 or 201 on a non-admin
    // token, because that would mean the feature-32 issuance ran.
    expect([403, 404]).toContain(denyResp.status);
    expect(denyResp.status).not.toBe(200);
    expect(denyResp.status).not.toBe(201);
  });

  // ── TLS-gated round-trip tests ──────────────────────────────
  //
  // These tests exercise the full UI issue → download-ack → revoke
  // flow. They require POST /admin/operator-certs to be registered,
  // which is gated by the TLS branch of cmd/helion-coordinator/main
  // .go. They'll skip cleanly on a plain-HTTP E2E overlay and run
  // once feature 39 flips the overlay to HTTPS.

  test('issue → download → revoke round-trip for a synthesised CN', async ({ authedPage: page }) => {
    test.skip(
      !(await issuanceEndpointAvailable()),
      'POST /admin/operator-certs requires TLS (see docs/planned-features/39-remove-rest-tls-opt-out.md)',
    );
    test.setTimeout(60_000);

    await page.click('a.nav-link:has(.nav-link__label:text-is("Operator Certs"))');
    await expect(page).toHaveURL(/\/admin\/operator-certs/);

    const cn = `e2e-cert-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}@ops`;

    await page.fill('input[formControlName="common_name"]', cn);
    await page.fill('input[formControlName="ttl_days"]', '1');
    await page.click('button.btn-ghost:has(.material-icons:text-is("casino"))');

    const [issueResp] = await Promise.all([
      page.waitForResponse(r =>
        r.url().includes('/admin/operator-certs') && r.request().method() === 'POST',
        { timeout: 15_000 },
      ),
      page.click('button.btn--primary:has-text("Issue cert")'),
    ]);
    expect(issueResp.status()).toBe(200);

    const resultPanel = page.locator('section.panel--result');
    await expect(resultPanel).toBeVisible({ timeout: 15_000 });

    const serial = (await resultPanel.locator('dl.kv dd').nth(1).textContent())?.trim() ?? '';
    expect(serial).toMatch(/^[0-9a-f]+$/);

    // Password must be visible exactly once; confirming disappears.
    await expect(resultPanel.locator('.pw-display')).toBeVisible();
    await resultPanel.locator('label.confirm input[type="checkbox"]').check();
    await expect(resultPanel.locator('.pw-display')).toHaveCount(0);

    // Revoke the just-issued serial.
    await page.fill('input[formControlName="serial"]', serial);
    await page.fill('input[formControlName="reason"]', 'e2e round-trip');
    const [revokeResp] = await Promise.all([
      page.waitForResponse(r =>
        r.url().includes(`/admin/operator-certs/${serial}/revoke`),
        { timeout: 15_000 },
      ),
      page.click('button.btn--primary:has-text("Revoke")'),
    ]);
    expect([200, 201]).toContain(revokeResp.status());

    // Revocations list updates on refresh.
    await page.click('button.btn--secondary:has(.material-icons:text-is("refresh"))');
    await expect(
      page.locator(`table.rev-table tbody tr:has(td.mono:text-is("${serial}"))`),
    ).toBeVisible({ timeout: 10_000 });
  });
});
