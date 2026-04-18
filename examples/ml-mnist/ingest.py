"""ingest.py — Step 1 of the MNIST-784 end-to-end demo.

Fetches the MNIST-784 dataset (28×28 pixels flattened to 784 features,
70 000 rows total) from OpenML via scikit-learn and writes it to the
path Helion stages as the `RAW_CSV` output. Node-agent Stager uploads
on exit 0 and downstream jobs consume via `from: ingest.RAW_CSV`.

Environment
-----------
HELION_OUTPUT_RAW_CSV   Absolute path the Stager expects to find the
                        output at. Injected by the node agent after
                        it resolves the `outputs: - name: RAW_CSV ...`
                        binding on the workflow job spec.

Exit codes
----------
0   CSV written successfully
1   fetch failure (network, OpenML unavailable) — no retry here; the
    workflow retry policy (if any) re-runs the whole step.

Notes
-----
Downloads ~11 MB from OpenML on first run and caches to
~/.scikit_learn_data. Subsequent runs read from cache (~1–2 s).
The full dataset lands in the output file; `preprocess.py`
subsamples to keep training in the 10–30 s envelope that makes
workflow transitions visible on the Pipelines list.
"""
from __future__ import annotations

import os
import sys

import pandas as pd


def main() -> int:
    out = os.environ.get("HELION_OUTPUT_RAW_CSV")
    if not out:
        print("HELION_OUTPUT_RAW_CSV not set (is this job declaring outputs?)",
              file=sys.stderr)
        return 1

    print("fetching mnist_784 from OpenML (~11 MB on first run)…", flush=True)
    # sklearn's default cache path is ~/.scikit_learn_data, which
    # breaks when the node runs as a user without a writable home
    # (Dockerfile.node-python creates `helion` with `useradd -M`,
    # no home directory → Errno 13 permission denied on /home/helion).
    # Pin the cache under /tmp so the fetch always has a writable
    # target. Per-job cache is fine for the demo — the first job
    # pays the ~30 s download cost, subsequent runs in the same
    # container hit the cache.
    cache_dir = os.environ.get("SCIKIT_LEARN_DATA", "/tmp/scikit_learn_data")
    os.makedirs(cache_dir, exist_ok=True)
    try:
        from sklearn.datasets import fetch_openml
        # parser='auto' lets sklearn pick the faster pandas parser when
        # available. as_frame=True keeps the DataFrame shape so we can
        # pickle it to CSV with a stable column layout.
        bundle = fetch_openml(
            "mnist_784", version=1,
            as_frame=True, parser="auto",
            data_home=cache_dir,
        )
    except Exception as e:  # noqa: BLE001 — one-shot script, log + exit
        print(f"fetch failed: {e}", file=sys.stderr)
        return 1

    frame: pd.DataFrame = bundle.frame
    rows, cols = frame.shape
    print(f"loaded {rows} rows × {cols} columns (784 pixel features + class label)",
          flush=True)

    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    # Write CSV with no index (Stager uploads the file verbatim).
    frame.to_csv(out, index=False)
    size = os.path.getsize(out)
    print(f"wrote {size} bytes to {out}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
