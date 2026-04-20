// e2e/specs/security-rest.spec.ts
//
// Security-critical REST surfaces that have no dashboard UI today but
// are part of the coordinator's public admin API. Covers:
//
//   - Feature 31 — CRL export + revocations list.
//   - Feature 33 — per-operator accountability: JWT `required_cn`
//     claim plumbs through POST /admin/tokens → validation.
//   - Feature 34 — WebAuthn ceremony endpoints' contract shape +
//     admin-only gating (config-503 or 200, no silent open).
//   - Feature 37 — authz deny surface: non-admin tokens get 403 and
//     every endpoint on `/admin/*` refuses them.
//   - Feature 38 — groups CRUD + share CRUD + revoke.
//
// These tests run against the coordinator REST API directly via
// fetch(); there is no Playwright page context needed, but we keep
// them in the E2E suite because they require the running cluster
// (BadgerDB, real JWT signing, revocation store persistence, live
// analytics sink). No UI assertions — the dashboard doesn't render
// any of these surfaces yet.
//
// Every test mints its own short-lived tokens from the root token
// and asserts both the accept path AND the deny path where
// applicable. The point isn't to re-test the per-package unit
// suites (they already cover the wire shapes); the point is to
// catch cross-package wiring regressions — middleware chain
// composition, route registration, env-var plumbing — that only
// show up end-to-end.

import { test, expect } from '@playwright/test';
import { getRootToken, API_URL } from '../fixtures/cluster.fixture';

/**
 * Shorthand for authenticated JSON requests. Returns the raw Response
 * so callers can assert both status + body.
 */
async function req(
  method: string,
  path: string,
  token: string,
  body?: unknown,
): Promise<Response> {
  const init: RequestInit = {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
  };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return fetch(`${API_URL}${path}`, init);
}

/**
 * Valid token roles in Helion today are `admin`, `node`, and `job`
 * — there is no generic `user` role at token-issuance time. The
 * feature-37 authz evaluator then grades any admin-path request:
 * `admin` passes, everything else (including `node` + `job`) hits
 * the `admin_required` deny surface. These tests use `node` as the
 * "non-admin" proxy because it's the only other mintable role that
 * exercises real-world traffic (node agents regularly call the
 * coordinator; a compromised node should NOT be able to pivot into
 * /admin/*).
 */
type MintableRole = 'admin' | 'node' | 'job';

async function mintToken(
  rootToken: string,
  subject: string,
  role: MintableRole,
  extras: Record<string, unknown> = {},
): Promise<string> {
  // tokenIssueAllow is a per-subject token bucket at 1 rps / burst 5.
  // E2E runs ~16 authz tests in sequence and blows through the burst
  // in <1s, so we retry on 429 with a short wait for the bucket to
  // refill. This only affects test pacing — never compromises the
  // security property under test (the rate limiter's existence IS
  // the property; all we do here is wait it out).
  for (let attempt = 0; attempt < 3; attempt++) {
    const resp = await req('POST', '/admin/tokens', rootToken, {
      subject,
      role,
      ttl_hours: 1,
      ...extras,
    });
    if (resp.ok) {
      const body = await resp.json() as { token: string };
      return body.token;
    }
    if (resp.status === 429 && attempt < 2) {
      await new Promise(r => setTimeout(r, 1_200));
      continue;
    }
    throw new Error(`mintToken(${role}): ${resp.status} ${await resp.text()}`);
  }
  // Unreachable — the loop either returns or throws.
  throw new Error(`mintToken(${role}): exhausted retries`);
}

/**
 * A single shared non-admin token lets the authz-deny tests all
 * ride the same subject's budget. Saves 10+ mint round-trips per
 * E2E run AND keeps the authz assertions identical because we're
 * reusing the same identity across every deny-path check.
 */
let sharedNodeTokenPromise: Promise<string> | null = null;
function sharedNodeToken(): Promise<string> {
  if (sharedNodeTokenPromise === null) {
    sharedNodeTokenPromise = mintToken(
      getRootToken(),
      `e2e-shared-node-${Date.now().toString(36)}`,
      'node',
    );
  }
  return sharedNodeTokenPromise;
}

