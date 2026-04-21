// e2e/specs/ml-mnist-parallel-walkthrough.spec.ts
//
// Feature 40 + 41 + 42 + 43 — paced "parallel heterogeneous"
// MNIST pipeline walkthrough for the docs video. The E2E flow
// now honestly matches the narrative in the docs:
//
//   1. OPEN in the DAG builder with a pre-hydrated draft
//      (feature 41 sessionStorage). Viewer sees the 5-job
//      workflow already laid out in the form — the typing is
//      compressed for pacing, but the hydration path is the
//      real one (WorkflowDraftService.load() fires on ngOnInit).
//   2. Navigate to the Submit tab. The feature-41 Resume-draft
//      card renders above the tab body. Viewer clicks SUBMIT
//      NOW — the component calls ApiService.submitWorkflow()
//      end-to-end. No fetch() shortcut, no REST detour.
//   3. MIDDLE beat on /nodes + /jobs shows the dispatch split
//      across runtimes in flight. train_light and train_heavy
//      land on different nodes at the same moment; feature 42
//      locks that down with a per-job dispatched_at overlap
//      assertion at the end of the run.
//   4. CLOSING beat on /analytics asserting the
//      workflow_outcomes row landed in PostgreSQL — feature-40
//      denormalised rollup proves the analytics layer picked
//      up the run — and the feature-42 per-job timing check
//      runs on GET /jobs right after.
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

// Feature 42 — per-job run-interval shape. dispatched_at and
// finished_at ride the JobResponse as RFC-3339 strings; both are
// present for any job that reached a terminal state after being
// picked up by a node. Missing for jobs that never dispatched
// (unschedulable, pre-pickup cancelled) — the overlap assertion
// tolerates that by filtering to the two train_* IDs.
interface JobTimingRow {
  id:             string;
  status:         string;
  dispatched_at?: string;
  finished_at?:   string;
}
interface JobListResp { jobs: JobTimingRow[]; total: number }

// Feature 41 — match what the dashboard's `WorkflowDraftService`
// writes under its own storage key. Kept in sync with the const
// declared in workflow-draft.service.ts.
const DRAFT_STORAGE_KEY = 'helion.workflow-draft.v1';

/**
 * Build the full 5-job MNIST workflow body. Used by the draft-seed
 * helper below; this is the exact SubmitWorkflowRequest the DAG
 * builder's Submit button POSTs to /workflows when the operator
 * clicks through the UI.
 */
