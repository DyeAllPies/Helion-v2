// e2e/fixtures/cluster.fixture.ts
//
// Reads the root JWT from the coordinator's token file so E2E tests can
// authenticate against the dashboard.  The token file is written by the
// coordinator at startup when HELION_TOKEN_FILE is set (see
// docker-compose.e2e.yml).
//
// Feature 39 — the E2E coordinator serves REST over hybrid-PQC TLS
// (HELION_REST_TLS default-on). Node's global fetch (undici) needs
// to trust the coordinator's self-signed CA before the fixture's
// REST helpers or the in-spec fetch() calls will succeed;
// installCoordinatorCATrust() below wires that up at module import.
// The Playwright browser context sets `ignoreHTTPSErrors: true`
// instead — cert-chain validation is unit-tested elsewhere
// (internal/pqcrypto + tests/integration/security) and we don't
// need to re-prove it through the UI.
//
// Environment variables:
//   E2E_TOKEN_FILE — path to the root-token file (default: ../state/root-token)
//   E2E_TOKEN      — override: supply the JWT directly (skips file read)
//   E2E_CA_FILE    — coordinator CA PEM path (default: ../state/ca.pem
//                    or docker exec into the coordinator container)
//   E2E_CA_PEM     — override: supply the CA PEM directly (skips file read)
//   E2E_API_URL    — coordinator HTTPS base URL (default:
//                    https://localhost:8080)

import { execSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { Agent, setGlobalDispatcher } from 'undici';

/** Root directory of the helion-v2 project (two levels up from this file). */
const PROJECT_ROOT = path.resolve(__dirname, '..', '..', '..');

/**
 * Read the root JWT that the coordinator wrote at startup.
 *
 * Resolution order:
 *   1. E2E_TOKEN env var (CI injects this directly)
 *   2. Host file at E2E_TOKEN_FILE or ./state/root-token (bind mount)
 *   3. docker exec into the coordinator container (named volume — E2E overlay)
 */
export function getRootToken(): string {
  // 1. Direct override.
  if (process.env['E2E_TOKEN']) {
    return process.env['E2E_TOKEN'].trim();
  }

  // 2. Host file (works when ./state is a bind mount).
  const tokenPath = process.env['E2E_TOKEN_FILE']
    || path.join(PROJECT_ROOT, 'state', 'root-token');

  if (fs.existsSync(tokenPath)) {
    return fs.readFileSync(tokenPath, 'utf-8').trim();
  }

  // 3. Named volume — read from inside the coordinator container.
  // The E2E overlay uses a Docker volume instead of a host bind mount,
  // so the token file is only accessible inside the container.
  try {
    const token = execSync(
      'docker exec helion-coordinator cat //app/state/root-token',
      { encoding: 'utf-8', timeout: 5000 },
    ).trim();
    if (token) return token;
  } catch {
    // container not running or command failed
  }

  throw new Error(
    `Root token not found at ${tokenPath} or via docker exec.\n` +
    'Start the cluster first:\n' +
    '  docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d\n' +
    'Or set E2E_TOKEN env var directly.'
  );
}

/** Coordinator REST API base URL. */
export const API_URL = process.env['E2E_API_URL'] || 'https://localhost:8080';

/**
 * Resolve the coordinator's CA PEM via env override, host bind
 * mount, or `docker exec`. Returns null only when the cluster
 * genuinely isn't reachable — REST specs that need the CA will
 * then surface a clean error rather than silently trusting the
 * wrong cert.
 */
function getCoordinatorCAPem(): string | null {
  if (process.env['E2E_CA_PEM']) return process.env['E2E_CA_PEM'];
  const caPath = process.env['E2E_CA_FILE']
    || path.join(PROJECT_ROOT, 'state', 'ca.pem');
  if (fs.existsSync(caPath)) return fs.readFileSync(caPath, 'utf-8');
  try {
    const pem = execSync(
      'docker exec helion-coordinator cat //app/state/ca.pem',
      { encoding: 'utf-8', timeout: 5000 },
    );
    if (pem.trim()) return pem;
  } catch {
    // container not running or command failed
  }
  return null;
}

/**
 * Pin Node's global fetch dispatcher to the coordinator's CA.
 * Strict mode: a missing CA throws rather than falling through
 * to a trust-all agent. This matches the playwright.config.ts
 * SPKI pinning — both code paths refuse to talk to anything
 * other than the exact E2E coordinator cert.
 */
(function installCoordinatorCATrust() {
  const ca = getCoordinatorCAPem();
  if (!ca) {
    throw new Error(
      'Coordinator CA not available: start the cluster with\n' +
      '  docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d\n' +
      'or set E2E_CA_FILE / E2E_CA_PEM to pin a different trust anchor.\n' +
      'We refuse to fall back to rejectUnauthorized:false — a broad\n' +
      'trust-all in tests masks cert regressions.',
    );
  }
  setGlobalDispatcher(new Agent({ connect: { ca } }));
})();

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
        const body = await res.json();
        // GET /nodes returns { nodes: [...], total: N }
        // Node health field is "health":"healthy" (string), not "healthy":true (boolean)
        const nodes = (body.nodes ?? body) as Array<{ healthy?: boolean; health?: string }>;
        const healthy = nodes.filter(n => n.healthy || n.health === 'healthy').length;
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
  payload: { id: string; command: string; args?: string[]; priority?: number },
): Promise<{ id: string; status: string; priority?: number }> {
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

/** Retry policy for submitJobWithRetry. */
interface RetryPolicyDef {
  max_attempts: number;
  backoff?: string;
  initial_delay_ms?: number;
  max_delay_ms?: number;
  jitter?: boolean;
}

/**
 * Submit a job with a retry policy via the coordinator REST API.
 */
export async function submitJobWithRetry(
  token: string,
  payload: { id: string; command: string; args?: string[]; retry_policy: RetryPolicyDef },
): Promise<{ id: string; status: string; attempt: number }> {
  const res = await fetch(`${API_URL}/jobs`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    throw new Error(`POST /jobs (retry) failed ${res.status}: ${await res.text()}`);
  }
  return res.json();
}

/** Workflow job definition for submitWorkflow. */
interface WorkflowJobDef {
  name: string;
  command: string;
  args?: string[];
  depends_on?: string[];
  condition?: string;
}

/**
 * Submit a workflow via the coordinator REST API and return the created Workflow.
 */
export async function submitWorkflow(
  token: string,
  payload: { id: string; name: string; jobs: WorkflowJobDef[] },
): Promise<{ id: string; status: string; jobs: Array<{ name: string; job_id: string; job_status: string }> }> {
  const res = await fetch(`${API_URL}/workflows`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    throw new Error(`POST /workflows failed ${res.status}: ${await res.text()}`);
  }
  return res.json();
}

/**
 * Cancel a workflow via the coordinator REST API.
 */
export async function cancelWorkflow(
  token: string,
  workflowId: string,
): Promise<void> {
  const res = await fetch(`${API_URL}/workflows/${workflowId}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) {
    throw new Error(`DELETE /workflows/${workflowId} failed ${res.status}: ${await res.text()}`);
  }
}