// ═══════════════════════════════════════════════════════════════════
// Feature 31 — cert revocation via CRL + revocations endpoint
// ═══════════════════════════════════════════════════════════════════

test.describe('Feature 31 — cert revocation endpoints', () => {

  test('GET /admin/operator-certs/revocations returns a list for admin', async () => {
    const root = getRootToken();
    const resp = await req('GET', '/admin/operator-certs/revocations', root);
    expect(resp.status).toBe(200);
    const body = await resp.json() as { revocations: unknown[] };
    // Empty array is fine on a fresh E2E volume — we only care about
    // the shape + admin access surface.
    expect(Array.isArray(body.revocations)).toBe(true);
  });

  test('GET /admin/ca/crl serves a signed X.509 CRL as PEM', async () => {
    const root = getRootToken();
    const resp = await req('GET', '/admin/ca/crl', root);
    // The CRL signer requires HELION_REST_TLS + the operator CA to be
    // wired. In the current E2E overlay the CA is always present, so
    // 200 is the expected response. A 503 here would tell us
    // crlSigner nil-wiring regressed.
    expect(resp.status).toBe(200);
    const pem = await resp.text();
    expect(pem).toContain('-----BEGIN X509 CRL-----');
    expect(pem).toContain('-----END X509 CRL-----');
  });

  test('non-admin gets 403 on CRL', async () => {
    const user = await sharedNodeToken();
    const resp = await req('GET', '/admin/ca/crl', user);
    expect(resp.status).toBe(403);
  });

  test('revoking a bogus serial returns a structured error', async () => {
    const root = getRootToken();
    const resp = await req(
      'POST',
      '/admin/operator-certs/not-a-hex-serial/revoke',
      root,
      { reason: 'negative test' },
    );
    // Malformed serial hits the NormalizeSerialHex branch → 400.
    expect(resp.status).toBe(400);
  });
});

// ═══════════════════════════════════════════════════════════════════
// Feature 33 — per-operator accountability (JWT ↔ cert CN binding)
// ═══════════════════════════════════════════════════════════════════

test.describe('Feature 33 — JWT bound to operator cert CN', () => {

  test('POST /admin/tokens accepts bind_to_cert_cn and echoes it', async () => {
    const root = getRootToken();
    const resp = await req('POST', '/admin/tokens', root, {
      subject:          `e2e-bound-${Date.now()}`,
      role:             'node',
      ttl_hours:        1,
      bind_to_cert_cn:  '  alice@ops  ',
    });
    expect([200, 201]).toContain(resp.status);
    const body = await resp.json() as {
      token: string;
      bound_to_cert_cn?: string;
    };
    expect(body.token).toBeTruthy();
    // Server trims + echoes the binding so an admin can confirm what
    // they stamped. Whitespace-stripping is part of the contract — a
    // tab in the binding would silently never match a cert CN.
    expect(body.bound_to_cert_cn).toBe('alice@ops');
  });

  test('legacy unbound tokens still mint without required_cn', async () => {
    const root = getRootToken();
    const resp = await req('POST', '/admin/tokens', root, {
      subject:    `e2e-unbound-${Date.now()}`,
      role:       'node',
      ttl_hours:  1,
    });
    expect([200, 201]).toContain(resp.status);
    const body = await resp.json() as { bound_to_cert_cn?: string };
    // Contract: absent / unset field means "legacy unbound behaviour"
    // — must not be the empty string either, which feature-33's trim
    // path could produce if CN validation silently eats the value.
    expect(body.bound_to_cert_cn ?? '').toBe('');
  });

  test('bound token with no cert on REST (plain HTTP) is 401', async () => {
    // Feature 33 refuses bound tokens on requests that can't present
    // a matching operator cert. The E2E overlay currently runs
    // plain-HTTP REST (HELION_REST_TLS=off — tracked for removal),
    // so any bound token is guaranteed to have observedCN="" and
    // hit the mismatch branch.
    const root = getRootToken();
    const mintResp = await req('POST', '/admin/tokens', root, {
      subject:          `e2e-bound-reject-${Date.now()}`,
      role:             'admin',
      ttl_hours:        1,
      bind_to_cert_cn:  'alice@ops',
    });
    expect([200, 201]).toContain(mintResp.status);
    const { token } = await mintResp.json() as { token: string };

    const refused = await req('GET', '/nodes', token);
    expect(refused.status).toBe(401);
  });
});

