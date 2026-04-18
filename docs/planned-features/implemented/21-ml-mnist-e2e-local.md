# Feature: ML — MNIST Local E2E (progression-observing)

**Priority:** P2
**Status:** Done. All six acceptance criteria met; full spec runs
~75 s on a warm cluster (six tests green). Shipped alongside two
incidental bug fixes surfaced by the longer workload: the
coordinator's Dispatch RPC had a hardcoded 10 s ceiling that
cancelled any batch job running longer (masked by iris being fast
enough to squeak under) and `examples/ml-mnist/train.py` hit a
sklearn 1.5 API removal (`multi_class` kwarg).
**Affected files:**
`examples/ml-mnist/` (new — seven files: scripts, workflow,
submitter, requirements, README),
`Dockerfile.node-python` (bake MNIST scripts alongside iris),
`dashboard/e2e/specs/ml-mnist-live.spec.ts` (new — gated
Playwright spec, local-only),
`internal/cluster/node_dispatcher.go` (dispatch RPC timeout now
derived from `job.TimeoutSeconds + 30 s`, not a hardcoded 10 s),
`internal/cluster/node_dispatcher_timeout_test.go` (new — pins
the derivation policy).
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## Problem

The iris Playwright spec ([ml-iris.spec.ts](../../dashboard/e2e/specs/ml-iris.spec.ts))
runs in `beforeAll`, waits for the workflow to finish, then
asserts the rendered terminal state. That's the right shape for
CI — deterministic, no race on polling, fast (~30 s). But it
hides the one thing a first-time user most wants to see: the
pipeline *running*. Status chips transitioning from `pending` →
`running` → `completed`, job cards flipping from `pending` to
`dispatching` to `completed` in the DAG view, the Pipelines list
auto-refreshing without the operator clicking anything.

We need a second e2e that exercises the **live progression**,
with jobs slow enough for a human (and a test) to observe the
transitions. And it should touch every ML surface — not just
final-state snapshots.

## Current state

- `examples/ml-iris/` — 4-job pipeline (ingest → preprocess →
  train → register) + separate serve job. Runs end-to-end in
  ~10 s on a warm cluster; too fast to watch.
- `dashboard/e2e/specs/ml-iris.spec.ts` — 5 tests, all asserting
  terminal state.
- `dashboard/e2e/specs/ml-iris-walkthrough.spec.ts` — paced
  walkthrough for the `docs/e2e-iris-run.mp4` video (gated
  behind `E2E_RECORD_IRIS_WALKTHROUGH=1`).

Neither existing spec watches `pending → running → completed`
transitions on the live Pipelines view.

## Design

### A second example: MNIST-784

Mirror the iris layout under `examples/ml-mnist/`. MNIST-784
(28×28 pixels flattened to 784 features) is the classic
"deliberately heavier than iris" classification benchmark —
enough rows + features that LogisticRegression training takes
10–30 s, making status transitions observable.

Dataset source: `sklearn.datasets.fetch_openml('mnist_784')`
downloads ~11 MB from OpenML on first run, caches to
`~/.scikit_learn_data`. Network-dependent but same pattern as
iris's `ingest.py` fetching from a scikit-learn GitHub raw URL.
Subsample to 5 000 train + 1 000 test rows inside `preprocess.py`
to keep training in the 10–30 s envelope.

```
examples/ml-mnist/
├── workflow.yaml          # 4-job DAG: ingest → preprocess → train → register
├── ingest.py              # fetches mnist_784 → RAW_CSV output
├── preprocess.py          # RAW_CSV → TRAIN_PARQUET + TEST_PARQUET (subsampled)
├── train.py               # parquet → model.joblib + metrics.json
├── register.py            # POSTs to /api/datasets + /api/models
├── serve.py               # FastAPI /predict takes 784-float feature vector
├── submit.py              # YAML→JSON + workflow-scoped token minting
├── requirements.txt       # same deps as iris
└── README.md              # local-only run guide
```

The same `Dockerfile.node-python` node image bakes both demos
under `/app/ml-iris/` and `/app/ml-mnist/` respectively. Operators
who run only the iris demo pay no cost for the MNIST bundle (the
scripts are ~20 KB; dependencies are already installed for iris).

### The Playwright spec

`dashboard/e2e/specs/ml-mnist-live.spec.ts`. Gated by
`E2E_LOCAL_MNIST=1` env var so CI's wildcard test discovery
skips it with zero overhead — the e2e-iris CI job that already
builds the Python node image will NOT run this spec. Local
operators opt in explicitly.

Test shape (one describe with six sequential tests sharing the
worker-scoped auth page — each assertion picks up where the
previous left off):

1. **"workflow appears on /ml/pipelines with pending or running status"**
   — submits the workflow via REST in `beforeAll`, then
   immediately navigates to `/ml/pipelines` and asserts the
   `mnist-wf-1` row appears with a non-terminal status. This is
   the live-progression entry point.

2. **"DAG detail view shows jobs in flight"** — clicks "View DAG",
   asserts at least one job card is in a non-terminal state
   (polls for up to 30 s looking for `pending` / `dispatching` /
   `running` chips). Watches ingestion progress.

3. **"workflow reaches completed status"** — returns to
   `/ml/pipelines`, polls the row until the status chip shows
   `completed` (up to 180 s). At this point the viewer has seen
   the full transition cycle.

4. **"registry has mnist dataset + model with lineage"** —
   `/ml/datasets` → mnist/v1; `/ml/models` → mnist-logreg/v1
   with `accuracy` metric and `source_job_id` lineage.

5. **"serve job reaches READY"** — submits serve via REST, polls
   `/ml/services` for the ready chip (up to 30 s).

