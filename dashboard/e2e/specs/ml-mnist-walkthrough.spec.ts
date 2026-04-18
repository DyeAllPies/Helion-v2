// e2e/specs/ml-mnist-walkthrough.spec.ts
//
// Paced MNIST pipeline walkthrough for the docs video
// (docs/e2e-mnist-run.mp4). Unlike ml-iris-walkthrough.spec.ts —
// which pre-completes the workflow in beforeAll and only films
// terminal-state navigation — this spec submits the workflow
// *inside* the recorded test so the viewer sees the full
// lifecycle: empty list → row appears in pending → DAG cards
// transition pending → dispatching → running → completed → back
// to the list showing completed → registry → services → READY.
//
// MNIST is the right demo for this shape because the workflow
// takes ~60 s end-to-end (iris finishes in ~10 s, which is too
// fast to film a transition), and the 784-feature LogisticRegression
// training is the observable "running" phase.
//
// Skipped by default. Enable with E2E_RECORD_MNIST_WALKTHROUGH=1
// plus E2E_VIDEO=1 to record:
//
//   # 1. cluster up with the iris overlay (it also serves MNIST):
//   COMPOSE_PROFILES=analytics,ml docker compose \
//     -f docker-compose.yml -f docker-compose.e2e.yml \
//     -f docker-compose.iris.yml up -d --build
//
//   # 2. record:
//   cd dashboard
//   E2E_RECORD_MNIST_WALKTHROUGH=1 E2E_VIDEO=1 \
//     E2E_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token) \
//     npx playwright test ml-mnist-walkthrough --workers=1
//
//   # 3. the resulting webm is at test-results/page@<hash>.webm.
//   #    Transcode to docs/e2e-mnist-run.mp4 via the ffmpeg one-liner
//   #    in playwright.config.ts's header comment (same as iris).
//
// The recording runs ~90–120 s depending on OpenML cache warmth.

import { test, expect } from '../fixtures/auth.fixture';
import { API_URL, getRootToken } from '../fixtures/cluster.fixture';

const RECORDING_ENABLED = process.env['E2E_RECORD_MNIST_WALKTHROUGH'] === '1';

const WF_ID = 'mnist-wf-1';
const SERVE_JOB_ID = 'mnist-serve-1';

// Pacing. Playwright caps per-test timeout at 30 s by default; the
// describe-level configure below extends it. Individual pauses are
// chosen so each panel is on screen long enough for a viewer to
// read but short enough that the total video stays under two
// minutes.
const PAUSE_SHORT  = 1_200;
const PAUSE_MEDIUM = 2_500;
const PAUSE_LONG   = 4_000;

interface WorkflowResp  { status: string }
interface ServiceResp   { services?: Array<{ job_id: string; ready: boolean }> }
interface LineageResp   { jobs: Array<{ name: string; outputs?: Array<{ name: string; uri: string }> }> }

/**
 * Submit the MNIST workflow. Mirrors examples/ml-mnist/workflow.yaml
 * inlined so the walkthrough is standalone (no pyyaml dep, no shell
 * exec). Idempotent — 409 is treated as success.
 */