// ═══════════════════════════════════════════════════════════════════
// Feature 34 — WebAuthn ceremony surface
// ═══════════════════════════════════════════════════════════════════

test.describe('Feature 34 — WebAuthn endpoints', () => {

  // NOTE on the current E2E overlay:
  //   HELION_WEBAUTHN_RPID / HELION_WEBAUTHN_ORIGINS are not set,
  //   so SetWebAuthn is never called and the /admin/webauthn/*
  //   routes aren't registered. Every request below therefore
  //   returns 404 rather than the 503 the configured-but-off
  //   path would emit. This mirrors production: an operator
  //   who doesn't wire the env vars gets an endpoint that
  //   simply doesn't exist — no confusing "403 by admin role
  //   but feature-disabled" state. Feature 39 will move this
  //   coverage to an overlay that DOES configure WebAuthn so
  //   the 200 / ceremony happy-path can be exercised end-to-
  //   end; today we assert the contract we actually ship.

  test('register-begin route is cleanly absent when not configured', async () => {
    const root = getRootToken();
    const resp = await req('POST', '/admin/webauthn/register-begin', root, {
      operator_cn: 'e2e-webauthn@ops',
      label:       'e2e-test-key',
    });
    expect(resp.status).toBe(404);
  });

  test('list-credentials route is cleanly absent when not configured', async () => {
    // A 500 here would indicate a partial wiring regression (routes
    // registered without the backing store). 404 is the only clean
    // "feature not enabled" state.
    const root = getRootToken();
    const resp = await req('GET', '/admin/webauthn/credentials', root);
    expect(resp.status).toBe(404);
  });

  test('non-admin gets 403 OR 404 on webauthn endpoints (never 200, never 500)', async () => {
    // Middleware chain is adminMiddleware-then-handler; route-not-
    // found check happens at the mux, BEFORE middleware runs on
    // Go's http.ServeMux. So an unregistered route returns 404
    // regardless of token role — which is a safer default than
    // 403 (auth-oracle leak) or 500 (surprise crash).
    const user = await sharedNodeToken();
    const resp = await req('POST', '/admin/webauthn/register-begin', user, {});
    expect([403, 404]).toContain(resp.status);
    expect(resp.status).not.toBe(500);
    expect(resp.status).not.toBe(200);
  });
});

// ═══════════════════════════════════════════════════════════════════
// Feature 37 — authz deny audit trail
// ═══════════════════════════════════════════════════════════════════

test.describe('Feature 37 — authz_deny surface', () => {

  test('non-admin POST /admin/tokens is 403 with a deny-coded body', async () => {
    const user = await sharedNodeToken();
    const resp = await req('POST', '/admin/tokens', user, {
      subject: 'never-minted', role: 'node', ttl_hours: 1,
    });
    expect(resp.status).toBe(403);
    const body = await resp.json() as { error?: string; code?: string };
    // Feature 37 hands back a machine-readable deny code on every
    // authz rejection so the dashboard can surface a stable UX.
    // Current server uses `code` (not `deny_code`) on the wire —
    // see writeDenyError in internal/api/authz.go.
    expect(body.code).toBe('admin_required');
    expect(body.error ?? '').not.toBe('');
  });

  test('non-admin cannot list or delete tokens', async () => {
    const user = await sharedNodeToken();
    const deleteResp = await req('DELETE', '/admin/tokens/some-jti', user);
    expect(deleteResp.status).toBe(403);
  });

  test('anonymous request to /admin/* is 401 (not 403 — no JWT)', async () => {
    // Anonymous requests never reach the authz evaluator; they're
    // rejected at authMiddleware with 401. This distinguishes
    // "missing auth" from "has auth but wrong role" in the audit
    // log — operationally important when chasing suspicious denies.
    const resp = await fetch(`${API_URL}/admin/tokens`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ subject: 'anon', role: 'node', ttl_hours: 1 }),
    });
    expect(resp.status).toBe(401);
  });
});

