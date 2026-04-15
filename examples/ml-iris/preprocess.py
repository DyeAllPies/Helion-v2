"""preprocess.py — Step 2 of the iris end-to-end demo.

Reads the iris CSV staged by the ingest step, performs a simple
80/20 stratified train-test split, and writes two parquet files
that the train step consumes.

This step exists in the demo to exercise Helion's multi-output
artifact plumbing end-to-end: a single job reads one input
(HELION_INPUT_RAW_CSV) and produces two outputs
(HELION_OUTPUT_TRAIN_PARQUET + HELION_OUTPUT_TEST_PARQUET), which
the train job then consumes via `from: preprocess.TRAIN_PARQUET`
and `from: preprocess.TEST_PARQUET`.

Environment
-----------
HELION_INPUT_RAW_CSV          Staged input path for the ingest output.
HELION_OUTPUT_TRAIN_PARQUET   Where to write the training split.
HELION_OUTPUT_TEST_PARQUET    Where to write the held-out split.
"""
from __future__ import annotations

import os
import sys

import pandas as pd
from sklearn.model_selection import train_test_split


# sklearn's iris CSV (our ingest source) has a short header row of
# the form `150,4,setosa,versicolor,virginica` before the actual
# data rows. Skipping it yields 150 rows of 4 float features + the
# integer class label in the last column.
FEATURE_COLS = ["sepal_length", "sepal_width", "petal_length", "petal_width"]
LABEL_COL = "species_idx"


def main() -> int:
    src = os.environ.get("HELION_INPUT_RAW_CSV")
    out_train = os.environ.get("HELION_OUTPUT_TRAIN_PARQUET")
    out_test = os.environ.get("HELION_OUTPUT_TEST_PARQUET")
    for name, value in (
        ("HELION_INPUT_RAW_CSV", src),
        ("HELION_OUTPUT_TRAIN_PARQUET", out_train),
        ("HELION_OUTPUT_TEST_PARQUET", out_test),
    ):
        if not value:
            print(f"{name} not set", file=sys.stderr)
            return 1

    # The CSV ships with a metadata header; skip it and assign
    # column names ourselves so downstream code doesn't depend on
    # the upstream mirror's exact column spelling.
    df = pd.read_csv(src, skiprows=1, names=FEATURE_COLS + [LABEL_COL])

    # Stratified split keeps class balance even at 20% held-out —
    # iris has 50 rows per class so plain random split can swing
    # significantly. random_state pinned so the artifact digest is
    # reproducible across runs (BadgerDB dedup key).
    train_df, test_df = train_test_split(
        df, test_size=0.2, stratify=df[LABEL_COL], random_state=42,
    )

    os.makedirs(os.path.dirname(out_train) or ".", exist_ok=True)
    os.makedirs(os.path.dirname(out_test) or ".", exist_ok=True)
    train_df.to_parquet(out_train, index=False)
    test_df.to_parquet(out_test, index=False)

    print(f"train: {len(train_df)} rows → {out_train}")
    print(f"test:  {len(test_df)} rows → {out_test}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
