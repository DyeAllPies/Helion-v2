// e2e/specs/ml-iris.spec.ts
//
// Feature 19 — iris end-to-end ML pipeline, UI-level acceptance.
//
// The companion script `scripts/run-iris-e2e.sh` asserts the same
// pipeline via REST (workflow → registry → lineage → serve →
// predict). This spec is the dashboard-side complement: does the
// frontend actually *render* the state the backend produced? A
// user-visible regression (table doesn't paint, DAG doesn't
// render, service chip stays "UNHEALTHY") slips past the REST
// harness but breaks the one guarantee the feature-19 spec makes:
// "can a normal person run an ML pipeline on Helion." A normal
// person uses the dashboard.
//
// Required cluster:
//   docker-compose.yml + docker-compose.e2e.yml + docker-compose.iris.yml
//     with COMPOSE_PROFILES=analytics,ml
//
// The iris.yml overlay swaps both nodes to the Python-capable
// image (Dockerfile.node-python); without it the iris workflow
// jobs fail with `python: command not found`. The CI wiring in
// .github/workflows/ci.yml's e2e-iris-ui job applies all three
// compose files before running this spec.
//
// Test setup (idempotent): beforeAll submits the iris workflow if
// it's not already present and waits for completed + serve ready.
// Re-runs against the same cluster reuse the registry entries.
//
// Covered UI:
//   1. /ml/pipelines                 — iris-wf-1 row appears
//   2. /ml/pipelines/iris-wf-1       — DAG panel renders + job cards
//                                      show ingest/preprocess/train/register
//                                      with completed status
//   3. /ml/datasets                  — iris/v1 row visible with s3:// URI
//   4. /ml/models                    — iris-logreg/v1 with lineage + metrics
//   5. /ml/services                  — iris-serve-1 ready with upstream URL

import { test, expect, navigateTo } from '../fixtures/auth.fixture';
import { API_URL, getRootToken } from '../fixtures/cluster.fixture';

const WF_ID = 'iris-wf-1';
const SERVE_JOB_ID = 'iris-serve-1';

interface WorkflowResp {
  status: string;
  jobs?: Array<{ name: string; job_id: string; job_status: string }>;
}

interface LineageResp {
  jobs: Array<{ name: string; outputs?: Array<{ name: string; uri: string }> }>;
}

interface ServiceResp {
  services?: Array<{ job_id: string; ready: boolean }>;
}

