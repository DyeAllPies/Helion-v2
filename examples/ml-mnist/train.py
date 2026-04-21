"""train.py — Step 3 of the MNIST-784 end-to-end demo.

Trains a multiclass LogisticRegression classifier on the preprocessed
MNIST parquet, evaluates on the held-out split, writes the model +
metrics. The full training loop is the observable "running" phase of
the pipeline — on 5 000 × 784 rows this takes ~10–30 s depending on
CPU and the configured max-iteration ceiling.

The parallel-training demo (see compare.py) runs this script twice
in the same workflow — once with HELION_TRAIN_MAX_ITER=50 on the
Go-runtime node (fast, less converged) and once with =400 on the
Rust-runtime node (slower, more accurate). The `variant` tag in
metrics.json lets compare.py render a clear A/B row on the demo
analytics panel.

Environment
-----------
HELION_INPUT_TRAIN_PARQUET  Staged train split.
HELION_INPUT_TEST_PARQUET   Staged held-out split.
HELION_OUTPUT_MODEL         Where to write the joblib-serialised model.
HELION_OUTPUT_METRICS       Where to write JSON metrics for register.py.
HELION_TRAIN_MAX_ITER       Optional; LogisticRegression max_iter ceiling
                            (default 200). Values between 20 and 1000 are
                            clamped to that range; anything outside
                            falls back to the default with a warning.
HELION_TRAIN_VARIANT        Optional free-form label (e.g. "light",
                            "heavy") written verbatim into metrics.json
                            so the compare step can pick it up. Falls
                            back to "default" if unset.

Exit codes
----------
0   training + eval succeeded; both outputs written
1   missing env, parse failure, or training error
"""
from __future__ import annotations

import json
import os
import sys
import time

import joblib
import numpy as np
import pandas as pd
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import accuracy_score, f1_score

RANDOM_SEED = 42
DEFAULT_MAX_ITER = 200
MIN_MAX_ITER = 20
MAX_MAX_ITER = 1000


def parse_max_iter() -> int:
    """Parse HELION_TRAIN_MAX_ITER within [MIN, MAX]; fall back to default.

    Defensive: sklearn's LogisticRegression silently accepts absurd values
    (negative, zero, 100_000). The minimum floor prevents a "didn't
    actually converge" footgun in the light-variant path; the maximum
    ceiling prevents a demo run from blowing past the workflow's
    timeout_seconds and failing the parallel-training A/B.
    """
    raw = os.environ.get("HELION_TRAIN_MAX_ITER", "").strip()
    if not raw:
        return DEFAULT_MAX_ITER
    try:
        n = int(raw)
    except ValueError:
        print(f"warning: HELION_TRAIN_MAX_ITER={raw!r} not an int; using {DEFAULT_MAX_ITER}",
              file=sys.stderr)
        return DEFAULT_MAX_ITER
    if n < MIN_MAX_ITER or n > MAX_MAX_ITER:
        print(
            f"warning: HELION_TRAIN_MAX_ITER={n} out of range "
            f"[{MIN_MAX_ITER}, {MAX_MAX_ITER}]; using {DEFAULT_MAX_ITER}",
            file=sys.stderr,
        )
        return DEFAULT_MAX_ITER
    return n


def main() -> int:
    in_train = os.environ.get("HELION_INPUT_TRAIN_PARQUET")
    in_test = os.environ.get("HELION_INPUT_TEST_PARQUET")
    out_model = os.environ.get("HELION_OUTPUT_MODEL")
    out_metrics = os.environ.get("HELION_OUTPUT_METRICS")
    if not (in_train and in_test and out_model and out_metrics):
        print("one of HELION_INPUT_TRAIN_PARQUET/TEST_PARQUET/OUTPUT_MODEL/OUTPUT_METRICS not set",
              file=sys.stderr)
        return 1

    max_iter = parse_max_iter()
    variant = os.environ.get("HELION_TRAIN_VARIANT", "default").strip() or "default"

    print(f"reading train {in_train}…", flush=True)
    train_df: pd.DataFrame = pd.read_parquet(in_train)
    print(f"reading test  {in_test}…", flush=True)
    test_df: pd.DataFrame = pd.read_parquet(in_test)

    feature_cols = [c for c in train_df.columns if c != "label"]
    x_train = train_df[feature_cols].to_numpy(dtype=np.float32) / 255.0
    y_train = train_df["label"].to_numpy()
    x_test = test_df[feature_cols].to_numpy(dtype=np.float32) / 255.0
    y_test = test_df["label"].to_numpy()

    print(
        f"fitting LogisticRegression(max_iter={max_iter}, variant={variant}) "
        f"on {x_train.shape[0]} × {x_train.shape[1]}…",
        flush=True,
    )
    t0 = time.time()
    # lbfgs + default multinomial is the classic MNIST-784 recipe.
    # sklearn >=1.5 removed the `multi_class` kwarg (always multinomial
    # for lbfgs with multi-class labels), so we drop it here — the
    # 0.6.x-era `multi_class="multinomial"` call raises TypeError on
    # modern wheels. max_iter is tunable per-variant so the parallel
    # demo pipeline exhibits a clear A/B: 50 iters converges fast but
    # not fully, 400 iters converges fully but is observably slower.
    model = LogisticRegression(
        solver="lbfgs",
        max_iter=max_iter,
        random_state=RANDOM_SEED,
        n_jobs=1,
    )
    model.fit(x_train, y_train)
    fit_seconds = time.time() - t0
    print(f"fit in {fit_seconds:.1f} s", flush=True)

    print("evaluating on held-out test split…", flush=True)
    preds = model.predict(x_test)
    accuracy = float(accuracy_score(y_test, preds))
    f1_macro = float(f1_score(y_test, preds, average="macro"))
    print(f"accuracy={accuracy:.4f}  f1_macro={f1_macro:.4f}", flush=True)

    os.makedirs(os.path.dirname(out_model) or ".", exist_ok=True)
    os.makedirs(os.path.dirname(out_metrics) or ".", exist_ok=True)
    joblib.dump(model, out_model)
    with open(out_metrics, "w", encoding="utf-8") as f:
        json.dump({
            "accuracy": accuracy,
            "f1_macro": f1_macro,
            "n_features": len(feature_cols),
            "n_train": int(x_train.shape[0]),
            "n_test": int(x_test.shape[0]),
            "n_classes": int(len(np.unique(y_train))),
            "fit_seconds": fit_seconds,
            "max_iter": max_iter,
            "variant": variant,
        }, f, indent=2)
    print(f"wrote {out_model} + {out_metrics}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
