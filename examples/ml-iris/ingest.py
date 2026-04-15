"""ingest.py — Step 1 of the iris end-to-end demo.

Downloads the UCI iris dataset CSV and writes it to the path Helion
stages as the `RAW_CSV` output. The node-agent's Stager sees the
file under the working directory after the process exits 0 and
uploads it to the artifact store; downstream jobs consume it via
`from: ingest.RAW_CSV` in their workflow spec.

Environment
-----------
HELION_OUTPUT_RAW_CSV   Absolute path the Stager expects to find the
                        output at. Injected by the node agent after
                        it resolves the `outputs: - name: RAW_CSV ...`
                        binding on the workflow job spec.

Exit codes
----------
0   CSV downloaded + written successfully
1   fetch failure (network error, non-200, etc.) — retried by the
    workflow's retry policy if one is set
"""
from __future__ import annotations

import os
import sys
import urllib.request

# UCI's canonical iris CSV. Stable URL, 150 rows, no auth required.
# Using the CSV mirror so we can read it with pandas directly in the
# next step without writing a custom parser for UCI's data/header
# split format.
IRIS_URL = "https://raw.githubusercontent.com/scikit-learn/scikit-learn/1.5.X/sklearn/datasets/data/iris.csv"


def main() -> int:
    out = os.environ.get("HELION_OUTPUT_RAW_CSV")
    if not out:
        print("HELION_OUTPUT_RAW_CSV not set (is this job declaring outputs?)",
              file=sys.stderr)
        return 1

    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)

    try:
        # 10-second timeout so a hung mirror doesn't wedge the
        # whole workflow waiting on an input this job can't produce.
        with urllib.request.urlopen(IRIS_URL, timeout=10) as resp:
            payload = resp.read()
    except Exception as e:  # noqa: BLE001 — one-shot script, log + exit
        print(f"fetch failed: {e}", file=sys.stderr)
        return 1

    with open(out, "wb") as f:
        f.write(payload)

    print(f"wrote {len(payload)} bytes to {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