async function submitMnistWorkflow(token: string): Promise<void> {
  const jobEnv = { HELION_API_URL: 'http://coordinator:8080', HELION_TOKEN: token };
  const body = {
    id: WF_ID, name: 'mnist-end-to-end', priority: 50,
    jobs: [
      {
        name: 'ingest',
        command: 'python', args: ['/app/ml-mnist/ingest.py'],
        env: jobEnv, timeout_seconds: 180,
        outputs: [{ name: 'RAW_CSV', local_path: 'raw.csv' }],
      },
      {
        name: 'preprocess',
        command: 'python', args: ['/app/ml-mnist/preprocess.py'],
        env: jobEnv, timeout_seconds: 60, depends_on: ['ingest'],
        inputs:  [{ name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' }],
        outputs: [
          { name: 'TRAIN_PARQUET', local_path: 'train.parquet' },
          { name: 'TEST_PARQUET',  local_path: 'test.parquet'  },
        ],
      },
      {
        name: 'train',
        command: 'python', args: ['/app/ml-mnist/train.py'],
        env: jobEnv, timeout_seconds: 180, depends_on: ['preprocess'],
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
        command: 'python', args: ['/app/ml-mnist/register.py'],
        env: { ...jobEnv, HELION_WORKFLOW_ID: WF_ID, HELION_TRAIN_JOB_NAME: 'train' },
        timeout_seconds: 60, depends_on: ['train'],
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
  if (res.status === 409) return;
  if (!res.ok) throw new Error(`workflow submit ${res.status}: ${await res.text()}`);
}

async function waitWorkflowCompleted(token: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
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

async function submitServeAndWaitReady(token: string, timeoutMs: number): Promise<void> {
  // Resolve MODEL uri via lineage so the serve job's input is bound
  // against the real artifact URI (not a local path).
  const lr = await fetch(`${API_URL}/workflows/${WF_ID}/lineage`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!lr.ok) throw new Error(`lineage ${lr.status}`);
  const lineage: LineageResp = await lr.json();
  let modelURI = '';
  for (const j of lineage.jobs) {
    if (j.name === 'train') {
      for (const o of j.outputs ?? []) if (o.name === 'MODEL') modelURI = o.uri;
    }
  }
  if (!modelURI) throw new Error('MODEL URI missing from lineage');

  const sr = await fetch(`${API_URL}/jobs`, {
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
  if (!sr.ok && sr.status !== 409) {
    throw new Error(`serve submit ${sr.status}: ${await sr.text()}`);
  }

  const deadline = Date.now() + timeoutMs;
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
  throw new Error('service not ready in time');
}

test.describe('Feature 21 — MNIST walkthrough (video recording)', () => {
  test.skip(!RECORDING_ENABLED, 'Enable with E2E_RECORD_MNIST_WALKTHROUGH=1 to record docs/e2e-mnist-run.mp4');

  // Single long-running test so the whole walkthrough is one
  // contiguous webm (worker-context boundaries would cut the video).
  test.describe.configure({ timeout: 360_000 });

  test('mnist pipeline — submission, live transitions, terminal state', async ({ authedPage: page }) => {
    const token = getRootToken();

    // 1. Baseline — dashboard home (/nodes from shared-page auth).
    //    Small pause so the viewer reads the header before we move.
    await page.waitForTimeout(PAUSE_MEDIUM);

    // 2. Navigate to the Pipelines list BEFORE submit. The viewer
    //    sees the empty list so the mnist-wf-1 row appearing after
    //    submission is the clear "something happened" moment.
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    await page.waitForTimeout(PAUSE_MEDIUM);

    // 3. Submit the workflow via REST. The Pipelines list fetches
    //    once on component init and doesn't auto-poll; clicking
    //    the same nav link is a no-op under Angular's default
    //    RouteReuseStrategy (same URL → no ngOnInit → no re-fetch).
    //    page.reload() would destroy the in-memory JWT (the auth
    //    fixture's comment on this is explicit). So we nudge the
    //    list to re-render by bouncing through a sibling ML route
    //    — Datasets → Pipelines destroys+recreates the Pipelines
    //    component, forcing a fresh fetch. On camera this reads
    //    as a natural "check something else, come back" beat.
    await submitMnistWorkflow(token);
    await page.waitForTimeout(PAUSE_SHORT);
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets$/);
    await page.waitForTimeout(PAUSE_SHORT);
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    const row = page.locator('tr').filter({ hasText: WF_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    // Pause on the row in non-terminal state so the viewer sees
    // the chip colour.
    await page.waitForTimeout(PAUSE_LONG);

    // 4. Click "View DAG" to drill into the detail view. Both the
    //    Pipelines list and the DAG detail component fetch on
    //    init and do NOT auto-poll (by design: these are
    //    snapshot views, not live streams — the dashboard pushes
    //    real-time updates through separate node/events surfaces).
    //    So to film transitions, we do what a real user would:
    //    drive the refresh manually by bouncing between the list
    //    and the DAG view. Each click re-navigates, which
    //    re-fetches the lineage + re-renders the cards with the
    //    latest per-job state.
    await row.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
    await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });
    await page.locator('.job-grid').scrollIntoViewIfNeeded();
    // The mermaid diagram renders async; give it a beat to appear
    // above the job grid so both surfaces are in frame.
    await page.waitForTimeout(PAUSE_LONG);

    // 5. Live-progress loop. While the workflow is still running,
    //    bounce list ↔ detail every ~10 s so the viewer watches
    //    cards flip pending → dispatching → running → completed
    //    across successive refreshes. The backend completion poll
    //    runs independently — as soon as it hits `completed`, we
    //    break out of the loop and do the final DAG render.
    const completionDeadline = Date.now() + 300_000;
    let completed = false;
    while (!completed && Date.now() < completionDeadline) {
      // Peek the backend without leaving the detail view. If it's
      // done we want the NEXT card render to be the all-green
      // snapshot, not a random intermediate.
      const wfRes = await fetch(`${API_URL}/workflows/${WF_ID}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (wfRes.ok) {
        const wf: WorkflowResp = await wfRes.json();
        if (wf.status === 'completed') {
          completed = true;
          break;
        }
        if (wf.status === 'failed' || wf.status === 'cancelled') {
          throw new Error(`workflow terminal ${wf.status}`);
        }
      }

      // User-natural refresh beat: back to the Pipelines list
      // (different route → component re-mount → fresh fetch,
      // shows the latest list-level status chip), then back into
      // the DAG detail (different route again → lineage refetched,
      // cards re-render with up-to-date per-job state). The auth
      // fixture's JWT is in-memory, so page.reload() would nuke
      // it; bouncing between SPA routes keeps the token.
      await page.click('a.nav-link >> text=Pipelines');
      await expect(page).toHaveURL(/\/ml\/pipelines$/);
      await expect(row).toBeVisible({ timeout: 10_000 });
      await page.waitForTimeout(PAUSE_MEDIUM);
      await row.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
      await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
      await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });
      await page.locator('.job-grid').scrollIntoViewIfNeeded();
      await page.waitForTimeout(PAUSE_LONG);
    }

    if (!completed) {
      // Defensive: if the inline loop timed out before observing
      // completion, run the long-wait helper so the rest of the
      // walkthrough has something to point at. Shouldn't fire in
      // practice on a warm cache.
      await waitWorkflowCompleted(token, 60_000);
    }

    // Final DAG render: bounce to the Pipelines list (now shows
    // the completed chip) then back into the DAG for the all-
    // green terminal-state shot.
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    await expect(row).toContainText(/completed/i, { timeout: 10_000 });
    await page.waitForTimeout(PAUSE_MEDIUM);
    await row.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
    await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });
    await page.locator('.job-grid').scrollIntoViewIfNeeded();
    await page.waitForTimeout(PAUSE_LONG);

    // 6. Back to the Pipelines list one final time so the closing
    //    shot of this section is the list row with the completed
    //    chip — mirrors the opening "row appears" moment.
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    await expect(row).toContainText(/completed/i, { timeout: 10_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // 7. Registry — Datasets shows mnist/v1.
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets$/);
    await expect(
      page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist' }),
    ).toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // 8. Models — hover a metric pill so the viewer notices the
    //    accuracy / f1_macro values. Lineage pill shows
    //    job:mnist-wf-1/train + dataset:mnist v1.
    await page.click('a.nav-link >> text=Models');
    await expect(page).toHaveURL(/\/ml\/models$/);
    const modelRow = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist-logreg' });
    await expect(modelRow).toBeVisible({ timeout: 10_000 });
    await modelRow.locator('.metric-pill').first().hover();
    await page.waitForTimeout(PAUSE_LONG);

    // 9. Services — submit the serve job (off-camera, REST) then
    //    wait for READY. The Services component fetches once on
    //    init, so after the prober flips the backend state we
    //    bounce through Datasets to re-mount the component and
    //    pick up the ready chip on screen.
    await page.click('a.nav-link >> text=Services');
    await expect(page).toHaveURL(/\/ml\/services$/);
    await page.waitForTimeout(PAUSE_SHORT);
    await submitServeAndWaitReady(token, 45_000);
    await page.click('a.nav-link >> text=Datasets');
    await expect(page).toHaveURL(/\/ml\/datasets$/);
    await page.waitForTimeout(PAUSE_SHORT);
    await page.click('a.nav-link >> text=Services');
    await expect(page).toHaveURL(/\/ml\/services$/);
    await expect(
      page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: SERVE_JOB_ID }),
    ).toBeVisible({ timeout: 15_000 });
    await expect(
      page.locator('table[mat-table] tr.mat-mdc-row')
        .filter({ hasText: SERVE_JOB_ID })
        .locator('.chip-ready'),
    ).toContainText(/READY/i, { timeout: 15_000 });
    // Closing beat so the final frame has the READY chip visible.
    await page.waitForTimeout(PAUSE_LONG);
  });
});
