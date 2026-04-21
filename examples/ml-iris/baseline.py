"""baseline.py — parallel sibling to train.py in the iris pipeline.

Fits a stratified DummyClassifier on the same train/test split used
by train.py and emits a baseline metrics JSON. Exists for two
reasons:

  1. Orchestration coverage (feature 42). A parallel sibling gives
     the dispatcher a chance to actually dispatch two jobs
     concurrently in the fast iris CI path — the MNIST walkthrough
     covers this visually, iris covers it in ~seconds.
  2. Model comparison context. Showing a DummyClassifier-stratified
     floor next to the LogisticRegression numbers makes "the model
     learned something" a concrete claim in the dashboard.

Environment
-----------
HELION_INPUT_TRAIN_PARQUET   Training rows (staged from preprocess).
HELION_INPUT_TEST_PARQUET    Held-out rows.
HELION_OUTPUT_METRICS        Where to write the baseline metrics JSON.

Metrics written
---------------
accuracy           Top-1 accuracy on the held-out split.
f1_macro           Macro-averaged F1 across the three classes.
n_train, n_test, n_features, model_type, variant=baseline (for
provenance display and feature-43-style variant diffing).
"""
from __future__ import annotations

import json
import os
import sys

import pandas as pd
from sklearn.dummy import DummyClassifier
from sklearn.metrics import accuracy_score, f1_score


FEATURE_COLS = ["sepal_length", "sepal_width", "petal_length", "petal_width"]
LABEL_COL = "species_idx"


def main() -> int:
    train_in = os.environ.get("HELION_INPUT_TRAIN_PARQUET")
    test_in = os.environ.get("HELION_INPUT_TEST_PARQUET")
    metrics_out = os.environ.get("HELION_OUTPUT_METRICS")
    for name, value in (
        ("HELION_INPUT_TRAIN_PARQUET", train_in),
        ("HELION_INPUT_TEST_PARQUET", test_in),
        ("HELION_OUTPUT_METRICS", metrics_out),
    ):
        if not value:
            print(f"{name} not set", file=sys.stderr)
            return 1
    assert train_in and test_in and metrics_out

    train_df = pd.read_parquet(train_in)
    test_df = pd.read_parquet(test_in)

    x_train = train_df[FEATURE_COLS].to_numpy()
    y_train = train_df[LABEL_COL].to_numpy()
    x_test = test_df[FEATURE_COLS].to_numpy()
    y_test = test_df[LABEL_COL].to_numpy()

    # Stratified baseline: predicts by class-frequency drawn from
    # the train split. For balanced iris this lands at ~1/3, which
    # is the honest "no learning" floor for train.py to beat.
    model = DummyClassifier(strategy="stratified", random_state=42)
    model.fit(x_train, y_train)

    preds = model.predict(x_test)
    accuracy = float(accuracy_score(y_test, preds))
    f1_macro = float(f1_score(y_test, preds, average="macro"))

    os.makedirs(os.path.dirname(metrics_out) or ".", exist_ok=True)
    metrics = {
        "accuracy": accuracy,
        "f1_macro": f1_macro,
        "n_train": int(len(x_train)),
        "n_test": int(len(x_test)),
        "n_features": int(x_train.shape[1]),
        "model_type": "sklearn.dummy.DummyClassifier",
        "variant": "baseline",
    }
    with open(metrics_out, "w", encoding="utf-8") as f:
        json.dump(metrics, f, indent=2, sort_keys=True)

    print(f"baseline: accuracy={accuracy:.4f} f1_macro={f1_macro:.4f}")
    print(f"metrics → {metrics_out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