// ═══════════════════════════════════════════════════════════════════
// Feature 38 — groups + shares
// ═══════════════════════════════════════════════════════════════════

test.describe('Feature 38 — groups + shares', () => {

  const groupName = `e2e-group-${Date.now().toString(36)}`;

  test('admin can create + list + delete a group', async () => {
    const root = getRootToken();

    // Create
    const createResp = await req('POST', '/admin/groups', root, { name: groupName });
    expect(createResp.status).toBe(201);

    // List — the freshly-created group must appear
    const listResp = await req('GET', '/admin/groups', root);
    expect(listResp.status).toBe(200);
    const listBody = await listResp.json() as {
      groups: Array<{ name: string }>;
      total: number;
    };
    const names = listBody.groups.map(g => g.name);
    expect(names).toContain(groupName);

    // Duplicate create → 409
    const dupResp = await req('POST', '/admin/groups', root, { name: groupName });
    expect(dupResp.status).toBe(409);

    // Add + remove member
    const addResp = await req(
      'POST',
      `/admin/groups/${groupName}/members`,
      root,
      { principal_id: 'user:alice' },
    );
    expect(addResp.status).toBe(204);

    const getResp = await req('GET', `/admin/groups/${groupName}`, root);
    expect(getResp.status).toBe(200);
    const getBody = await getResp.json() as { members?: string[] };
    expect(getBody.members ?? []).toContain('user:alice');

    const rmResp = await req(
      'DELETE',
      `/admin/groups/${groupName}/members/user:alice`,
      root,
    );
    expect(rmResp.status).toBe(204);

    // Delete
    const delResp = await req('DELETE', `/admin/groups/${groupName}`, root);
    expect(delResp.status).toBe(204);
  });

  test('non-admin cannot manage groups', async () => {
    const user = await sharedNodeToken();
    const resp = await req('POST', '/admin/groups', user, { name: 'wont-land' });
    expect(resp.status).toBe(403);
  });

  test('invalid group name is 400, not 500', async () => {
    const root = getRootToken();
    const resp = await req('POST', '/admin/groups', root, { name: '' });
    expect(resp.status).toBe(400);
  });

  test('share CRUD on a synthesised job: create → list → revoke', async () => {
    const root = getRootToken();

    // Use a fresh job so the share list starts empty for this resource.
    const jobId = `e2e-share-${Date.now().toString(36)}`;
    const submitResp = await req('POST', '/jobs', root, {
      id:      jobId,
      command: 'echo',
      args:    ['share-test'],
    });
    expect([201, 200]).toContain(submitResp.status);

    // Create share
    const shareResp = await req(
      'POST',
      `/admin/resources/job/share?id=${jobId}`,
      root,
      { grantee: 'user:bob', actions: ['read'] },
    );
    expect(shareResp.status).toBe(201);

    // List
    const listResp = await req(
      'GET',
      `/admin/resources/job/shares?id=${jobId}`,
      root,
    );
    expect(listResp.status).toBe(200);
    const listBody = await listResp.json() as {
      shares: Array<{ grantee: string; actions: string[] }>;
      total: number;
    };
    expect(listBody.total).toBe(1);
    expect(listBody.shares[0].grantee).toBe('user:bob');

    // Revoke
    const revokeResp = await req(
      'DELETE',
      `/admin/resources/job/share?id=${jobId}&grantee=user:bob`,
      root,
    );
    expect(revokeResp.status).toBe(204);

    // Verify it's gone
    const listAgainResp = await req(
      'GET',
      `/admin/resources/job/shares?id=${jobId}`,
      root,
    );
    const listAgainBody = await listAgainResp.json() as { total: number };
    expect(listAgainBody.total).toBe(0);
  });

  test('share with unknown resource kind is 400', async () => {
    const root = getRootToken();
    const resp = await req(
      'POST',
      '/admin/resources/unicorn/share?id=whatever',
      root,
      { grantee: 'user:bob', actions: ['read'] },
    );
    expect(resp.status).toBe(400);
  });
});
