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
// Optimised for CI time: no waitForTimeout pacing, no video, no
// DAG-builder click-throughs. The MNIST walkthrough is the
// human-eye version; this one is the fast regression gate.
//
// Coverage expanded in 2026-04-21 to pick up what MNIST now proves
// (features 40, 42, 43) so the same regressions don't ride past CI:
//   - Feature 40  — workflow_outcomes row populated in analytics
//                   (job_count / success_count / duration_ms /
//                   started_at) surfaced via /api/analytics/ml-runs.
//   - Feature 40b — workflow-level `tags` round-trip: the iris
//                   submission stamps {team, task, env}; the row
//                   in ml-runs echoes them verbatim.
//   - Feature 42  — parallel-dispatch overlap. A new `baseline`
//                   sibling depends only on preprocess (same as
//                   `train`), so dispatch fires them concurrently.
//                   The spec asserts their
//                   [dispatched_at, finished_at] intervals overlap
//                   on the wall clock, mirroring the MNIST
//                   train_light ‖ train_heavy assertion.
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
//                                      show ingest/preprocess/train/baseline/register
//                                      with completed status
//   3. /ml/datasets                  — iris/v1 row visible with s3:// URI
//   4. /ml/models                    — iris-logreg/v1 with lineage + metrics
//   5. /ml/services                  — iris-serve-1 ready with upstream URL
//   6. REST /api/analytics/ml-runs  — workflow_outcomes row with tags + timing
//   7. REST /jobs                    — per-job dispatched_at intervals overlap

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

// Feature 40 / 40b / 40c — workflow_outcomes rollup shape. Kept
// aligned with internal/api/handlers_analytics_unified.go#MLRunRow.
interface MLRunsResp {
  rows: Array<{
    workflow_id:    string;
    status:         string;
    job_count:      number;
    success_count:  number;
    failed_count:   number;
    started_at?:    string;
    duration_ms?:   number;
    tags?:          Record<string, string>;
  }>;
  total: number;
}

// Feature 42 — per-job run-interval fields on JobResponse. Both
// dispatched_at and finished_at are non-null for any job that
// reached a terminal state via a node pickup.
interface JobTimingRow {
  id:             string;
  status:         string;
  dispatched_at?: string;
  finished_at?:   string;
}
interface JobListResp { jobs: JobTimingRow[]; total: number }