6. **"`POST /predict` returns a valid digit class"** — hits the
   upstream URL from inside the helion-net network and asserts
   the response is one of 0–9. Unlike iris (one true answer for
   a setosa row), MNIST doesn't give us a determined class for
   an arbitrary feature vector; we assert well-formedness + in
   range, not the specific value.

`beforeAll` does NOT wait for completion — the whole point is to
run the tests in parallel with the workflow so transitions are
visible.

### What this demonstrates

| Surface | Assertion |
|---|---|
| Workflow submit → persist → dispatch | Step 1 sees `pending` or `running` in the list |
| Per-job state machine (pending → dispatching → running → completed) | Step 2 catches a non-terminal chip on at least one job card |
| Pipelines list auto-refresh | Step 3 observes the list row's status chip change from non-terminal to `completed` without an explicit reload |
| Registry write path | Steps 4 asserts dataset + model exist |
| Lineage join (`/workflows/{id}/lineage`) | Step 4 validates `source_job_id` + `source_dataset` render on the Models row |
| Metric pills | Step 4 validates `accuracy` metric appears on the Models row |
| Service dispatch + readiness probing | Step 5 waits for the READY chip |
| Upstream URL round-trip | Step 6 hits the predict endpoint |
| Prediction contract (`{predictions: int}`) | Step 6 validates the response shape |

### Why local-only

Three reasons for the `E2E_LOCAL_MNIST=1` gate:

1. **Network dependency.** `fetch_openml` downloads ~11 MB from
   OpenML on first run. CI without that cache would add ~30 s
   per run and fail flaky when OpenML has a bad day.
2. **Duration.** The workflow is deliberately slow (10–30 s just
   for training) to make transitions visible. iris runs in ~10 s
   and is the right shape for CI; MNIST is the right shape for
   demos and local smoke.
3. **Scope separation.** CI asserts terminal state (backend did
   the right thing). Local demo asserts live state (frontend
   shows the right thing while the backend is busy). Two distinct
   invariants; two distinct entry points.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| None | Re-uses feature-19's workflow-scoped `job`-role token path. `submit.py` mints via `POST /admin/tokens` with `subject: workflow:mnist-wf-1`; same adminMiddleware 403 protection against the scoped token escalating. | §1 threat row "leaked workflow token escalates to admin" |

No new trust boundary. Same REST surface, same JWT, same
attestation gate on outputs. The only novel code is the MNIST
scripts themselves (pure numpy/sklearn, no network code beyond
the OpenML fetch inside `ingest.py`).

## Acceptance criteria

1. `docker compose up` with the iris overlay (`-f
   docker-compose.iris.yml`) succeeds — node image picks up the
   new `examples/ml-mnist/` COPY lines.
2. `HELION_API_URL=... HELION_JOB_API_URL=... HELION_TOKEN=...
   python examples/ml-mnist/submit.py examples/ml-mnist/workflow.yaml`
   returns 201 with `id=mnist-wf-1`.
3. Workflow reaches `completed` within 180 s (MNIST training
   timeout).
4. `/api/datasets` → `mnist/v1`; `/api/models` → `mnist-logreg/v1`
   with non-zero accuracy + `source_job_id=mnist-wf-1/train`.
5. `E2E_LOCAL_MNIST=1 npx playwright test ml-mnist-live
   --workers=1` runs all six tests green.
6. CI runs (without the env var) skip the spec with zero
   overhead (assertion: spec reports `1 skipped` or similar).

When all six are met, flip `**Status:**` to **Done**,
`git mv` this file to `planned-features/implemented/`, and
strike the row on `planned-features/README.md`.

## Incidental fixes shipped with this feature

### Dispatch RPC timeout — coordinator cancelled MNIST mid-flight

The coordinator's `GRPCNodeDispatcher.DispatchToNode` had
`context.WithTimeout(ctx, 10*time.Second)` applied to both the dial
and the Dispatch RPC itself. Batch jobs run synchronously on the
node (the Dispatch handler blocks on `rt.Run` until the subprocess
exits), so any job whose wall clock exceeded 10 s got its
subprocess SIGKILLed by the cancelled gRPC stream.

This bug was present since the dispatch loop was introduced in
commit `c5d4646`; iris just barely fit under the ceiling (fastest
jobs < 3 s on a warm cache), so it went unnoticed until MNIST
ingest (~15–30 s for the OpenML fetch + pandas DataFrame) hit it.

**Fix.** `dispatchRPCTimeout(job)` in
`internal/cluster/node_dispatcher.go` picks the timeout:
- batch with `TimeoutSeconds > 0` → `TimeoutSeconds + 30 s` buffer
  (covers staging upload + log stream + ReportResult)
- service jobs → 10 s floor (handler ACKs from a goroutine, no
  wait needed)
- batch without a declared timeout → 10 s floor (preserves legacy
  behaviour for any caller that hasn't set `TimeoutSeconds`)

`node_dispatcher_timeout_test.go` pins all four cases so the
ceiling can't regress silently.

### sklearn 1.5 removed `multi_class` kwarg

`examples/ml-mnist/train.py` called
`LogisticRegression(solver="lbfgs", multi_class="multinomial", …)`
— valid on sklearn 1.4 and earlier, but sklearn 1.5 removed the
kwarg (lbfgs defaults to multinomial for multi-class problems).
On the current Dockerfile.node-python pin the call raised
`TypeError: LogisticRegression.__init__() got an unexpected
keyword argument 'multi_class'` and exit-coded the train job.

**Fix.** Drop the kwarg in `train.py`. Behaviour is identical
(lbfgs + multi-class labels → multinomial) but now compatible
with sklearn 1.5+.
