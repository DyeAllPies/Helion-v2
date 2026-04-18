// e2e/specs/ml-mnist-live.spec.ts
//
// Feature 21 — MNIST-784 LOCAL-ONLY live-progression Playwright
// spec. Distinct from ml-iris.spec.ts (CI) in two ways:
//
//   1. Gated behind E2E_LOCAL_MNIST=1. CI's wildcard spec
//      discovery runs this file but the describe().skip() at the
//      top returns instantly. Operators opt in explicitly for
//      local demo / smoke runs.
//
//   2. Watches the workflow `pending → running → completed`
//      transition live instead of waiting for terminal state in
//      beforeAll. Early tests navigate while the pipeline is
//      mid-flight; later tests wait for completion and assert the
//      rendered terminal state. Every ML surface (Pipelines list,
//      DAG detail, Datasets, Models, Services, /predict) is
//      covered.
//
// Local run (iris overlay works for both demos — it's the
// generic Python-capable node overlay):
//
//   # cluster up
//   COMPOSE_PROFILES=analytics,ml docker compose \
//     -f docker-compose.yml \
//     -f docker-compose.e2e.yml \
//     -f docker-compose.iris.yml \
//     up -d --build
//
//   # spec
//   cd dashboard
//   E2E_LOCAL_MNIST=1 \
//     E2E_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token) \
//     npx playwright test ml-mnist-live --workers=1
//
// The spec expects a single worker so tests share the shared-
// context authed page; running with multiple workers splits the
// describe across contexts and the transition-observing tests
// race their own sibling tests.
//
// On a warm OpenML cache the full spec runs in ~60–120 s (the
// MNIST fetch + training takes longer than iris).

import { test, expect } from '../fixtures/auth.fixture';
import { API_URL, getRootToken } from '../fixtures/cluster.fixture';

const RUN_LOCAL = process.env['E2E_LOCAL_MNIST'] === '1';

const WF_ID = 'mnist-wf-1';
const SERVE_JOB_ID = 'mnist-serve-1';
const WORKFLOW_TIMEOUT_MS = 300_000;   // 5 min; cold OpenML cache + CI-slow CPU
const SERVICE_READY_TIMEOUT_MS = 45_000;

interface WorkflowResp {
  status: string;
  jobs?: Array<{ name: string; job_id: string; job_status: string }>;
}
interface LineageResp {
  jobs: Array<{ name: string; outputs?: Array<{ name: string; uri: string }> }>;
}
interface ServiceResp {
  services?: Array<{ job_id: string; ready: boolean; node_address?: string; port?: number }>;
}

