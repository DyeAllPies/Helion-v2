// e2e/specs/ml-mnist-parallel-walkthrough.spec.ts
//
// Feature 40 — paced "parallel heterogeneous" MNIST pipeline
// walkthrough for the docs video. The sibling of
// ml-mnist-walkthrough.spec.ts, but with:
//
//   1. An OPENING beat in the DAG builder UI so the viewer
//      sees the visual submission surface (not just a bare
//      REST POST).
//   2. The MIDDLE beat parked on /nodes and /jobs so the viewer
//      can see the dispatch split across runtimes in flight.
//      The two training jobs run concurrently; the Jobs table
//      shows one on a Go node and one on a Rust node SIMUL-
//      taneously. That's the orchestration money shot.
//   3. The CLOSING beat on /analytics asserting the
//      workflow_outcomes row landed in PostgreSQL — feature-40
//      denormalised rollup proves the analytics layer picked
//      up the run.
//
// The pipeline shape (see examples/ml-mnist/workflow.yaml):
//
//   ingest   (Go)  ──┐
//                    ├→ preprocess (Go) ──┬→ train_light (Go)  ──┐
//                                         │                       ├→ compare (Go)
//                                         └→ train_heavy (Rust) ──┘
//
// Total wall-clock: ~70–120 s on a warm OpenML cache. The two
// train_* jobs run in parallel so the heavy-variant's 20–30 s
// LogisticRegression fit does NOT stack on top of the light-
// variant's 5–8 s fit — the DAG is a roughly T-shaped
// critical path (preprocess → max(light, heavy) → compare).
//
// Skipped by default. Enable with E2E_RECORD_MNIST_PARALLEL=1
// plus E2E_VIDEO=1 to record. Prerequisites identical to the
// non-parallel walkthrough — see ml-mnist-walkthrough.spec.ts
// header for the full up-front comment.

import { test, expect } from '../fixtures/auth.fixture';
import { API_URL, getRootToken } from '../fixtures/cluster.fixture';

const RECORDING_ENABLED = process.env['E2E_RECORD_MNIST_PARALLEL'] === '1';

const WF_ID = 'mnist-parallel-wf-1';

// Pacing — match the non-parallel walkthrough's rhythm so a
// stitched video cut between the two reads as one consistent
// piece.
const PAUSE_SHORT  = 1_200;
const PAUSE_MEDIUM = 2_500;
const PAUSE_LONG   = 4_000;

interface WorkflowResp { status: string }
interface MLRunsResp {
  rows: Array<{
    workflow_id:    string;
    status:         string;
    job_count:      number;
    success_count:  number;
    failed_count:   number;
  }>;
  total: number;
}

/**
 * Submit the parallel MNIST workflow via REST. We detour through
 * the DAG builder UI for the narrative beat, but the final
 * submission goes through REST so the test is deterministic —
 * filling 5 jobs through DAG-builder clicks would take 40+ s and
 * introduce flake on form validation edge cases. The REST path
 * is the same one ApiService uses when the DAG builder's Submit
 * button fires.
 */
