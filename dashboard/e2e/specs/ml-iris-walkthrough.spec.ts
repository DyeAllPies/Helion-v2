// e2e/specs/ml-iris-walkthrough.spec.ts
//
// Paced iris pipeline walkthrough for the docs video
// (docs/e2e-iris-run.mp4). Deliberately slower than
// ml-iris.spec.ts — inserts waitForTimeout() between navigation
// steps so a human viewer can read each panel.
//
// Skipped by default so it doesn't slow every CI run. Enable
// with E2E_RECORD_IRIS_WALKTHROUGH=1 plus E2E_VIDEO=1 to record:
//
//   # 1. cluster up with iris overlay (see iris README)
//   # 2. workflow + serve pre-populated (run-iris-e2e.sh once with
//   #    IRIS_SKIP_TEARDOWN=1, OR let this spec's beforeAll do it)
//   cd dashboard
//   E2E_RECORD_IRIS_WALKTHROUGH=1 E2E_VIDEO=1 \
//     E2E_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token) \
//     npx playwright test ml-iris-walkthrough --workers=1
//
// The resulting webm lives at
// test-results/page@<hash>.webm (one per worker context) and is
// stitched to docs/e2e-iris-run.mp4 by the ffmpeg one-liner in
// playwright.config.ts's header comment.

import { test, expect } from '../fixtures/auth.fixture';
import { API_URL, getRootToken } from '../fixtures/cluster.fixture';

const RECORDING_ENABLED = process.env['E2E_RECORD_IRIS_WALKTHROUGH'] === '1';

const WF_ID = 'iris-wf-1';
const SERVE_JOB_ID = 'iris-serve-1';

// Pacing — chosen so the video is watchable without being slow.
const PAUSE_SHORT  = 1_200;
const PAUSE_MEDIUM = 2_200;
const PAUSE_LONG   = 3_500;

interface WorkflowResp { status: string }
interface ServiceResp  { services?: Array<{ job_id: string; ready: boolean }> }
interface LineageResp  { jobs: Array<{ name: string; outputs?: Array<{ name: string; uri: string }> }> }

async function ensureWorkflow(token: string): Promise<void> {
  // If already completed (pre-populated by run-iris-e2e.sh), done.
  const res = await fetch(`${API_URL}/workflows/${WF_ID}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (res.ok) {
    const body: WorkflowResp = await res.json();
    if (body.status === 'completed') return;
  }
  // Otherwise: submit the minimal workflow shape (mirrors
  // ml-iris.spec.ts's helper but inlined here to keep this file
  // standalone since it runs off the main CI path). Kept aligned
  // with examples/ml-iris/workflow.yaml — 5 jobs with a parallel
  // train ‖ baseline fork (feature 42) and workflow-level tags
  // (feature 40b).
  const jobEnv = {
    HELION_API_URL: 'https://coordinator:8080',
    HELION_CA_FILE: '/app/state/ca.pem',
    HELION_TOKEN:   token,
  };
  const body = {
    id: WF_ID, name: 'iris-end-to-end', priority: 60,
    tags: { team: 'ml', task: 'iris-classification', env: 'ci' },
    jobs: [
      { name: 'ingest',     command: 'python', args: ['/app/ml-iris/ingest.py'],     env: jobEnv, timeout_seconds: 60,
        node_selector: { runtime: 'go' },
        outputs: [{ name: 'RAW_CSV', local_path: 'raw.csv' }] },
      { name: 'preprocess', command: 'python', args: ['/app/ml-iris/preprocess.py'], env: jobEnv, timeout_seconds: 60, depends_on: ['ingest'],
        node_selector: { runtime: 'go' },
        inputs:  [{ name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' }],
        outputs: [{ name: 'TRAIN_PARQUET', local_path: 'train.parquet' },
                  { name: 'TEST_PARQUET',  local_path: 'test.parquet'  }] },
      { name: 'train',      command: 'python', args: ['/app/ml-iris/train.py'],      env: jobEnv, timeout_seconds: 120, depends_on: ['preprocess'],
        node_selector: { runtime: 'go' },
        inputs:  [{ name: 'TRAIN_PARQUET', from: 'preprocess.TRAIN_PARQUET', local_path: 'train.parquet' },
                  { name: 'TEST_PARQUET',  from: 'preprocess.TEST_PARQUET',  local_path: 'test.parquet'  }],
        outputs: [{ name: 'MODEL',   local_path: 'model.joblib' },
                  { name: 'METRICS', local_path: 'metrics.json' }] },
      { name: 'baseline',   command: 'python', args: ['/app/ml-iris/baseline.py'],   env: jobEnv, timeout_seconds: 60, depends_on: ['preprocess'],
        node_selector: { runtime: 'go' },
        inputs:  [{ name: 'TRAIN_PARQUET', from: 'preprocess.TRAIN_PARQUET', local_path: 'train.parquet' },
                  { name: 'TEST_PARQUET',  from: 'preprocess.TEST_PARQUET',  local_path: 'test.parquet'  }],
        outputs: [{ name: 'METRICS', local_path: 'metrics.json' }] },
      { name: 'register',   command: 'python', args: ['/app/ml-iris/register.py'],
        env: { ...jobEnv, HELION_WORKFLOW_ID: WF_ID, HELION_TRAIN_JOB_NAME: 'train' },
        timeout_seconds: 60, depends_on: ['train'],
        node_selector: { runtime: 'go' },
        inputs: [{ name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' },
                 { name: 'MODEL',   from: 'train.MODEL',   local_path: 'model.joblib' },
                 { name: 'METRICS', from: 'train.METRICS', local_path: 'metrics.json' }] },
    ],
  };
  const sr = await fetch(`${API_URL}/workflows`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!sr.ok && sr.status !== 409) {
    throw new Error(`workflow submit ${sr.status}: ${await sr.text()}`);
  }
  const deadline = Date.now() + 180_000;
  while (Date.now() < deadline) {
    const r = await fetch(`${API_URL}/workflows/${WF_ID}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (r.ok) {
      const b: WorkflowResp = await r.json();
      if (b.status === 'completed') return;
      if (b.status === 'failed' || b.status === 'cancelled') {
        throw new Error(`workflow terminal ${b.status}`);
      }
    }
    await new Promise(r => setTimeout(r, 2_000));
  }
  throw new Error('workflow did not complete in time');
}