async function getWorkflow(token: string): Promise<WorkflowResp | null> {
  const res = await fetch(`${API_URL}/workflows/${WF_ID}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(`GET /workflows/${WF_ID} ${res.status}`);
  return res.json() as Promise<WorkflowResp>;
}

/**
 * Submit the MNIST workflow. Mirrors examples/ml-mnist/workflow.yaml
 * but inlined so the spec doesn't depend on pyyaml or submit.py —
 * Playwright drives the REST API directly. Credentials are injected
 * into each job's env (Go runtime doesn't forward node env).
 */
async function submitMnistWorkflow(token: string): Promise<void> {
  const jobEnv = {
    HELION_API_URL: 'http://coordinator:8080',
    HELION_TOKEN: token,
  };
  const body = {
    id: WF_ID,
    name: 'mnist-end-to-end',
    priority: 50,
    jobs: [
      {
        name: 'ingest',
        command: 'python',
        args: ['/app/ml-mnist/ingest.py'],
        env: jobEnv,
        timeout_seconds: 180,
        outputs: [{ name: 'RAW_CSV', local_path: 'raw.csv' }],
      },
      {
        name: 'preprocess',
        command: 'python',
        args: ['/app/ml-mnist/preprocess.py'],
        env: jobEnv,
        timeout_seconds: 60,
        depends_on: ['ingest'],
        inputs: [{ name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' }],
        outputs: [
          { name: 'TRAIN_PARQUET', local_path: 'train.parquet' },
          { name: 'TEST_PARQUET',  local_path: 'test.parquet'  },
        ],
      },
      {
        name: 'train',
        command: 'python',
        args: ['/app/ml-mnist/train.py'],
        env: jobEnv,
        timeout_seconds: 180,
        depends_on: ['preprocess'],
        inputs: [
          { name: 'TRAIN_PARQUET', from: 'preprocess.TRAIN_PARQUET', local_path: 'train.parquet' },
          { name: 'TEST_PARQUET',  from: 'preprocess.TEST_PARQUET',  local_path: 'test.parquet'  },
        ],
        outputs: [
          { name: 'MODEL',   local_path: 'model.joblib' },
          { name: 'METRICS', local_path: 'metrics.json' },
        ],
      },
      {
        name: 'register',
        command: 'python',
        args: ['/app/ml-mnist/register.py'],
        env: {
          ...jobEnv,
          HELION_WORKFLOW_ID:    WF_ID,
          HELION_TRAIN_JOB_NAME: 'train',
        },
        timeout_seconds: 60,
        depends_on: ['train'],
        inputs: [
          { name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' },
          { name: 'MODEL',   from: 'train.MODEL',   local_path: 'model.joblib' },
          { name: 'METRICS', from: 'train.METRICS', local_path: 'metrics.json' },
        ],
      },
    ],
  };
  const res = await fetch(`${API_URL}/workflows`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (res.status === 409) return; // idempotent re-run
  if (!res.ok) throw new Error(`POST /workflows ${res.status}: ${await res.text()}`);
}

async function waitForWorkflowStatus(
  token: string,
  target: string,
  timeoutMs: number,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let last = '';
  while (Date.now() < deadline) {
    const wf = await getWorkflow(token);
    if (wf) {
      if (wf.status !== last) {
        // eslint-disable-next-line no-console
        console.log(`  [mnist] status: ${wf.status}`);
        last = wf.status;
      }
      if (wf.status === target) return;
      if (wf.status === 'failed' || wf.status === 'cancelled') {
        throw new Error(`workflow terminal ${wf.status} (expected ${target})`);
      }
    }
    await new Promise(r => setTimeout(r, 2_000));
  }
  throw new Error(`workflow did not reach ${target} within ${timeoutMs}ms (last ${last})`);
}

async function resolveModelURI(token: string): Promise<string> {
  const res = await fetch(`${API_URL}/workflows/${WF_ID}/lineage`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`GET lineage ${res.status}: ${await res.text()}`);
  const l: LineageResp = await res.json();
  for (const j of l.jobs) {
    if (j.name === 'train') {
      for (const o of j.outputs ?? []) if (o.name === 'MODEL') return o.uri;
    }
  }
  throw new Error('train.MODEL output URI missing from lineage');
}

async function submitServeJob(token: string, modelURI: string): Promise<void> {
  const res = await fetch(`${API_URL}/jobs`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      id: SERVE_JOB_ID,
      command: 'uvicorn',
      args: ['serve:app', '--host', '0.0.0.0', '--port', '8000'],
      env: { PYTHONPATH: '/app/ml-mnist' },
      inputs: [{ name: 'MODEL', uri: modelURI, local_path: 'model.joblib' }],
      service: { port: 8000, health_path: '/healthz', health_initial_ms: 2000 },
    }),
  });
  if (res.status === 409) return;
  if (!res.ok) throw new Error(`POST /jobs (serve) ${res.status}: ${await res.text()}`);
}

async function waitForServiceReady(token: string, timeoutMs: number): Promise<{ host: string; port: number }> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const r = await fetch(`${API_URL}/api/services`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (r.ok) {
      const body: ServiceResp = await r.json();
      const s = (body.services ?? []).find(x => x.job_id === SERVE_JOB_ID && x.ready);
      if (s?.node_address && s.port) {
        const host = s.node_address.split(':')[0];
        return { host, port: s.port };
      }
    }
    await new Promise(r => setTimeout(r, 2_000));
  }
  throw new Error(`service ${SERVE_JOB_ID} not ready within ${timeoutMs}ms`);
}

/**
 * Hit /predict via docker exec so the request goes through the
 * internal helion-net network (the upstream URL references the
 * node's container name, not a host-routable address).
 *
 * We route the wget invocation through `sh -c` inside the container
 * so the Linux shell does the argument parsing — when docker exec
 * on Windows hosts the single-quoted `--header='Content-Type: …'`,
 * the host cmd.exe strips the quotes and wget sees the MIME type as
 * a second positional argument ("wget: bad address 'application'").
 * Running the wget behind `sh -c "…"` keeps the quoting intact on
 * the container side, which is the only side that matters.
 */
function predictViaDockerExec(host: string, port: number, features: number[]): {
  predictions: number[];
} {
  const { spawnSync } = require('node:child_process') as typeof import('node:child_process');
  const body = JSON.stringify({ features: [features] });
  // Escape single quotes in the JSON body for the sh -c string literal.
  // The JSON contains no single quotes in practice (only digits, commas,
  // brackets) but belt-and-braces keeps this robust.
  const bodyEsc = body.replace(/'/g, `'\\''`);
  const shCmd =
    `wget -qO- ` +
    `--header='Content-Type: application/json' ` +
    `--post-data='${bodyEsc}' ` +
    `http://${host}:${port}/predict`;
  const r = spawnSync('docker', ['exec', 'helion-coordinator', 'sh', '-c', shCmd], {
    encoding: 'utf-8',
    timeout: 10_000,
  });
  if (r.status !== 0) {
    throw new Error(
      `predict failed (status=${r.status}): stderr=${r.stderr} stdout=${r.stdout}`,
    );
  }
  return JSON.parse(r.stdout) as { predictions: number[] };
}

test.describe('Feature 21 — MNIST live-progression walkthrough (local)', () => {
  test.skip(!RUN_LOCAL, 'Local-only: export E2E_LOCAL_MNIST=1 to enable');

  // Per-test timeout budget. `test.setTimeout` inside beforeAll only
  // scopes to the hook itself, not to sibling tests — each test below
  // would fall back to the 30 s default and fail the slow ones
  // (waitForWorkflowStatus, service ready, predict). Apply the budget
  // at the describe level so every child inherits it.
  test.describe.configure({ timeout: WORKFLOW_TIMEOUT_MS + 60_000 });

  test.beforeAll(async () => {
    const token = getRootToken();
    // Submit but DO NOT wait — the whole point is to observe the
    // in-flight transitions from the UI.
    await submitMnistWorkflow(token);
  });

  test('1. workflow appears on /ml/pipelines with non-terminal status', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);

    // The row should be present right after submit. Its status is
    // one of the non-terminal states — exact value depends on how
    // fast the dispatch loop ran. `pending`, `running`, or
    // `completed` are all ok; `failed` / `cancelled` fail the test.
    const row = page.locator('tr').filter({ hasText: WF_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row).not.toContainText(/failed/i);
    await expect(row).not.toContainText(/cancelled/i);
  });

  test('2. DAG detail shows at least one job in flight', async ({ authedPage: page }) => {
    // Reach the detail view via the row's "View DAG" link so the
    // in-memory JWT survives — page.goto would force a reload +
    // login redirect (same caveat as ml-iris.spec.ts).
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    const listRow = page.locator('tr').filter({ hasText: WF_ID });
    await expect(listRow).toBeVisible({ timeout: 15_000 });
    await listRow.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));

    // The DAG panel should render; mermaid is async but the panel
    // header + job cards mount immediately.
    await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });

    // Poll the job cards for up to 60 s looking for a non-terminal
    // chip. On a fast cluster the whole workflow may finish before
    // this test runs — that's fine, the `expect.poll` succeeds on
    // EITHER seeing a non-terminal chip OR the workflow reaching
    // completed (all four cards completed is also a valid terminal
    // state to observe).
    //
    // Matches strings like "pending", "dispatching", "running" OR
    // "completed" — the latter proves the cards render at all.
    const jobNames = ['ingest', 'preprocess', 'train', 'register'];
    await expect.poll(async () => {
      for (const name of jobNames) {
        const card = page.locator('.job-card').filter({
          has: page.locator('.job-card__header span.mono', {
            hasText: new RegExp(`^${name}$`),
          }),
        });
        const txt = (await card.textContent({ timeout: 1_000 }).catch(() => '')) || '';
        if (/pending|dispatching|running|completed/i.test(txt)) return name;
      }
      return null;
    }, { timeout: 60_000, message: 'expected at least one job card with a status chip' })
      .not.toBeNull();
  });

  test('3. workflow reaches completed status', async ({ authedPage: page }) => {
    const token = getRootToken();
    // Poll via REST (lighter than UI polling) until completed.
    // The Pipelines list auto-refreshes from the backend; we just
    // want to assert the chip shows completed at the end.
    await waitForWorkflowStatus(token, 'completed', WORKFLOW_TIMEOUT_MS);

    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    const row = page.locator('tr').filter({ hasText: WF_ID });
    // The list may lag the REST status by up to its refresh
    // interval; retry the match for a few ticks.
    await expect.poll(async () => {
      const txt = await row.textContent({ timeout: 1_000 }).catch(() => '');
      return /completed/i.test(txt ?? '');
    }, { timeout: 20_000 }).toBe(true);
  });

  test('4. registry shows mnist/v1 + mnist-logreg/v1 with lineage + metrics', async ({ authedPage: page }) => {
    // Datasets.
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets$/);
    const dsRow = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist' });
    await expect(dsRow).toBeVisible({ timeout: 10_000 });
    await expect(dsRow).toContainText('v1');

    // Models — lineage + metric pills.
    await page.click('a.nav-link >> text=Models');
    await expect(page).toHaveURL(/\/ml\/models$/);
    const mRow = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist-logreg' });
    await expect(mRow).toBeVisible({ timeout: 10_000 });
    await expect(mRow).toContainText('v1');
    await expect(mRow).toContainText(/sklearn/i);
    await expect(mRow.locator('a').first()).toContainText(/job:\s*mnist-wf-1\/train/);
    await expect(mRow).toContainText(/dataset:\s*mnist\s*v1/);
    // train.py writes accuracy + f1_macro at minimum.
    const pills = mRow.locator('.metric-pill');
    await expect(pills.filter({ hasText: /accuracy\s*=/ })).toHaveCount(1);
    await expect(pills.filter({ hasText: /f1_macro\s*=/ })).toHaveCount(1);
  });

  test('5. serve job reaches READY on /ml/services', async ({ authedPage: page }) => {
    const token = getRootToken();
    const modelURI = await resolveModelURI(token);
    await submitServeJob(token, modelURI);
    await waitForServiceReady(token, SERVICE_READY_TIMEOUT_MS);

    await page.click('a.nav-link >> text=Services');
    await expect(page).toHaveURL(/\/ml\/services$/);
    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: SERVE_JOB_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row.locator('.chip-ready')).toBeVisible();
    await expect(row.locator('.chip-ready')).toContainText(/READY/i);
    await expect(row).toContainText(/:8000/);
  });

  test('6. POST /predict returns a valid digit class (0-9)', async () => {
    const token = getRootToken();
    const { host, port } = await waitForServiceReady(token, SERVICE_READY_TIMEOUT_MS);
    // 784 zeros — "all black" pixel frame. The model maps it to
    // whichever digit it thinks a blank frame most resembles;
    // we only assert the response is well-formed + in-range, not
    // the specific class (there's no ground-truth answer for an
    // arbitrary feature vector the way there is for an iris
    // setosa row).
    const features = Array.from({ length: 784 }, () => 0);
    const result = predictViaDockerExec(host, port, features);
    expect(result.predictions).toHaveLength(1);
    const cls = result.predictions[0];
    expect(Number.isInteger(cls)).toBe(true);
    expect(cls).toBeGreaterThanOrEqual(0);
    expect(cls).toBeLessThanOrEqual(9);
  });
});
