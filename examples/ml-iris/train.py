"""train.py — Step 3 of the iris end-to-end demo.

Fits a logistic-regression classifier on the training split,
evaluates on the held-out test split, writes the fitted model as
a joblib pickle and a compact metrics JSON file.

Choice of logistic regression (and sklearn, not PyTorch): iris is
a four-feature three-class toy dataset. Training takes milliseconds
on a CPU; the demo's point is the pipeline, not the model. A
full PyTorch + CUDA dep chain would be ~800 MiB of image bloat for
zero measured accuracy benefit.

Environment
-----------
HELION_INPUT_TRAIN_PARQUET   Training rows (staged from preprocess).
HELION_INPUT_TEST_PARQUET    Held-out rows.
HELION_OUTPUT_MODEL          Where to write the fitted model pickle.
HELION_OUTPUT_METRICS        Where to write the metrics JSON blob.

Metrics written
---------------
accuracy       Top-1 accuracy on the held-out split.
f1_macro       Macro-averaged F1 across the three classes.
n_train, n_test, n_features, model_type (for provenance display).
"""
from __future__ import annotations

import json
import os
import sys

import joblib
import pandas as pd
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import accuracy_score, f1_score


FEATURE_COLS = ["sepal_length", "sepal_width", "petal_length", "petal_width"]
LABEL_COL = "species_idx"


def main() -> int:
    train_in = os.environ.get("HELION_INPUT_TRAIN_PARQUET")
    test_in = os.environ.get("HELION_INPUT_TEST_PARQUET")
    model_out = os.environ.get("HELION_OUTPUT_MODEL")
    metrics_out = os.environ.get("HELION_OUTPUT_METRICS")
    for name, value in (
        ("HELION_INPUT_TRAIN_PARQUET", train_in),
        ("HELION_INPUT_TEST_PARQUET", test_in),
        ("HELION_OUTPUT_MODEL", model_out),
        ("HELION_OUTPUT_METRICS", metrics_out),
    ):
        if not value:
            print(f"{name} not set", file=sys.stderr)
            return 1

    train_df = pd.read_parquet(train_in)
    test_df = pd.read_parquet(test_in)

    x_train = train_df[FEATURE_COLS].to_numpy()
    y_train = train_df[LABEL_COL].to_numpy()
    x_test = test_df[FEATURE_COLS].to_numpy()
    y_test = test_df[LABEL_COL].to_numpy()

    # max_iter=1000 is overkill for iris but silences sklearn's
    # convergence warning on older versions. Default solver (lbfgs)
    # handles the multiclass case natively.
    model = LogisticRegression(max_iter=1000, random_state=42)
    model.fit(x_train, y_train)

    preds = model.predict(x_test)
    accuracy = float(accuracy_score(y_test, preds))
    f1_macro = float(f1_score(y_test, preds, average="macro"))

    os.makedirs(os.path.dirname(model_out) or ".", exist_ok=True)
    os.makedirs(os.path.dirname(metrics_out) or ".", exist_ok=True)

    joblib.dump(model, model_out)

    # Include a few provenance fields alongside the metrics so the
    # registry's `metrics` map carries useful context, not just raw
    # accuracy numbers.
    metrics = {
        "accuracy": accuracy,
        "f1_macro": f1_macro,
        "n_train": int(len(x_train)),
        "n_test": int(len(x_test)),
        "n_features": int(x_train.shape[1]),
        "model_type": "sklearn.linear_model.LogisticRegression",
    }
    with open(metrics_out, "w", encoding="utf-8") as f:
        json.dump(metrics, f, indent=2, sort_keys=True)

    print(f"accuracy={accuracy:.4f} f1_macro={f1_macro:.4f}")
    print(f"model → {model_out}")
    print(f"metrics → {metrics_out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