async function ensureServe(token: string): Promise<void> {
  const r = await fetch(`${API_URL}/api/services`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (r.ok) {
    const body: ServiceResp = await r.json();
    if ((body.services ?? []).some(s => s.job_id === SERVE_JOB_ID && s.ready)) return;
  }
  const lr = await fetch(`${API_URL}/workflows/${WF_ID}/lineage`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const lineage: LineageResp = await lr.json();
  let modelURI = '';
  for (const j of lineage.jobs) {
    if (j.name === 'train') {
      for (const o of j.outputs ?? []) if (o.name === 'MODEL') modelURI = o.uri;
    }
  }
  if (!modelURI) throw new Error('MODEL URI missing from lineage');
  await fetch(`${API_URL}/jobs`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      id: SERVE_JOB_ID,
      command: 'uvicorn',
      args: ['serve:app', '--host', '0.0.0.0', '--port', '8000'],
      env: { PYTHONPATH: '/app/ml-iris' },
      inputs: [{ name: 'MODEL', uri: modelURI, local_path: 'model.joblib' }],
      service: { port: 8000, health_path: '/healthz', health_initial_ms: 2000 },
    }),
  });
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const rr = await fetch(`${API_URL}/api/services`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (rr.ok) {
      const b: ServiceResp = await rr.json();
      if ((b.services ?? []).some(s => s.job_id === SERVE_JOB_ID && s.ready)) return;
    }
    await new Promise(r => setTimeout(r, 2_000));
  }
  throw new Error('service not ready');
}

test.describe('Feature 19 — iris walkthrough (video recording)', () => {
  test.skip(!RECORDING_ENABLED, 'Enable with E2E_RECORD_IRIS_WALKTHROUGH=1 to record docs/e2e-iris-run.mp4');

  test.beforeAll(async () => {
    test.setTimeout(300_000);
    const token = getRootToken();
    await ensureWorkflow(token);
    await ensureServe(token);
  });

  // Single test so the whole walkthrough records as one contiguous
  // video — avoids worker-context restarts that would produce
  // multiple webm chunks.
  test('iris pipeline through all four ML dashboard views', async ({ authedPage: page }) => {
    // Baseline: dashboard home (/nodes from the shared-page auth).
    await page.waitForTimeout(PAUSE_MEDIUM);

    // 1. Pipelines list — iris-wf-1 row visible with completed.
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    const row = page.locator('tr').filter({ hasText: WF_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row).toContainText(/completed/i);
    await page.waitForTimeout(PAUSE_LONG);

    // 2. DAG detail — click "View DAG" on the row.
    await row.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
    await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(PAUSE_LONG);
    await page.locator('.job-grid').scrollIntoViewIfNeeded();
    await page.waitForTimeout(PAUSE_LONG);

    // 3. Datasets.
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets$/);
    await expect(page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'iris' })).toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // 4. Models — hover a metric pill so the viewer notices the
    //    numbers.
    await page.click('a.nav-link >> text=Models');
    await expect(page).toHaveURL(/\/ml\/models$/);
    const modelRow = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'iris-logreg' });
    await expect(modelRow).toBeVisible({ timeout: 10_000 });
    await modelRow.locator('.metric-pill').first().hover();
    await page.waitForTimeout(PAUSE_LONG);

    // 5. Services — READY chip + upstream URL.
    await page.click('a.nav-link >> text=Services');
    await expect(page).toHaveURL(/\/ml\/services$/);
    await expect(page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: SERVE_JOB_ID })).toBeVisible({ timeout: 15_000 });
    await page.waitForTimeout(PAUSE_LONG);
  });
});
