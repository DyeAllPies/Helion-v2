# Feature: Asymmetric `train_light` vs `train_heavy` — dataset size, not just max_iter

**Priority:** P2
**Status:** Implemented (2026-04-21)
**Affected files:**
`examples/ml-mnist/preprocess.py` (honour a `HELION_PREPROCESS_SAMPLES_<LIGHT|HEAVY>` ceiling OR emit two subsample artefacts),
`examples/ml-mnist/train.py` (read the variant-specific sample
ceiling; existing `HELION_TRAIN_VARIANT` + `HELION_TRAIN_MAX_ITER`
stay),
`examples/ml-mnist/workflow.yaml` (wire the two new env vars on
preprocess + pass per-variant on each train step),
`examples/ml-mnist/compare.py` (only if artefact names or shape
change — should not),
`docs/e2e-mnist-parallel-run.mp4` (re-record so wall-clock gap is
visible).

## Problem

Feature 40's `train_light` vs `train_heavy` differ **only in
`max_iter`** (light=50, heavy=400) — both train on the same 5 000
preprocessed MNIST rows. The wall-clock gap is narrow (a few
seconds), which:

- Undercuts the visual story the walkthrough video is trying to
  tell: "heavy training is doing real work, light is a fast
  reference run."
- Makes the parallel-overlap assertion in
  [feature 42](42-parallel-train-overlap-guarantee.md) less
  informative — two jobs finishing within a second of each other
  leave a small overlap window that a scheduler hiccup could erase.
- Doesn't reflect the realistic motivation for a light/heavy fork
  in a real ML pipeline: running a cheap baseline in parallel with
  a more expensive model on a different runtime / hardware tier.

## Current state