// Tags the submission stamps. The analytics row must echo these
// exactly; the unit test under internal/api/handlers_workflows_test.go
// covers the per-entry validation separately.
const WF_TAGS: Record<string, string> = {
  team: 'ml',
  task: 'iris-classification',
  env:  'ci',
};

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
  // Feature 39 — the coordinator REST listener is TLS-on in the
  // E2E overlay. register.py and any future in-cluster HTTPS
  // caller pin the self-signed CA via HELION_CA_FILE (mirrors
  // examples/ml-mnist/register.py).
  const jobEnv = {
    HELION_API_URL: 'https://coordinator:8080',
    HELION_CA_FILE: '/app/state/ca.pem',
    HELION_TOKEN: token,
  };
  const body = {
    id: WF_ID,
    name: 'iris-end-to-end',
    priority: 60,
    // Feature 40b — stamped here and re-read from the ml-runs
    // analytics row at the tail of this spec. Kept aligned with
    // examples/ml-iris/workflow.yaml's `tags:` block so both entry
    // points (CLI submit.py + this E2E) produce the same row.
    tags: WF_TAGS,
    jobs: [
      {
        name: 'ingest',
        command: 'python',
        args: ['/app/ml-iris/ingest.py'],
        env: jobEnv,
        timeout_seconds: 60,
        node_selector: { runtime: 'go' },
        outputs: [{ name: 'RAW_CSV', local_path: 'raw.csv' }],
      },
      {
        name: 'preprocess',
        command: 'python',
        args: ['/app/ml-iris/preprocess.py'],
        env: jobEnv,
        timeout_seconds: 60,
        depends_on: ['ingest'],
        node_selector: { runtime: 'go' },
        inputs: [{ name: 'RAW_CSV', from: 'ingest.RAW_CSV', local_path: 'raw.csv' }],
        outputs: [
          { name: 'TRAIN_PARQUET', local_path: 'train.parquet' },
          { name: 'TEST_PARQUET',  local_path: 'test.parquet'  },
        ],
      },
      // ── Parallel fork (feature 42) ──────────────────────────
      // `train` and `baseline` both depend only on preprocess, so
      // the dispatcher fires them concurrently (goroutine-per-job
      // fix in internal/cluster/dispatch.go). Both jobs load the
      // same train/test parquets; `train` fits a real
      // LogisticRegression, `baseline` fits a stratified
      // DummyClassifier — the overlap is observable in their
      // [dispatched_at, finished_at] intervals even though both
      // complete in well under a second.
      //
      // runtime=go pins both to the Go-runtime Python nodes
      // (iris overlay also ships a Rust node for MNIST that
      // can't run iris's python-based jobs without PATH in env).
      {
        name: 'train',
        command: 'python',
        args: ['/app/ml-iris/train.py'],
        env: jobEnv,
        timeout_seconds: 120,
        depends_on: ['preprocess'],
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
        name: 'baseline',
        command: 'python',
        args: ['/app/ml-iris/baseline.py'],
        env: jobEnv,
        timeout_seconds: 60,
        depends_on: ['preprocess'],
        node_selector: { runtime: 'go' },
        inputs: [
          { name: 'TRAIN_PARQUET', from: 'preprocess.TRAIN_PARQUET', local_path: 'train.parquet' },
          { name: 'TEST_PARQUET',  from: 'preprocess.TEST_PARQUET',  local_path: 'test.parquet'  },
        ],
        outputs: [
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
        // Register still depends only on train — baseline is a
        // diagnostic sibling, not part of the registered-model
        // lineage. The REST harness (scripts/run-iris-e2e.sh)
        // still finds iris-logreg/v1 with source_job_id from the
        // `train` job.
        depends_on: ['train'],
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
    for (const jobName of ['ingest', 'preprocess', 'train', 'baseline', 'register']) {
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

  // ── Feature 40 / 40b / 40c — workflow analytics ──────────────

  test('ml-runs analytics row carries job counts, timing and tags', async () => {
    // The analytics sink flushes every 200 ms; workflow.completed
    // fires once compare/register terminate. Short poll keeps the
    // test resilient to the flush cadence without baking in a
    // waitForTimeout.
    const token = getRootToken();
    let row: MLRunsResp['rows'][number] | undefined;
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline && !row) {
      const r = await fetch(`${API_URL}/api/analytics/ml-runs?limit=20`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (r.ok) {
        const b: MLRunsResp = await r.json();
        row = b.rows.find(x => x.workflow_id === WF_ID);
      }
      if (!row) await new Promise(x => setTimeout(x, 500));
    }
    expect(row, 'iris-wf-1 missing from /api/analytics/ml-runs').toBeDefined();
    expect(row!.status).toBe('completed');
    // 5 jobs: ingest, preprocess, train, baseline, register.
    expect(row!.job_count).toBe(5);
    expect(row!.success_count).toBe(5);
    expect(row!.failed_count).toBe(0);

    // Feature 40c — timing populated.
    expect(row!.started_at, 'started_at missing').toBeDefined();
    expect(row!.duration_ms, 'duration_ms missing').toBeDefined();
    expect(row!.duration_ms!).toBeGreaterThan(0);

    // Feature 40b — tag round-trip. Every key submitted must land
    // on the row with the same value; the endpoint MAY add
    // system.* keys in the future, so we do subset-match instead
    // of equality.
    expect(row!.tags, 'tags missing on ml-runs row').toBeDefined();
    for (const [k, v] of Object.entries(WF_TAGS)) {
      expect(row!.tags![k], `tag ${k} lost round-trip`).toBe(v);
    }
  });

  // ── Feature 42 — parallel-dispatch overlap ──────────────────

  test('train and baseline dispatch windows overlap on the wall clock', async () => {
    // Intervals [a,b] and [c,d] overlap iff a < d && c < b. Both
    // jobs depend only on preprocess, so a regressed serialised
    // dispatcher (pre-goroutine-per-job) would leave a gap — the
    // MNIST walkthrough saw a 0.77ms miss before the fix landed.
    const token = getRootToken();
    const r = await fetch(`${API_URL}/jobs?size=100&status=COMPLETED`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(r.ok).toBeTruthy();
    const body: JobListResp = await r.json();
    const train    = body.jobs.find(j => j.id === `${WF_ID}/train`);
    const baseline = body.jobs.find(j => j.id === `${WF_ID}/baseline`);
    expect(train, `${WF_ID}/train missing`).toBeDefined();
    expect(baseline, `${WF_ID}/baseline missing`).toBeDefined();
    expect(train!.dispatched_at, 'train.dispatched_at missing').toBeDefined();
    expect(train!.finished_at,   'train.finished_at missing').toBeDefined();
    expect(baseline!.dispatched_at, 'baseline.dispatched_at missing').toBeDefined();
    expect(baseline!.finished_at,   'baseline.finished_at missing').toBeDefined();

    const tStart = Date.parse(train!.dispatched_at!);
    const tEnd   = Date.parse(train!.finished_at!);
    const bStart = Date.parse(baseline!.dispatched_at!);
    const bEnd   = Date.parse(baseline!.finished_at!);

    expect(
      tStart,
      `train dispatched at ${train!.dispatched_at} but baseline finished before (${baseline!.finished_at}) — intervals do not overlap`,
    ).toBeLessThan(bEnd);
    expect(
      bStart,
      `baseline dispatched at ${baseline!.dispatched_at} but train finished before (${train!.finished_at}) — intervals do not overlap`,
    ).toBeLessThan(tEnd);
  });
});
