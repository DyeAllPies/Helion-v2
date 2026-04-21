"""preprocess.py — Step 2 of the MNIST-784 end-to-end demo.

Reads the full MNIST CSV staged by the ingest step and writes TWO
stratified subsamples — one sized for the light-variant train step,
one sized for the heavy-variant train step — as four parquet files.
The two train jobs then consume their own pair, which gives the
parallel-training demo a visibly asymmetric workload (light is a
cheap baseline, heavy does the real work on a different runtime).

Downstream parquet format (same for both variants):
  - 784 `px0..px783` feature columns (uint8 pixel intensities)
  - 1   `label` column (int, class 0-9)

Environment
-----------
HELION_INPUT_RAW_CSV                 Staged input path for the ingest output.
HELION_OUTPUT_TRAIN_LIGHT_PARQUET    Where to write the light-variant train split.
HELION_OUTPUT_TEST_LIGHT_PARQUET     Where to write the light-variant held-out split.
HELION_OUTPUT_TRAIN_HEAVY_PARQUET    Where to write the heavy-variant train split.
HELION_OUTPUT_TEST_HEAVY_PARQUET     Where to write the heavy-variant held-out split.
HELION_PREPROCESS_SAMPLES_LIGHT      Optional; light-variant train-row count
                                     (default 1000). Clamped to
                                     [MIN_TRAIN_ROWS, MAX_TRAIN_ROWS].
HELION_PREPROCESS_SAMPLES_HEAVY      Optional; heavy-variant train-row count
                                     (default 20000). Clamped likewise.

The test split size is `max(200, n_train // 5)` per variant so each
holdout has enough class coverage for a stratified split.

Exit codes
----------
0   all four parquet files written
1   missing env var, parse failure, or not-enough-rows-in-input
"""
from __future__ import annotations

import os
import sys

import pandas as pd
from sklearn.model_selection import train_test_split

# Stratified split needs at least MIN_TRAIN_ROWS total train rows to
# guarantee every digit class has a representative (MNIST has 10
# classes, so the floor here is conservatively above 10×10=100).
# The upper bound keeps pathological operator inputs from blowing
# past the timeout_seconds on the train step.
MIN_TRAIN_ROWS = 200
MAX_TRAIN_ROWS = 60_000
RANDOM_SEED = 42  # reproducible splits; the train step uses the same.

# Defaults chosen so the two variants have a visibly asymmetric
# wall-clock footprint on the walkthrough video — see feature 43.
DEFAULT_LIGHT_TRAIN = 1_000
DEFAULT_HEAVY_TRAIN = 20_000


def parse_samples(env_name: str, default: int) -> int:
    """Parse a sample-count env var within [MIN, MAX]; fall back to default.

    Defensive: preprocess runs under the coordinator's env plumbing so
    a misconfigured workflow.yaml could pass anything here. Rejecting
    non-integer or out-of-range values with a warning + fallback keeps
    the demo robust without hard-failing on a typo.
    """
    raw = os.environ.get(env_name, "").strip()
    if not raw:
        return default
    try:
        n = int(raw)
    except ValueError:
        print(
            f"warning: {env_name}={raw!r} not an int; using {default}",
            file=sys.stderr,
        )
        return default
    if n < MIN_TRAIN_ROWS or n > MAX_TRAIN_ROWS:
        print(
            f"warning: {env_name}={n} out of range "
            f"[{MIN_TRAIN_ROWS}, {MAX_TRAIN_ROWS}]; using {default}",
            file=sys.stderr,
        )
        return default
    return n


def _test_size(n_train: int) -> int:
    """Pick a test-split size proportional to train, with a floor."""
    return max(200, n_train // 5)


def _subsample(df: pd.DataFrame, n_train: int, n_test: int) -> tuple[pd.DataFrame, pd.DataFrame]:
    """Draw a stratified (train, test) pair sized n_train + n_test from df.

    The pool is sampled down to exactly n_train+n_test first so
    train_test_split's sizes are exact integers (avoids sklearn's
    float-vs-int rounding on very small configurations).
    """
    total = n_train + n_test
    if len(df) < total:
        raise ValueError(
            f"input has {len(df)} rows; need at least {total} for "
            f"a {n_train}-train / {n_test}-test split"
        )
    pool = df.sample(n=total, random_state=RANDOM_SEED).reset_index(drop=True)
    return train_test_split(
        pool,
        train_size=n_train,
        test_size=n_test,
        random_state=RANDOM_SEED,
        stratify=pool["label"],
    )


def main() -> int:
    src = os.environ.get("HELION_INPUT_RAW_CSV")
    out_paths = {
        "train_light": os.environ.get("HELION_OUTPUT_TRAIN_LIGHT_PARQUET"),
        "test_light":  os.environ.get("HELION_OUTPUT_TEST_LIGHT_PARQUET"),
        "train_heavy": os.environ.get("HELION_OUTPUT_TRAIN_HEAVY_PARQUET"),
        "test_heavy":  os.environ.get("HELION_OUTPUT_TEST_HEAVY_PARQUET"),
    }
    missing = [n for n, v in (("HELION_INPUT_RAW_CSV", src),
                              *[(f"HELION_OUTPUT_{k.upper()}_PARQUET", v)
                                for k, v in out_paths.items()]) if not v]
    if missing:
        print(f"missing env: {', '.join(missing)}", file=sys.stderr)
        return 1

    n_train_light = parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", DEFAULT_LIGHT_TRAIN)
    n_train_heavy = parse_samples("HELION_PREPROCESS_SAMPLES_HEAVY", DEFAULT_HEAVY_TRAIN)
    n_test_light = _test_size(n_train_light)
    n_test_heavy = _test_size(n_train_heavy)

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

    try:
        train_light, test_light = _subsample(df, n_train_light, n_test_light)
        train_heavy, test_heavy = _subsample(df, n_train_heavy, n_test_heavy)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        return 1

    print(
        f"light split: {len(train_light)} train + {len(test_light)} test",
        flush=True,
    )
    print(
        f"heavy split: {len(train_heavy)} train + {len(test_heavy)} test",
        flush=True,
    )

    splits = {
        "train_light": train_light,
        "test_light":  test_light,
        "train_heavy": train_heavy,
        "test_heavy":  test_heavy,
    }
    for key, frame in splits.items():
        path = out_paths[key]
        assert path is not None  # checked above
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        frame.to_parquet(path, index=False)
        print(f"wrote {path} ({os.path.getsize(path)} bytes)", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