function buildMnistWorkflowBody(token: string): {
  id: string; name: string; priority: number; jobs: unknown[];
} {
  const jobEnv = {
    // Feature 39 — coordinator REST is TLS-on; compare.py /
    // register.py trust the CA pinned via HELION_CA_FILE (see
    // ssl._ssl_context() in each). No system trust store, no
    // insecure skip.
    HELION_API_URL: 'https://coordinator:8080',
    HELION_CA_FILE: '/app/state/ca.pem',
    HELION_TOKEN:   token,
    // Rust runtime env_clear()s before spawn; python resolves
    // through PATH on both node images.
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

  return body;
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

  test('parallel pipeline — DAG builder build → Submit-tab click → live dispatch → analytics row', async ({ authedPage: page }) => {
    const token = getRootToken();
    const wfBody = buildMnistWorkflowBody(token);

    // ──────────────────────────────────────────────────────────
    // 1. Feature 41 — pre-seed a draft in sessionStorage so the
    //    DAG builder's ngOnInit hydrates the form through the
    //    real WorkflowDraftService.load() path. This is NOT a
    //    REST shortcut: when the operator lands on the builder
    //    they see the 5 jobs populated through the same code
    //    path that a 40-second keystroke-by-keystroke fill
    //    would produce. The only thing the seed bypasses is
    //    typing; hydration, form validation, Preview, and
    //    Submit all run for real.
    //
    //    addInitScript fires before every document load, so when
    //    the page navigates to /submit/dag-builder (below) the
    //    draft is already in sessionStorage.
    // ──────────────────────────────────────────────────────────
    await page.addInitScript((seed: { key: string; payload: string }) => {
      // Guard: don't overwrite an unrelated entry. In practice
      // sessionStorage is empty here, but this keeps the seed
      // explicit.
      window.sessionStorage.setItem(seed.key, seed.payload);
    }, {
      key: DRAFT_STORAGE_KEY,
      payload: JSON.stringify({
        schema: 'v1',
        savedAt: new Date().toISOString(),
        body: wfBody,
      }),
    });

    // ──────────────────────────────────────────────────────────
    // 2. OPENING beat: DAG builder. The viewer sees the 5-job
    //    workflow already laid out in the list pane + the live
    //    JSON preview at the bottom mirrors it. Nothing got typed
    //    on camera; everything they see came from the hydration.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Submit');
    await expect(page).toHaveURL(/\/submit\//, { timeout: 10_000 });

    // Navigate to the DAG builder sub-tab. Copy ("DAG builder")
    // is stable; the route path is even more stable.
    const dagTab = page.locator('a').filter({ hasText: /DAG\s*builder/i }).first();
    if (await dagTab.isVisible().catch(() => false)) {
      await dagTab.click();
    } else {
      await page.goto('/submit/dag-builder');
    }
    await expect(page).toHaveURL(/\/submit\/dag-builder$/, { timeout: 10_000 });

    // Form hydration finished — the jobs pane carries one row per
    // job in the seeded draft.
    for (const jobName of ['ingest', 'preprocess', 'train_light', 'train_heavy', 'compare']) {
      await expect(
        page.locator('.job-item').filter({ hasText: jobName }),
      ).toBeVisible({ timeout: 5_000 });
    }
    await page.waitForTimeout(PAUSE_LONG);

    // ──────────────────────────────────────────────────────────
    // 3. Navigate to the Submit landing tab. The feature-41
    //    Resume-draft card renders above the tab body; viewer
    //    clicks SUBMIT NOW. This fires
    //    SubmitShellComponent.onSubmitDraft() which calls
    //    ApiService.submitWorkflow(body) end-to-end. No fetch()
    //    detour; the spec now honestly drives the UI.
    // ──────────────────────────────────────────────────────────
    const jobTab = page.locator('a.submit-tab').filter({ hasText: /^\s*JOB\s*$/i }).first();
    if (await jobTab.isVisible().catch(() => false)) {
      await jobTab.click();
    } else {
      await page.goto('/submit/job');
    }
    await expect(page).toHaveURL(/\/submit\/job$/, { timeout: 10_000 });

    // Resume-draft card visible; SUBMIT NOW is the button that
    // takes our workflow to the coordinator.
    const resumeCard = page.locator('[data-testid="resume-draft-card"]');
    await expect(resumeCard).toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(PAUSE_MEDIUM);
    await resumeCard.locator('[data-testid="resume-draft-submit"]').click();

    // After submit, the component navigates to
    // /ml/pipelines/{id}. Wait for that redirect before the
    // middle beat kicks off.
    await expect(page).toHaveURL(new RegExp(`/ml/pipelines/${WF_ID}$`), {
      timeout: 15_000,
    });
    await page.waitForTimeout(PAUSE_LONG);

    // ──────────────────────────────────────────────────────────
    // 3. Nodes page — both nodes healthy with the runtime labels
    //    visible. This is the "two nodes, two runtimes" beat
    //    that makes the parallel-dispatch story land.
    // ──────────────────────────────────────────────────────────
    await page.click('a.nav-link >> text=Nodes');
    await expect(page).toHaveURL(/\/nodes$/);
    // Give the list a tick to populate. The iris-overlay brings
    // up THREE nodes — two Go-runtime Python nodes (node1 =
    // e2e-node-1, node2 = iris-node-2) and one Rust-runtime
    // Python node (mnist-node-rust). The parallel walkthrough
    // lands the Go jobs on either of the Go nodes (round-robin)
    // and the heavy-training job specifically on the Rust node.
    // If fewer than three rows, the demo's parallel split risks
    // collapsing; ≥ 3 keeps the compare step meaningful.
    await expect(
      page.locator('table[mat-table] tr.mat-mdc-row'),
    ).toHaveCount(3, { timeout: 15_000 });
    // Additional narrative beat: assert the rust-runtime node is
    // actually present by ID. Helps the viewer correlate the
    // Jobs page's node_id column against the Nodes page.
    await expect(
      page.locator('table[mat-table] tr.mat-mdc-row').filter({ hasText: 'mnist-node-rust' }),
    ).toBeVisible();
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
    const pipelineRow = page.locator('tr').filter({ hasText: WF_ID });
    await expect(pipelineRow).toBeVisible({ timeout: 15_000 });
    await pipelineRow.locator(`a[aria-label="View DAG for ${WF_ID}"]`).click();
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

    // ──────────────────────────────────────────────────────────
    // 8. Feature 42 — per-job overlap assertion. The
    //    workflow_outcomes row above proves the run completed;
    //    what it does NOT prove is that train_light and
    //    train_heavy actually ran concurrently. A silent
    //    regression — selector collapse, slot-1 node, dispatcher
    //    serialisation — would leave the DAG structurally
    //    parallel but behaviourally serial.
    //
    //    Intervals [a,b] and [c,d] overlap iff a < d && c < b.
    //    We're strict: no tolerance — both train_* timestamps
    //    are coordinator-side clock, so they're directly
    //    comparable, and the feature-43 20 k-row heavy fit gives
    //    the intervals ample margin. A single-ms overlap is still
    //    a pass, but in practice we expect many seconds.
    // ──────────────────────────────────────────────────────────
    const jobsResp = await fetch(
      `${API_URL}/jobs?size=100&status=COMPLETED`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(jobsResp.ok).toBeTruthy();
    const jobsBody: JobListResp = await jobsResp.json();
    const trainLight = jobsBody.jobs.find(j => j.id === `${WF_ID}/train_light`);
    const trainHeavy = jobsBody.jobs.find(j => j.id === `${WF_ID}/train_heavy`);
    expect(trainLight, 'train_light job missing from /jobs response').toBeDefined();
    expect(trainHeavy, 'train_heavy job missing from /jobs response').toBeDefined();
    expect(trainLight!.dispatched_at, 'train_light has no dispatched_at').toBeDefined();
    expect(trainLight!.finished_at,   'train_light has no finished_at').toBeDefined();
    expect(trainHeavy!.dispatched_at, 'train_heavy has no dispatched_at').toBeDefined();
    expect(trainHeavy!.finished_at,   'train_heavy has no finished_at').toBeDefined();

    const lightStart  = Date.parse(trainLight!.dispatched_at!);
    const lightFinish = Date.parse(trainLight!.finished_at!);
    const heavyStart  = Date.parse(trainHeavy!.dispatched_at!);
    const heavyFinish = Date.parse(trainHeavy!.finished_at!);

    // Strict overlap check.
    expect(
      lightStart,
      `train_light dispatched at ${trainLight!.dispatched_at} but train_heavy finished before that (${trainHeavy!.finished_at}) — intervals do not overlap`,
    ).toBeLessThan(heavyFinish);
    expect(
      heavyStart,
      `train_heavy dispatched at ${trainHeavy!.dispatched_at} but train_light finished before that (${trainLight!.finished_at}) — intervals do not overlap`,
    ).toBeLessThan(lightFinish);
  });
});
