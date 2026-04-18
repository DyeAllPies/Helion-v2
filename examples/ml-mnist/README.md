# MNIST-784 — local-only end-to-end ML demo

Heavier sibling of the [iris demo](../ml-iris/README.md). Same
pipeline shape — ingest → preprocess → train → register → serve —
but with a MNIST-784 dataset (28×28 pixels flattened to 784
features) and LogisticRegression training on 5 000 subsampled
rows. The workflow takes ~30–90 s end-to-end, which is the point:
long enough to watch the Pipelines list tick through
`pending` → `running` → `completed` and the DAG view's job
cards flip chips.

```
           ┌──────────┐      ┌────────────┐      ┌────────┐      ┌──────────┐
workflow:  │  ingest  │ ───▶ │ preprocess │ ───▶ │ train  │ ───▶ │ register │
           └──────────┘      └────────────┘      └────────┘      └──────────┘
        fetch mnist_784    stratified split    LogisticRegression    POSTs to
        from OpenML        (5k train + 1k      multinomial,          /api/datasets
        (~11 MB)           test, 784 px cols)  lbfgs, max_iter=200   /api/models
                                                                     with lineage

separate submit (service job, not part of DAG):
           ┌──────────┐
           │   serve  │ ──▶ /healthz  (probe every 5 s)
           │  (fastapi│ ──▶ /predict  (POST {"features": [[784 floats], ...]})
           └──────────┘
```

> **⚠️ Local testing only.** This demo is NOT wired into CI.
> The companion Playwright spec at
> [`dashboard/e2e/specs/ml-mnist-live.spec.ts`](../../dashboard/e2e/specs/ml-mnist-live.spec.ts)
> is gated behind `E2E_LOCAL_MNIST=1`. The feature-19 iris demo
> is the CI-level ML acceptance test; MNIST is for observing live
> workflow progression during demos and local smoke-testing.

---

## Prerequisites

Same as iris — Go toolchain, Docker Compose, Python 3.11 with
PyYAML, a Helion bearer token. First-time fetch of MNIST from
OpenML takes ~30 s and caches under `~/.scikit_learn_data`.

The Python node image [`Dockerfile.node-python`](../../Dockerfile.node-python)
bakes both the iris AND MNIST scripts (under `/app/ml-iris/` and
`/app/ml-mnist/` respectively) so the iris compose overlay works
for both demos with no changes.

---

## Running it

Same three-file compose as iris:

```bash
# 1. Start the cluster
rm -rf state/ logs/ && mkdir -p state logs
COMPOSE_PROFILES=analytics,ml docker compose \
  -f docker-compose.yml \
  -f docker-compose.e2e.yml \
  -f docker-compose.iris.yml \
  up -d --build
```

The overlay is named `iris` but it's the generic Python-capable
node overlay — it also covers MNIST.

```bash
# 2. Export environment
export HELION_API_URL=http://127.0.0.1:8080
export HELION_JOB_API_URL=http://coordinator:8080
export HELION_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token)

# 3. Submit the workflow
cd examples/ml-mnist
python submit.py workflow.yaml
```

On a warm cache: ingest ~5 s, preprocess ~5 s, train ~15–30 s,
register ~1 s. Total ~30–50 s. A cold OpenML cache adds ~30 s.

Watch progress on the dashboard:

- `/ml/pipelines` — top-level status (auto-polls, chip flips)
- `/ml/pipelines/mnist-wf-1` — DAG view with per-job chips
- `/events` — `dataset.registered`, `model.registered`,
  `job.unschedulable` (reason), `ml.resolve_failed` if any
- `/ml/datasets` → mnist/v1 once register completes
- `/ml/models` → mnist-logreg/v1 with lineage + accuracy pill

### Serving — manual step

`submit.py --serve` is wired but depends on the `registry://`
input-URI scheme which is not yet implemented by the coordinator.
Until it lands, submit the serve job by hand:

