// e2e/specs/ml-mnist-walkthrough.spec.ts
//
// Paced MNIST pipeline walkthrough for the docs video
// (docs/e2e-mnist-run.mp4). Unlike ml-iris-walkthrough.spec.ts —
// which pre-completes the workflow in beforeAll and only films
// terminal-state navigation — this spec submits the workflow
// *inside* the recorded test so the viewer sees the full
// lifecycle: empty list → row appears in pending → DAG cards
// transition pending → dispatching → running → completed → Jobs
// tab showing the same four rows → registry → services → READY.
//
// MNIST is the right demo for this shape because the workflow
// takes ~60 s end-to-end (iris finishes in ~10 s, which is too
// fast to film a transition), and the 784-feature LogisticRegression
// training is the observable "running" phase.
//
// The walkthrough depends on live polling in ml-pipelines.component
// and ml-pipeline-detail.component: without it, the list row and
// DAG cards would be static snapshots and the spec would need a
// route-bounce hack to force re-fetches. With polling, we just
// park on each page and let the camera roll.
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
  const jobEnv = {
    HELION_API_URL: 'http://coordinator:8080',
    HELION_TOKEN: token,
    // The Rust subprocess runtime calls `env_clear()` before spawn
    // (see runtime-rust/src/executor.rs), so PATH is not inherited
    // from the node agent's process env. Jobs invoking `python`
    // rely on PATH to resolve the binary — declare it explicitly
    // so the same workflow works whether the scheduler picks a
    // Go-runtime or a Rust-runtime node. Both node images ship
    // python at /usr/local/bin.
    PATH: '/usr/local/bin:/usr/bin:/bin',
  };
  const body = {
    id: WF_ID, name: 'mnist-end-to-end', priority: 50,
    jobs: [
      // Feature 21 heterogeneous-scheduling demo: pin the lightweight
      // orchestration steps to Go-runtime nodes and route the heavy
      // sklearn training step to the Rust-runtime node via the
      // `runtime` label. The dashboard's DAG job cards surface the
      // node_id on each card so the viewer can see the split.
      {
        name: 'ingest',
        command: 'python', args: ['/app/ml-mnist/ingest.py'],
        env: jobEnv, timeout_seconds: 180,
        node_selector: { runtime: 'go' },
        outputs: [{ name: 'RAW_CSV', local_path: 'raw.csv' }],
      },
      {
        name: 'preprocess',
        command: 'python', args: ['/app/ml-mnist/preprocess.py'],
        env: jobEnv, timeout_seconds: 60, depends_on: ['ingest'],
        node_selector: { runtime: 'go' },
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
        node_selector: { runtime: 'rust' },
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
        node_selector: { runtime: 'go' },
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

    // 3. Submit the workflow via REST. Both the Pipelines list
    //    and the DAG detail component now poll the coordinator on
    //    the same cadence as the Nodes list (environment.tokenRefreshMs
    //    — 5 s dev / 10 s prod), so the new row and its chip
    //    transitions just appear in-place as the backend advances.
    //    No route-bounce hack needed.
    await submitMnistWorkflow(token);
    const row = page.locator('tr').filter({ hasText: WF_ID });
    // The list's startWith(0) tick fires on mount, and the next
    // poll fires within tokenRefreshMs — so the row shows up
    // either immediately (we were already on the page) or after
    // one tick. Give it the tick's worth of headroom plus slack.
    await expect(row).toBeVisible({ timeout: 15_000 });
    // Linger so the viewer sees the row with its transitional chip
    // colour and the JOBS column ticking up (0/4 → 1/4 → …).
    await page.waitForTimeout(PAUSE_LONG);

    // 4. Click "View DAG" to drill into the detail view. The
    //    detail component polls getWorkflowLineage and re-renders
    //    the mermaid graph only when the per-job status signature
    //    changes, so staying on this page is now sufficient — the
    //    four job cards flip pending → dispatching → running →
    //    completed in place while the camera rolls.
    await row.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
    await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });
    await page.locator('.job-grid').scrollIntoViewIfNeeded();
    // Mermaid renders async after lineage arrives.
    await page.waitForTimeout(PAUSE_MEDIUM);

    // 5. Park on the DAG view for the rest of the workflow's run.
    //    The in-component polling drives the card transitions, and
    //    waitForWorkflowCompleted polls the REST API in parallel
    //    so we know exactly when to advance to the next scene.
    //    No navigation during this window — the transition sequence
    //    (pending → dispatching → running → completed for each of
    //    the four jobs) is the whole point of this beat.
    await waitWorkflowCompleted(token, 300_000);

    // Linger on the all-green terminal DAG so the viewer sees the
    // final state clearly before we leave.
    await page.waitForTimeout(PAUSE_LONG);

    // 6. Back to the Pipelines list for the closing beat: row chip
    //    now shows completed and JOBS is 4/4. Mirrors the opening
    //    "row appears" moment at its terminal state.
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    await expect(row).toContainText(/completed/i, { timeout: 10_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // 6b. Jobs — detour through the core Jobs list so the viewer
    //     sees the same workflow from a different angle: each of
    //     the four DAG jobs appears as its own row with status
    //     badge + command. Reinforces that an ML pipeline isn't a
    //     special construct — it's just four ordinary Helion jobs
    //     linked by `from:` references, which is why they show up
    //     in the non-ML Jobs view too.
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs$/);
    // The four workflow jobs are identified as `<wf-id>/<name>`.
    // Assert all four rendered so the viewer has something to
    // look at before the pause.
    for (const jobName of ['ingest', 'preprocess', 'train', 'register']) {
      await expect(
        page.locator('tr').filter({ hasText: `${WF_ID}/${jobName}` }),
      ).toBeVisible({ timeout: 10_000 });
    }
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
    await page.waitForTimeout(PAUSE_LONG);

    // 10. Analytics — prove the pipeline also ran through the
    //     cross-cutting observability stack (events → analytics
    //     sink → PostgreSQL → Analytics dashboard). The MNIST
    //     workflow takes ~60 s end-to-end, so the default 7-day
    //     window would bury the just-completed run in noise.
    //     Click the "Last 10 min" quick-range button — it sends
    //     ISO-instant boundaries to the backend (not day-truncated
    //     dates), so the chart zooms to the window containing our
    //     four just-completed jobs. Makes the "we ran, and it's
    //     already visible to ops" beat land immediately.
    await page.click('a.nav-link >> text=Analytics');
    await expect(page).toHaveURL(/\/analytics$/);
    const tenMinBtn = page.locator('button.quick-range').filter({ hasText: /LAST\s*10\s*MIN/ });
    await expect(tenMinBtn).toBeVisible({ timeout: 10_000 });
    await tenMinBtn.click();
    // Throughput chart is the headline panel — wait for the canvas
    // (ng2-charts renders into a <canvas>) before the linger pause
    // so the final frame actually shows a chart rather than a
    // loading spinner.
    await expect(page.locator('canvas').first()).toBeVisible({ timeout: 15_000 });
    await page.waitForTimeout(PAUSE_LONG);
  });
});