async function submitParallelMnistWorkflow(token: string): Promise<void> {
  const jobEnv = {
    HELION_API_URL: 'http://coordinator:8080',
    HELION_TOKEN:   token,
    // Rust runtime env_clear()s before spawn; python resolves
    // through PATH on both node images. See
    // ml-mnist-walkthrough.spec.ts header for the full rationale.
    PATH: '/usr/local/bin:/usr/bin:/bin',
  };

  const body = {
    id: WF_ID,
    name: 'mnist-parallel-heterogeneous',
    priority: 50,
    jobs: [
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
        inputs: [{ name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' }],
        outputs: [
          { name: 'TRAIN_PARQUET', local_path: 'train.parquet' },
          { name: 'TEST_PARQUET',  local_path: 'test.parquet'  },
        ],
      },
      // ── Parallel fork: both train_* depend only on preprocess ──
      {
        name: 'train_light',
        command: 'python', args: ['/app/ml-mnist/train.py'],
        env: { ...jobEnv, HELION_TRAIN_MAX_ITER: '50', HELION_TRAIN_VARIANT: 'light' },
        timeout_seconds: 120, depends_on: ['preprocess'],
        node_selector: { runtime: 'go' },
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
        name: 'train_heavy',
        command: 'python', args: ['/app/ml-mnist/train.py'],
        env: { ...jobEnv, HELION_TRAIN_MAX_ITER: '400', HELION_TRAIN_VARIANT: 'heavy' },
        timeout_seconds: 300, depends_on: ['preprocess'],
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
        name: 'compare',
        command: 'python', args: ['/app/ml-mnist/compare.py'],
        env: {
          ...jobEnv,
          HELION_WORKFLOW_ID:           WF_ID,
          HELION_TRAIN_LIGHT_JOB_NAME:  'train_light',
          HELION_TRAIN_HEAVY_JOB_NAME:  'train_heavy',
        },
        timeout_seconds: 60,
        depends_on: ['train_light', 'train_heavy'],
        node_selector: { runtime: 'go' },
        inputs: [
          { name: 'RAW_CSV',       from: 'ingest.RAW_CSV',             local_path: 'raw.csv' },
          { name: 'MODEL_LIGHT',   from: 'train_light.MODEL',          local_path: 'model_light.joblib' },
          { name: 'MODEL_HEAVY',   from: 'train_heavy.MODEL',          local_path: 'model_heavy.joblib' },
          { name: 'METRICS_LIGHT', from: 'train_light.METRICS',        local_path: 'metrics_light.json' },
          { name: 'METRICS_HEAVY', from: 'train_heavy.METRICS',        local_path: 'metrics_heavy.json' },
        ],
        outputs: [
          { name: 'COMPARISON', local_path: 'comparison.json' },
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
  if (!res.ok) {
    throw new Error(`workflow submit ${res.status}: ${await res.text()}`);
  }
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

test.describe('Feature 40 — MNIST parallel-heterogeneous walkthrough (video)', () => {
  test.skip(
    !RECORDING_ENABLED,
    'Enable with E2E_RECORD_MNIST_PARALLEL=1 to record docs/e2e-mnist-parallel-run.mp4',
  );

  test.describe.configure({ timeout: 360_000 });

  test('parallel pipeline — DAG builder opener → live dispatch → analytics row', async ({ authedPage: page }) => {
    const token = getRootToken();

    // ──────────────────────────────────────────────────────────
    // 1. OPENING beat: DAG builder.
    //
    // The viewer arrives on /submit — a form-based builder with
    // "+ add job" on the left pane, a per-job editor on the
    // right, and the live JSON preview at the bottom. We
    // demonstrate the mechanic (click "+ add job" → fill one
    // field) so the audience knows this UI exists, then cut to
    // the REST submission for deterministic pacing.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Submit');
    await expect(page).toHaveURL(/\/submit$/);
    await expect(page.locator('h1').filter({ hasText: /DAG builder|SUBMIT|workflow/i }).first())
      .toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // Click "+ add job" twice so the audience sees the list
    // grow. Doesn't fill or submit — the REST call below is
    // the source of truth. The button text is "+ add job"
    // per submit-dag-builder.component.ts line 65.
    const addJobBtn = page.locator('button.btn-ghost').filter({ hasText: /\+\s*add\s*job/i });
    await expect(addJobBtn).toBeVisible({ timeout: 5_000 });
    await addJobBtn.click();
    await page.waitForTimeout(PAUSE_SHORT);
    await addJobBtn.click();
    await page.waitForTimeout(PAUSE_SHORT);

    // Type into the first job's name so the viewer sees the
    // live JSON preview update at the bottom of the page.
    const firstNameInput = page.locator('input[formControlName="name"]').first();
    await firstNameInput.fill('ingest');
    await page.waitForTimeout(PAUSE_MEDIUM);

    // ──────────────────────────────────────────────────────────
    // 2. Navigate to Pipelines and submit the real workflow via
    //    REST. 5-job DAG via click-by-click fill would take 40+ s
    //    and risk flake; the REST path is the same wire that the
    //    DAG builder's Submit button would hit. The viewer's
    //    mental model: "we typed the first job, then loaded the
    //    rest and submitted."
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    await page.waitForTimeout(PAUSE_SHORT);

    await submitParallelMnistWorkflow(token);
    const row = page.locator('tr').filter({ hasText: WF_ID });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // ──────────────────────────────────────────────────────────
    // 3. Nodes page — both nodes healthy with the runtime labels
    //    visible. This is the "two nodes, two runtimes" beat
    //    that makes the parallel-dispatch story land.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Nodes');
    await expect(page).toHaveURL(/\/nodes$/);
    // Give the list a tick to populate. Both registered nodes
    // (e2e-node-1 Go runtime, e2e-node-2 Rust runtime) must be
    // visible — if only one is, the demo's parallel split
    // collapses to serial.
    await expect(
      page.locator('table[mat-table] tr.mat-mdc-row'),
    ).toHaveCount(2, { timeout: 15_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // ──────────────────────────────────────────────────────────
    // 4. Jobs page — watch the heterogeneous dispatch unfold.
    //    train_light + train_heavy land at the same time on
    //    different node_ids. The Jobs table has a NODE column
    //    and a RUNTIME column (see job-list.component.ts) so
    //    the split is visible at a glance.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Jobs');
    await expect(page).toHaveURL(/\/jobs$/);

    // Wait for all 5 job rows to appear. Job ID format is
    // `<wf_id>/<job_name>`.
    for (const jobName of ['ingest', 'preprocess', 'train_light', 'train_heavy', 'compare']) {
      await expect(
        page.locator('tr').filter({ hasText: `${WF_ID}/${jobName}` }),
      ).toBeVisible({ timeout: 90_000 });
    }
    // Park for the LIVE CAMERA BEAT: let the viewer watch the
    // status chips transition. train_light on Go finishes first;
    // train_heavy keeps running on Rust. The NODE column gives
    // the viewer two different node_ids side-by-side for the
    // same workflow — that's the parallel-heterogeneous
    // orchestration money shot.
    await page.waitForTimeout(PAUSE_LONG * 2);

    // ──────────────────────────────────────────────────────────
    // 5. Drill into the DAG detail view so the viewer sees the
    //    mermaid chart with the forked shape (two train nodes
    //    feeding a single compare node). Polling re-renders the
    //    chart as jobs transition — no bounce hack needed.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Pipelines');
    await expect(page).toHaveURL(/\/ml\/pipelines$/);
    await row.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`));
    await expect(page.locator('.dag-panel__header')).toBeVisible({ timeout: 10_000 });
    await page.locator('.job-grid').scrollIntoViewIfNeeded();
    await page.waitForTimeout(PAUSE_MEDIUM);

    // Park on the DAG until completion. Individual job cards
    // transition pending → dispatching → running → completed;
    // the two train variants show their variant label via the
    // node_id on each card.
    await waitWorkflowCompleted(token, 300_000);
    await page.waitForTimeout(PAUSE_LONG);

    // ──────────────────────────────────────────────────────────
    // 6. Models page — BOTH models registered, with the
    //    `winner=true|false` tag from compare.py visible as a
    //    metric pill. One model carries the winner badge, the
    //    other the runner-up. The metric-pill hover shows the
    //    accuracy that drove the decision.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Models');
    await expect(page).toHaveURL(/\/ml\/models$/);
    const lightRow = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist-logreg-light' });
    const heavyRow = page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist-logreg-heavy' });
    await expect(lightRow).toBeVisible({ timeout: 15_000 });
    await expect(heavyRow).toBeVisible({ timeout: 15_000 });
    await heavyRow.locator('.metric-pill').first().hover();
    await page.waitForTimeout(PAUSE_LONG);

    // ──────────────────────────────────────────────────────────
    // 7. Analytics page + REST verification of feature-40
    //    workflow_outcomes. We don't yet ship a dedicated ML-runs
    //    panel — the denormalised rollup landed as infrastructure
    //    in this feature, the UI panel is a planned follow-up.
    //    For the camera, we visit /analytics (showing the sink
    //    is live), then cut to a REST assertion on
    //    GET /api/analytics/ml-runs that proves our workflow_id
    //    made it into the denormalised table with the correct
    //    counts.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Analytics');
    await expect(page).toHaveURL(/\/analytics$/);
    const tenMinBtn = page.locator('button.quick-range').filter({ hasText: /LAST\s*10\s*MIN/ });
    await expect(tenMinBtn).toBeVisible({ timeout: 10_000 });
    await tenMinBtn.click();
    await expect(page.locator('canvas').first()).toBeVisible({ timeout: 15_000 });
    await page.waitForTimeout(PAUSE_LONG);

    // Feature 40 verification — the workflow_outcomes table
    // carries our run. Poll briefly because the sink flushes
    // every 200 ms but the workflow.completed event only fires
    // AFTER the compare job terminates.
    let found: MLRunsResp['rows'][number] | null = null;
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline && !found) {
      const r = await fetch(`${API_URL}/api/analytics/ml-runs?limit=20`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (r.ok) {
        const b: MLRunsResp = await r.json();
        found = b.rows.find(row => row.workflow_id === WF_ID) ?? null;
      }
      if (!found) await new Promise(r => setTimeout(r, 500));
    }
    expect(found).not.toBeNull();
    expect(found!.status).toBe('completed');
    expect(found!.job_count).toBe(5);
    expect(found!.success_count).toBe(5);
    expect(found!.failed_count).toBe(0);
  });
});
