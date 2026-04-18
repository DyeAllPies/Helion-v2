"""preprocess.py — Step 2 of the MNIST-784 end-to-end demo.

Reads the full MNIST CSV staged by the ingest step, performs a
stratified subsample to keep training fast (5 000 train + 1 000 test
rows), and writes two parquet files that the train step consumes.

Downstream parquet format:
  - 784 `px0..px783` feature columns (uint8 pixel intensities)
  - 1   `label` column (int, class 0-9)

Environment
-----------
HELION_INPUT_RAW_CSV          Staged input path for the ingest output.
HELION_OUTPUT_TRAIN_PARQUET   Where to write the training split.
HELION_OUTPUT_TEST_PARQUET    Where to write the held-out split.

Exit codes
----------
0   both parquet files written
1   missing env var or parse failure
"""
from __future__ import annotations

import os
import sys

import pandas as pd
from sklearn.model_selection import train_test_split

# Kept deliberately small — LogisticRegression on 5 000 × 784 takes
# ~10–30 s, which is the observable window the Playwright spec relies
# on. Raising these values is safe but makes the transitions harder
# to catch on the Pipelines list.
N_TRAIN = 5_000
N_TEST = 1_000
RANDOM_SEED = 42  # reproducible splits; the train step uses the same.


def main() -> int:
    src = os.environ.get("HELION_INPUT_RAW_CSV")
    out_train = os.environ.get("HELION_OUTPUT_TRAIN_PARQUET")
    out_test = os.environ.get("HELION_OUTPUT_TEST_PARQUET")
    if not src or not out_train or not out_test:
        missing = [n for n, v in (("HELION_INPUT_RAW_CSV", src),
                                  ("HELION_OUTPUT_TRAIN_PARQUET", out_train),
                                  ("HELION_OUTPUT_TEST_PARQUET", out_test)) if not v]
        print(f"missing env: {', '.join(missing)}", file=sys.stderr)
        return 1

    print(f"reading {src}…", flush=True)
    df = pd.read_csv(src)
    print(f"loaded {len(df)} rows × {df.shape[1]} columns", flush=True)

    # OpenML's mnist_784 columns: pixel1..pixel784 + 'class'. Rename
    # to snake_case for downstream consumers.
    if "class" in df.columns:
        df = df.rename(columns={"class": "label"})
    # Cast label to int so parquet stores it compactly (OpenML returns
    # strings like '5').
    df["label"] = df["label"].astype(int)
    # Cast pixel features to uint8 — pixel values are 0-255 integers.
    pixel_cols = [c for c in df.columns if c != "label"]
    df[pixel_cols] = df[pixel_cols].astype("uint8")

    # Stratified split — maintain class proportions so the test set
    # isn't degenerate for any digit.
    total = N_TRAIN + N_TEST
    if len(df) > total:
        df = df.sample(n=total, random_state=RANDOM_SEED).reset_index(drop=True)

    train_df, test_df = train_test_split(
        df, train_size=N_TRAIN, test_size=N_TEST,
        random_state=RANDOM_SEED, stratify=df["label"],
    )
    print(f"split into {len(train_df)} train + {len(test_df)} test", flush=True)

    os.makedirs(os.path.dirname(out_train) or ".", exist_ok=True)
    os.makedirs(os.path.dirname(out_test) or ".", exist_ok=True)
    train_df.to_parquet(out_train, index=False)
    test_df.to_parquet(out_test, index=False)
    print(f"wrote {out_train} ({os.path.getsize(out_train)} bytes)", flush=True)
    print(f"wrote {out_test} ({os.path.getsize(out_test)} bytes)", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