```bash
# Resolve the model URI from the workflow lineage endpoint
# (register.py stamps file:// on local backends; use the
# authoritative s3:// URI from the lineage's train.MODEL output).
MODEL_URI=$(curl -s -H "Authorization: Bearer $HELION_TOKEN" \
  "$HELION_API_URL/workflows/mnist-wf-1/lineage" \
  | python -c "import sys,json; d=json.load(sys.stdin); \
      [print(o['uri']) for j in d['jobs'] if j['name']=='train' \
       for o in j.get('outputs',[]) if o['name']=='MODEL']")

cat <<JSON | curl -s -X POST \
  -H "Authorization: Bearer $HELION_TOKEN" \
  -H "Content-Type: application/json" \
  "$HELION_API_URL/jobs" -d @-
{
  "id": "mnist-serve-1",
  "command": "uvicorn",
  "args": ["serve:app", "--host", "0.0.0.0", "--port", "8000"],
  "env": {"PYTHONPATH": "/app/ml-mnist"},
  "inputs": [{"name": "MODEL", "uri": "$MODEL_URI", "local_path": "model.joblib"}],
  "service": {"port": 8000, "health_path": "/healthz", "health_initial_ms": 2000}
}
JSON
```

Test a prediction. A feature vector of 784 pixels is awkward to
type by hand, so build one from the test parquet (or just send
all-zeros which the model will classify as something):

```bash
# 784 zeros (all black pixels)
FEATURES=$(python -c "import json; print(json.dumps([[0.0]*784]))")
UPSTREAM=$(curl -s -H "Authorization: Bearer $HELION_TOKEN" \
  "$HELION_API_URL/api/services/mnist-serve-1" \
  | python -c "import sys,json; d=json.load(sys.stdin); \
      print(d['upstream_url'].rsplit('/',1)[0])")
docker exec helion-coordinator \
  wget -qO- --header='Content-Type: application/json' \
       --post-data="{\"features\":$(python -c 'import json; print(json.dumps([[0.0]*784]))')}" \
       "$UPSTREAM/predict"
# → {"predictions":[N]}  (N is whichever digit the model thinks
#                         "all black" looks most like)
```

---

## Playwright local-only test

```bash
# Workflow should already be up (previous steps leave the cluster running
# if you don't docker-compose down).
cd dashboard
E2E_LOCAL_MNIST=1 E2E_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token) \
  npx playwright test ml-mnist-live --workers=1
```

The spec has six tests covering the full ML-pipeline surface in
live-progression mode:

1. Workflow appears on Pipelines with `pending` or `running`
   status (polled immediately after submit).
2. DAG detail view shows at least one job in a non-terminal
   state.
3. Workflow reaches `completed` (up to 180 s).
4. Registry shows mnist/v1 + mnist-logreg/v1 with lineage +
   metrics.
5. Serve job reaches READY.
6. `POST /predict` returns a valid digit class (0–9).

Without `E2E_LOCAL_MNIST=1`, the spec reports "skipped" and the
regular CI Playwright run moves past it with zero overhead.

---

## File reference

| File | Purpose |
|------|---------|
| [`workflow.yaml`](workflow.yaml) | 4-job DAG (same shape as iris). |
| [`ingest.py`](ingest.py) | `sklearn.datasets.fetch_openml('mnist_784')` → CSV. |
| [`preprocess.py`](preprocess.py) | Stratified 5k train + 1k test subsample, pixel→uint8. |
| [`train.py`](train.py) | LogisticRegression(multinomial, lbfgs) → joblib + metrics JSON. |
| [`register.py`](register.py) | POSTs to `/api/datasets` + `/api/models` with lineage. |
| [`serve.py`](serve.py) | FastAPI `/healthz` + `/predict` (784-float feature vector in, class int out). |
| [`submit.py`](submit.py) | YAML→JSON + workflow-scoped token minting (same as iris). |
| [`requirements.txt`](requirements.txt) | Pip deps (identical to iris set). |

---

## Known caveats

1. **OpenML network dependency.** `ingest.py` fetches from OpenML
   on first run. If OpenML is unavailable the step fails; the
   workflow retries if configured, but the default YAML has no
   retry policy. Re-runs after the first successful fetch read
   from `~/.scikit_learn_data` and are fast (~1 s).
2. **Training time.** LogisticRegression on 5 000 × 784 fits in
   10–30 s on a modern laptop CPU. Slower CPUs (CI runners,
   containers without AVX) push this toward 60 s; the
   `timeout_seconds: 180` on the train step gives plenty of
   margin.
3. **Prediction accuracy.** The subsampled training set + simple
   LogisticRegression gets ~91 % test accuracy, not the
   state-of-the-art ~98 %. The point is pipeline plumbing, not
   model quality.
