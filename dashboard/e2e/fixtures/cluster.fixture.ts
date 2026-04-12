// e2e/fixtures/cluster.fixture.ts
//
// Reads the root JWT from the coordinator's token file so E2E tests can
// authenticate against the dashboard.  The token file is written by the
// coordinator at startup when HELION_TOKEN_FILE is set (see
// docker-compose.e2e.yml).
//
// Environment variables:
//   E2E_TOKEN_FILE — path to the root-token file (default: ../state/root-token)
//   E2E_TOKEN      — override: supply the JWT directly (skips file read)
//   E2E_API_URL    — coordinator HTTP base URL (default: http://localhost:8080)

import * as fs from 'node:fs';
import * as path from 'node:path';

/** Root directory of the helion-v2 project (two levels up from this file). */
const PROJECT_ROOT = path.resolve(__dirname, '..', '..', '..');

/**
 * Read the root JWT that the coordinator wrote to disk.
 * Throws if the file doesn't exist — the cluster must be running first.
 */
export function getRootToken(): string {
  // Direct override — useful in CI where the token is injected as a secret.
  if (process.env['E2E_TOKEN']) {
    return process.env['E2E_TOKEN'].trim();
  }

  const tokenPath = process.env['E2E_TOKEN_FILE']
    || path.join(PROJECT_ROOT, 'state', 'root-token');

  if (!fs.existsSync(tokenPath)) {
    throw new Error(
      `Root token not found at ${tokenPath}.\n` +
      'Start the cluster first:\n' +
      '  docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d\n' +
      'Or set E2E_TOKEN env var directly.'
    );
  }

  return fs.readFileSync(tokenPath, 'utf-8').trim();
}

/** Coordinator REST API base URL. */
export const API_URL = process.env['E2E_API_URL'] || 'http://localhost:8080';

/**
 * Wait until the coordinator healthz endpoint responds 200.
 * Retries every 2 s for up to `timeoutMs`.
 */
export async function waitForCoordinator(timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  const url = `${API_URL}/healthz`;

  while (Date.now() < deadline) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      // not up yet
    }
    await new Promise(r => setTimeout(r, 2_000));
  }
  throw new Error(`Coordinator not reachable at ${url} after ${timeoutMs}ms`);
}

/**
 * Wait for at least `count` healthy nodes to appear in GET /nodes.
 * Requires a valid JWT for authentication.
 */
export async function waitForNodes(
  token: string,
  count = 1,
  timeoutMs = 60_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${API_URL}/nodes`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.ok) {
        const nodes = (await res.json()) as Array<{ healthy: boolean }>;
        const healthy = nodes.filter(n => n.healthy).length;
        if (healthy >= count) return;
      }
    } catch {
      // not ready yet
    }
    await new Promise(r => setTimeout(r, 3_000));
  }
  throw new Error(`Expected ${count} healthy node(s) within ${timeoutMs}ms`);
}

/**
 * Submit a job via the coordinator REST API and return the created Job.
 */
export async function submitJob(
  token: string,
  payload: { id: string; command: string; args?: string[] },
): Promise<{ id: string; status: string }> {
  const res = await fetch(`${API_URL}/jobs`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    throw new Error(`POST /jobs failed ${res.status}: ${await res.text()}`);
  }
  return res.json();
}