/** Fetch the iris workflow. Returns null on 404 (not yet submitted). */
async function getWorkflow(token: string): Promise<WorkflowResp | null> {
  const res = await fetch(`${API_URL}/workflows/${WF_ID}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(`GET /workflows/${WF_ID} ${res.status}: ${await res.text()}`);
  return res.json() as Promise<WorkflowResp>;
}

/**
 * POST the iris workflow. Uses HELION_API_URL=http://coordinator:8080
 * in each job's env so register.py can call back from inside the
 * cluster. Hardcoded to match examples/ml-iris/workflow.yaml; if
 * that file changes, re-sync this fixture.
 */
async function submitIrisWorkflow(token: string): Promise<void> {
  const jobEnv = {
    HELION_API_URL: 'http://coordinator:8080',
    HELION_TOKEN: token,
  };
  const body = {
    id: WF_ID,
    name: 'iris-end-to-end',
    priority: 60,
    jobs: [
      {
        name: 'ingest',
        command: 'python',
        args: ['/app/ml-iris/ingest.py'],
        env: jobEnv,
        timeout_seconds: 60,
        outputs: [{ name: 'RAW_CSV', local_path: 'raw.csv' }],
      },
      {
        name: 'preprocess',
        command: 'python',
        args: ['/app/ml-iris/preprocess.py'],
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
        args: ['/app/ml-iris/train.py'],
        env: jobEnv,
        timeout_seconds: 120,
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
        args: ['/app/ml-iris/register.py'],
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
  if (res.status === 409) return; // already submitted — idempotent re-run
  if (!res.ok) {
    throw new Error(`POST /workflows ${res.status}: ${await res.text()}`);
  }
}

/** Poll until workflow reaches terminal state or deadline. */
async function waitForWorkflowCompleted(token: string, timeoutMs = 180_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const wf = await getWorkflow(token);
    if (wf) {
      if (wf.status === 'completed') return;
      if (wf.status === 'failed' || wf.status === 'cancelled') {
        throw new Error(`workflow ${WF_ID} terminal ${wf.status} during setup`);
      }
    }
    await new Promise(r => setTimeout(r, 3_000));
  }
  throw new Error(`workflow ${WF_ID} did not complete within ${timeoutMs}ms`);
}

/**
 * Resolve the train job's MODEL output URI from the lineage endpoint.
 * The serve job needs the concrete s3:// URI; register.py stamps
 * file:// into the registry on local backends (documented gap),
 * so the lineage endpoint is the authoritative source.
 */
async function resolveModelURI(token: string): Promise<string> {
  const res = await fetch(`${API_URL}/workflows/${WF_ID}/lineage`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`GET lineage ${res.status}: ${await res.text()}`);
  const l: LineageResp = await res.json();
  for (const j of l.jobs) {
    if (j.name === 'train') {
      for (const o of j.outputs ?? []) {
        if (o.name === 'MODEL') return o.uri;
      }
    }
  }
  throw new Error('train.MODEL output URI missing from lineage');
}

/** Submit iris-serve-1 if not already present. */
async function submitServeJob(token: string): Promise<void> {
  const modelURI = await resolveModelURI(token);
  const body = {
    id: SERVE_JOB_ID,
    command: 'uvicorn',
    args: ['serve:app', '--host', '0.0.0.0', '--port', '8000'],
    env: { PYTHONPATH: '/app/ml-iris' },
    inputs: [{ name: 'MODEL', uri: modelURI, local_path: 'model.joblib' }],
    service: { port: 8000, health_path: '/healthz', health_initial_ms: 2000 },
  };
  const res = await fetch(`${API_URL}/jobs`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (res.status === 409) return; // already submitted
  if (!res.ok) {
    throw new Error(`POST /jobs (serve) ${res.status}: ${await res.text()}`);
  }
}

async function waitForServiceReady(token: string, timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const res = await fetch(`${API_URL}/api/services`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (res.ok) {
      const body: ServiceResp = await res.json();
      const s = (body.services ?? []).find(x => x.job_id === SERVE_JOB_ID);
      if (s?.ready) return;
    }
    await new Promise(r => setTimeout(r, 2_000));
  }
  throw new Error(`service ${SERVE_JOB_ID} not ready within ${timeoutMs}ms`);
}

test.describe('Feature 19 — iris pipeline through the dashboard', () => {
  test.beforeAll(async () => {
    // 3+ minutes of setup budget: workflow (~10s on a warm cluster)
    // + serve probe interval (~5s). Playwright's default 30s won't
    // cover a cold docker cache.
    test.setTimeout(300_000);
    const token = getRootToken();
    await submitIrisWorkflow(token);
    await waitForWorkflowCompleted(token);
    await submitServeJob(token);
    await waitForServiceReady(token);
  });

  test('pipelines list shows iris-wf-1 with completed status', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines/);
    await expect(page.locator('h1.page-title')).toContainText(/ML · PIPELINES/i);

    // The pipeline list renders a row per workflow; scope by
    // hasText on the unique workflow id so we don't match a prior
    // run from a different spec file.
    const row = page.locator('tr').filter({ hasText: WF_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row).toContainText(/completed/i);
  });

  test('pipeline detail renders the DAG with all four jobs + artifact flow', async ({ authedPage: page }) => {
    // Navigate via the list's "View DAG" link so the in-memory JWT
    // is preserved. page.goto() forces a full reload which drops the
    // Angular auth service state and redirects to /login.
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    const listRow = page.locator('tr').filter({ hasText: WF_ID });
    await expect(listRow).toBeVisible({ timeout: 15_000 });
    await listRow.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
    await expect(page.locator('h1.page-title')).toContainText(new RegExp(`PIPELINE · ${WF_ID}`, 'i'));

    // The DAG panel is explicitly titled; its presence is the
    // "mermaid render produced output" signal without coupling
    // to mermaid's internal DOM.
    await expect(page.locator('.dag-panel__header')).toContainText(/DAG/i, { timeout: 10_000 });

    // Each job renders as a card with its workflow-local name in a
    // monospaced header span and a status chip. Filter by the header
    // span exact-match so `ingest` doesn't match the preprocess card
    // (which contains "deps: ingest" in its body).
    for (const jobName of ['ingest', 'preprocess', 'train', 'register']) {
      const card = page.locator('.job-card').filter({
        has: page.locator('.job-card__header span.mono', {
          hasText: new RegExp(`^${jobName}$`),
        }),
      });
      await expect(card).toBeVisible();
      await expect(card).toContainText(/completed/i);
    }

    // The preprocess → train → register chain carries `from:` refs,
    // so at least one artifact edge is declared. The legend is
    // visible whenever the DAG panel renders, regardless of edge
    // count — asserts the render actually ran without errors.
    await expect(page.locator('.dag-legend')).toContainText(/artifact/i);
  });

  test('datasets view shows iris/v1 with a non-empty URI', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets/);

    // The register step stamps source=uci and task=classification
    // tags onto the entry; filtering by name-and-version in the row
    // selector is enough to assert presence without coupling to
    // pagination order.
    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'iris' });
    await expect(row).toBeVisible({ timeout: 10_000 });
    await expect(row).toContainText('v1');
    // URI may be s3:// or file:// depending on whether the Stager
    // surfaced HELION_INPUT_RAW_CSV_URI (documented gap #3 in the
    // iris README). Either scheme proves the registry round-trip.
    await expect(row).toContainText(/(s3|file):\/\//);
  });

  test('models view shows iris-logreg/v1 with lineage + metrics', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Models');
    await expect(page).toHaveURL(/\/ml\/models/);

    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'iris-logreg' });
    await expect(row).toBeVisible({ timeout: 10_000 });
    await expect(row).toContainText('v1');
    await expect(row).toContainText(/sklearn/i);
    // Lineage cell should link back to the training job + the
    // source dataset. The job link renders with text "job: <id>".
    await expect(row.locator('a').first()).toContainText(/job:\s*iris-wf-1\/train/);
    await expect(row).toContainText(/dataset:\s*iris\s*v1/);
    // Metrics pills use the `.metric-pill` class; the train step
    // reports at least `accuracy` and `f1_macro`.
    const pills = row.locator('.metric-pill');
    await expect(pills.filter({ hasText: /accuracy\s*=/ })).toHaveCount(1);
    await expect(pills.filter({ hasText: /f1_macro\s*=/ })).toHaveCount(1);
  });

  test('services view shows iris-serve-1 as READY', async ({ authedPage: page }) => {
    await page.click('a.nav-link >> text=Services');
    await expect(page).toHaveURL(/\/ml\/services/);

    const row = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: SERVE_JOB_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    // READY chip confirms the prober has reported at least one
    // successful health check to the coordinator. The chip styling
    // is asserted via class so a test on the text alone doesn't
    // false-pass on a UNHEALTHY chip whose text happens to contain
    // the word.
    await expect(row.locator('.chip-ready')).toBeVisible();
    await expect(row.locator('.chip-ready')).toContainText(/READY/i);
    // Upstream URL points at the node's service port (not the node's
    // gRPC port); matching on :8000 proves the Port field survived
    // the buildUpstreamURL plumbing.
    await expect(row).toContainText(/:8000/);
  });
});