- [`examples/ml-mnist/ingest.py:56`](../../../examples/ml-mnist/ingest.py#L56):
  `fetch_openml("mnist_784")` → full 70 000 rows, 28×28, written to
  a single parquet artefact.
- [`examples/ml-mnist/preprocess.py:34-35`](../../../examples/ml-mnist/preprocess.py#L34-L35):
  stratified subsample to 5 000 train + 1 000 test, written as one
  `(X, y)` pair consumed by both `train_light` and `train_heavy`.
- [`examples/ml-mnist/train.py:50-80`](../../../examples/ml-mnist/train.py#L50-L80):
  reads `HELION_TRAIN_MAX_ITER` (clamped [20, 1000]) and
  `HELION_TRAIN_VARIANT`. Dataset is whatever preprocess produced.
- Timing on the 2026-04-20 walkthrough run: `train_light` ~4 s,
  `train_heavy` ~15 s (workflow `duration_ms=53440`). Real — but
  the 11 s gap is mostly max_iter cost on a converged model;
  accuracy is nearly identical.

## Design

### Option A (recommended) — per-variant preprocess sample cap

Keep the MNIST-784 dataset (apples-to-apples comparison preserved;
`compare.py` already works). Add a preprocess-time environment
variable per variant that bounds the sample count:

- `HELION_PREPROCESS_SAMPLES_LIGHT=1000`
  (1 000 train + 200 test — very fast, noisy accuracy)
- `HELION_PREPROCESS_SAMPLES_HEAVY=20000`
  (20 000 train + 4 000 test — slower, firmer accuracy)

Two possible wirings:

1. **One preprocess step, two artefacts.** `preprocess.py` writes
   `{train,test}_light.parquet` and `{train,test}_heavy.parquet`
   side-by-side. `train.py` picks the pair matching its
   `HELION_TRAIN_VARIANT`. DAG stays as it is
   (preprocess → train_light ‖ train_heavy → compare). **Pick this.**
2. Two preprocess steps. Doubles DAG width, adds one more parallel
   fork point — more visual complexity but harder-to-reason-about
   artefact dependencies. Rejected.

### Option B — swap the light variant to `sklearn.datasets.load_digits()`

1 797 samples × 8×8 (64 features). Bundled with sklearn, no network
fetch, trains in well under a second. Dramatic asymmetry (~20–50×
wall-clock gap) but **breaks `compare.py`'s accuracy comparison**:
the two models see different input dimensions and different label
distributions, so "pick the winner by test accuracy" becomes
misleading. Would need a caveat ribbon in compare.py output, plus
an architecture change to treat it as "here are two differently-
shaped models" instead of a head-to-head. Rejected as the primary
path; filed under Deferred.

### Expected wall-clock gap (Option A)

| Variant | Train rows | max_iter | Expected time |
|---------|-----------:|---------:|--------------:|
| light   |      1 000 |       50 |        ~1–2 s |
| heavy   |     20 000 |      400 |      ~25–40 s |

Gap ≥20 s gives feature 42's overlap assertion plenty of margin
and makes the "heavy is doing real work" beat land visually.

### Implementation sketch

`preprocess.py` (shape):

```python
def subsample(X, y, n_train, n_test, seed):
    # existing stratified logic, parametrised
    ...

def main():
    X, y = load_ingested_artefact()
    for variant, env_key in (("light", "HELION_PREPROCESS_SAMPLES_LIGHT"),
                             ("heavy", "HELION_PREPROCESS_SAMPLES_HEAVY")):
        n_train = int(os.environ.get(env_key, DEFAULTS[variant]))
        n_test  = max(100, n_train // 5)
        X_tr, X_te, y_tr, y_te = subsample(X, y, n_train, n_test, seed=42)
        write_parquet(f"train_{variant}.parquet",  X_tr, y_tr)
        write_parquet(f"test_{variant}.parquet",   X_te, y_te)
```

`train.py`:

```python
variant  = os.environ.get("HELION_TRAIN_VARIANT", "light").lower()
train_fp = os.environ["HELION_ARTIFACT_TRAIN"]   # set per-step in workflow.yaml
test_fp  = os.environ["HELION_ARTIFACT_TEST"]
...
```

`workflow.yaml` (fragment):

```yaml
- id: preprocess
  env:
    HELION_PREPROCESS_SAMPLES_LIGHT: "1000"
    HELION_PREPROCESS_SAMPLES_HEAVY: "20000"
  outputs:
    - name: train_light
      path: train_light.parquet
    - name: test_light
      path: test_light.parquet
    - name: train_heavy
      path: train_heavy.parquet
    - name: test_heavy
      path: test_heavy.parquet

- id: train_light
  depends_on: [preprocess]
  env:
    HELION_TRAIN_VARIANT: "light"
    HELION_TRAIN_MAX_ITER: "50"
  inputs:
    - from: preprocess.train_light
      as: train.parquet
    - from: preprocess.test_light
      as: test.parquet
```

Same shape for `train_heavy` with `_heavy` outputs. `compare.py`
unchanged — it still reads `metrics.json` from each train step.

## Security plan

- No new network fetches, no new credentials. Preprocess still runs
  entirely inside the demo container with artefacts staged through
  the existing `internal/artifacts` Store
  ([feature 11](11-ml-artifact-store.md)).
- No new env-var denylist bypass — the two new variables are
  `HELION_PREPROCESS_SAMPLES_*`, subject to the same validation as
  any other env var today. Both are integer-only; `preprocess.py`
  rejects non-digit inputs with `int()` at parse time.
- No RBAC change, no new endpoints, no audit event additions.
- Artefact byte count bounded by the preprocessed-row ceiling. With
  heavy=20 000 rows × 784 float32 features ≈ 62 MiB uncompressed,
  parquet-compressed to ~10 MiB — well under the `max_artifact_bytes`
  ceiling already enforced.

## Implementation order

| # | Step                                                        | Depends on | Effort |
|---|-------------------------------------------------------------|-----------|--------|
| 1 | Refactor `preprocess.py` to emit two artefact pairs          | —         | Small  |
| 2 | Update `train.py` to honour `HELION_ARTIFACT_TRAIN/TEST`     | 1         | Small  |
| 3 | Wire the new env vars + per-variant inputs in `workflow.yaml`| 2         | Small  |
| 4 | Run locally, confirm wall-clock gap ≥20 s                   | 3         | Small  |
| 5 | Re-record `docs/e2e-mnist-parallel-run.mp4`                 | 4         | Small  |

## Tests

**Unit (Python — via pytest under `examples/ml-mnist/tests/`):**
- `test_preprocess_light_sample_count`: default env → 1 000-row
  artefact written.
- `test_preprocess_heavy_sample_count`: env-override → 20 000-row
  artefact.
- `test_preprocess_rejects_non_integer_env`: `SAMPLES_LIGHT=abc`
  exits non-zero with a clear stderr line (feeds into feature 29
  stdout scrubbing but shouldn't involve a secret).

**Integration:**
- End-to-end MNIST walkthrough run — implicit check that
  `compare.py` still produces a winner and registers both models.
  No new spec; feature 42's overlap assertion is the timing check.

## Open questions

- Should defaults (1 000 / 20 000) live in `workflow.yaml` only, or
  also in `preprocess.py` as a fallback for operators running the
  script ad hoc? **Recommendation: both.** Script defaults to
  1 000 / 20 000 if env is unset; workflow.yaml still sets them
  explicitly for documentation.

## Deferred

- Option B (light variant uses `load_digits()`). Filed as
  [`deferred/43-light-variant-load-digits.md`](../deferred/43-light-variant-load-digits.md)
  — revisit if the UX goal shifts from "dramatic time gap" back to
  "dramatic dataset-shape gap."

## Implementation status

_Filled in as the slice lands._
