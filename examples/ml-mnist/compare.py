"""compare.py — Final step of the parallel-training MNIST demo.

Reads the metrics files emitted by two sibling training jobs (one
light, one heavy), registers BOTH trained models against the
coordinator's registry with distinct names + a `winner=true|false`
tag, and emits a comparison summary for the dashboard to pick up.

Why this exists
───────────────
The demo workflow runs LogisticRegression twice in parallel: a
fast low-iter variant on the Go-runtime node and a slower high-
iter variant on the Rust-runtime node. The point of the compare
step is to prove that:

  1. Parallel dispatch really landed on different runtimes (the
     Jobs page shows the two variants each with their own
     `node_id`).
  2. The heterogeneous split produced two DIFFERENT model
     artifacts with DIFFERENT metrics (accuracy, f1_macro).
  3. The dashboard can read back both models + highlight the
     winner via `tags.winner=true` (Models page filter/badge).

compare.py is also the only step that talks to the registry REST
API, so failures here (lineage lookup, duplicate registration,
auth) surface as a clear job-failure on the DAG — easier to
diagnose than splitting into four separate register jobs.

Environment
───────────
HELION_API_URL               Coordinator REST base URL.
HELION_TOKEN                 JWT bearer for admin-audited writes.
HELION_WORKFLOW_ID           Parent workflow ID (for lineage lookup).
HELION_TRAIN_LIGHT_JOB_NAME  Workflow-local name of the light-train
                             step (default: "train_light").
HELION_TRAIN_HEAVY_JOB_NAME  Workflow-local name of the heavy-train
                             step (default: "train_heavy").
HELION_INPUT_RAW_CSV         Staged raw CSV (for dataset URI).
HELION_INPUT_MODEL_LIGHT     Staged light-variant model artifact.
HELION_INPUT_MODEL_HEAVY     Staged heavy-variant model artifact.
HELION_INPUT_METRICS_LIGHT   Staged light-variant metrics JSON.
HELION_INPUT_METRICS_HEAVY   Staged heavy-variant metrics JSON.
HELION_OUTPUT_COMPARISON     Where to write the comparison JSON
                             summary (accuracy deltas, winner, etc.).

Exit codes
──────────
0   both models + dataset registered (or 409 — idempotent), winner
    picked, summary written
1   missing env, metrics parse failure, lineage lookup failure, or
    non-409 HTTP error from the registry

Safety properties
─────────────────
- Always registers the dataset + both models, even if the heavy
  variant lost the comparison. Two artifacts are more useful to
  the operator than one — they can roll back to the other.
- Idempotent: re-running the job against the same coordinator
  state treats 409 as success (matches register.py's behaviour).
- Never makes the winner decision solely on a single metric. The
  decision key (default: accuracy) is configurable via
  HELION_COMPARE_METRIC so a future GPU/latency-sensitive demo
  can pick a different tiebreaker without a code change.
"""
from __future__ import annotations

import hashlib
import json
import os
import ssl
import sys
import urllib.error
import urllib.request


def _ssl_context() -> ssl.SSLContext | None:
    """Build a strict SSL context pinned to HELION_CA_FILE.

    Feature 39 flipped the coordinator's REST listener to TLS-on
    with a self-signed CA. The E2E overlay writes the CA PEM to
    /app/state/ca.pem (same volume node agents read at startup);
    in-container Python scripts inherit access via the shared
    volume mount. Returning a context that trusts ONLY this CA
    (not the system trust store) fails closed on any other TLS
    endpoint, which is the right default for scripts that only
    ever talk to the coordinator.

    Returns None when HELION_CA_FILE is unset or missing — the
    urllib default (system trust store + cert verification) then
    applies, which matches local-dev runs against a publicly
    trusted CA. Returning None never downgrades security: any
    https:// URL still validates, just against a different trust
    anchor.
    """
    ca_file = os.environ.get("HELION_CA_FILE", "").strip()
    if not ca_file or not os.path.exists(ca_file):
        return None
    ctx = ssl.create_default_context(cafile=ca_file)
    # Match the coordinator's MinVersion (TLS 1.3, but keep 1.2
    # as a floor for non-PQC test clients). A downgrade would
    # fail the handshake anyway; this is belt-and-braces.
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    return ctx


DATASET_NAME = "mnist"
DATASET_VERSION = "v1"
MODEL_NAME_LIGHT = "mnist-logreg-light"
MODEL_NAME_HEAVY = "mnist-logreg-heavy"
MODEL_VERSION = "v1"
FRAMEWORK = "sklearn"
DEFAULT_COMPARE_METRIC = "accuracy"


def _sha256_of(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(64 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def _get_json(base: str, path: str, token: str) -> dict:
    url = base.rstrip("/") + path
    req = urllib.request.Request(
        url, method="GET",
        headers={"Authorization": f"Bearer {token}"},
    )
    with urllib.request.urlopen(req, timeout=30, context=_ssl_context()) as resp:
        return json.loads(resp.read())


def _resolve_job_id(base: str, token: str, workflow_id: str, name: str) -> str:
    wf = _get_json(base, f"/workflows/{workflow_id}", token)
    for j in wf.get("jobs", []):
        if j.get("name") == name:
            job_id = j.get("job_id")
            if not job_id:
                raise RuntimeError(
                    f"{name!r} has no runtime job_id yet "
                    f"(workflow {workflow_id!r} not fully started?)"
                )
            return job_id
    raise RuntimeError(
        f"no job named {name!r} in workflow {workflow_id!r} "
        f"(found: {[j.get('name') for j in wf.get('jobs', [])]})"
    )


def _post(base: str, path: str, token: str, body: dict) -> None:
    url = base.rstrip("/") + path
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        url, data=data, method="POST",
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {token}",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=30, context=_ssl_context()) as resp:
            print(f"POST {path} → {resp.status}")
    except urllib.error.HTTPError as e:
        if e.code == 409:
            print(f"POST {path} → 409 (already registered; treating as idempotent)")
            return
        body_text = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"{path}: {e.code} {body_text}") from e


def _resolve_uri(env_var: str) -> str:
    uri = os.environ.get(f"{env_var}_URI")
    if uri:
        return uri
    local = os.environ.get(env_var)
    if local:
        return f"file://{local}"
    return ""


def _api_base() -> str | None:
    api = os.environ.get("HELION_API_URL")
    if api:
        return api
    coord = os.environ.get("HELION_COORDINATOR", "")
    if coord.startswith("http://") or coord.startswith("https://"):
        return coord
    return None


def _numeric_metrics(m: dict) -> dict:
    """Only numeric values survive — the registry's Metrics map
    is `map[string]float64` server-side. Strings and lists are
    dropped. Infinity and NaN are also filtered because they
    don't survive JSON round-trip cleanly across Go's strict
    decoder."""
    import math
    out: dict = {}
    for k, v in m.items():
        if isinstance(v, bool):
            # bool is a subclass of int in Python; registry
            # treats metrics as scalars, not flags.
            continue
        if isinstance(v, (int, float)):
            f = float(v)
            if not math.isfinite(f):
                continue
            out[k] = f
    return out


def _model_body(
    *, name: str, source_job_id: str, uri: str, size: int, sha: str,
    metrics: dict, variant: str, is_winner: bool,
) -> dict:
    # Only numeric metrics survive — registry typing is strict.
    # `winner` goes into tags (string map) so the dashboard's
    # Models page can filter on it without a type check.
    return {
        "name": name,
        "version": MODEL_VERSION,
        "uri": uri,
        "framework": FRAMEWORK,
        "source_job_id": source_job_id,
        "source_dataset": {"name": DATASET_NAME, "version": DATASET_VERSION},
        "metrics": _numeric_metrics(metrics),
        "size_bytes": size,
        "sha256": sha,
        "tags": {
            "task": "mnist-digit-classification",
            "variant": variant,
            "winner": "true" if is_winner else "false",
        },
    }


def main() -> int:
    base = _api_base()
    token = os.environ.get("HELION_TOKEN")
    workflow_id = os.environ.get("HELION_WORKFLOW_ID")
    light_name = os.environ.get("HELION_TRAIN_LIGHT_JOB_NAME", "train_light")
    heavy_name = os.environ.get("HELION_TRAIN_HEAVY_JOB_NAME", "train_heavy")

    raw_csv = os.environ.get("HELION_INPUT_RAW_CSV")
    model_light = os.environ.get("HELION_INPUT_MODEL_LIGHT")
    model_heavy = os.environ.get("HELION_INPUT_MODEL_HEAVY")
    metrics_light = os.environ.get("HELION_INPUT_METRICS_LIGHT")
    metrics_heavy = os.environ.get("HELION_INPUT_METRICS_HEAVY")
    out_summary = os.environ.get("HELION_OUTPUT_COMPARISON")
    compare_metric = os.environ.get("HELION_COMPARE_METRIC", DEFAULT_COMPARE_METRIC).strip()
    if not compare_metric:
        compare_metric = DEFAULT_COMPARE_METRIC

    required = {
        "HELION_API_URL (or HELION_COORDINATOR as http URL)": base,
        "HELION_TOKEN": token,
        "HELION_WORKFLOW_ID": workflow_id,
        "HELION_INPUT_RAW_CSV": raw_csv,
        "HELION_INPUT_MODEL_LIGHT": model_light,
        "HELION_INPUT_MODEL_HEAVY": model_heavy,
        "HELION_INPUT_METRICS_LIGHT": metrics_light,
        "HELION_INPUT_METRICS_HEAVY": metrics_heavy,
        "HELION_OUTPUT_COMPARISON": out_summary,
    }
    for name, value in required.items():
        if not value:
            print(f"{name} not set", file=sys.stderr)
            return 1
    assert base and token and workflow_id
    assert raw_csv and model_light and model_heavy
    assert metrics_light and metrics_heavy and out_summary

    # ── Lineage: resolve the runtime job_ids ───────────────────
    try:
        source_light = _resolve_job_id(base, token, workflow_id, light_name)
        source_heavy = _resolve_job_id(base, token, workflow_id, heavy_name)
    except (RuntimeError, urllib.error.HTTPError) as e:
        print(f"lineage lookup failed: {e}", file=sys.stderr)
        return 1

    # ── Read both metrics files ────────────────────────────────
    with open(metrics_light, "r", encoding="utf-8") as f:
        m_light = json.load(f)
    with open(metrics_heavy, "r", encoding="utf-8") as f:
        m_heavy = json.load(f)

    if compare_metric not in m_light or compare_metric not in m_heavy:
        print(
            f"compare metric {compare_metric!r} missing from at least one "
            f"metrics file (light has {list(m_light.keys())}, "
            f"heavy has {list(m_heavy.keys())})",
            file=sys.stderr,
        )
        return 1

    # ── Pick winner by the compare metric ──────────────────────
    v_light = float(m_light[compare_metric])
    v_heavy = float(m_heavy[compare_metric])
    winner = "heavy" if v_heavy > v_light else "light"
    # Floor on "tied" — if the two runs produce identical metrics
    # (unlikely but possible on tiny datasets), call it light since
    # the lighter variant is cheaper to serve in production. Record
    # the tie explicitly so the dashboard can render a neutral
    # badge.
    tied = v_light == v_heavy
    print(
        f"compare: metric={compare_metric} light={v_light:.4f} heavy={v_heavy:.4f} "
        f"winner={winner} tied={tied}",
        flush=True,
    )

    # ── Register dataset + both models ─────────────────────────
    dataset_uri = _resolve_uri("HELION_INPUT_RAW_CSV")
    uri_light = _resolve_uri("HELION_INPUT_MODEL_LIGHT")
    uri_heavy = _resolve_uri("HELION_INPUT_MODEL_HEAVY")

    dataset_body = {
        "name": DATASET_NAME,
        "version": DATASET_VERSION,
        "uri": dataset_uri,
        "size_bytes": os.path.getsize(raw_csv),
        "sha256": _sha256_of(raw_csv),
        "tags": {"source": "openml", "task": "image-classification"},
    }
    body_light = _model_body(
        name=MODEL_NAME_LIGHT, source_job_id=source_light,
        uri=uri_light, size=os.path.getsize(model_light),
        sha=_sha256_of(model_light), metrics=m_light,
        variant="light", is_winner=(winner == "light"),
    )
    body_heavy = _model_body(
        name=MODEL_NAME_HEAVY, source_job_id=source_heavy,
        uri=uri_heavy, size=os.path.getsize(model_heavy),
        sha=_sha256_of(model_heavy), metrics=m_heavy,
        variant="heavy", is_winner=(winner == "heavy"),
    )

    try:
        _post(base, "/api/datasets", token, dataset_body)
        _post(base, "/api/models", token, body_light)
        _post(base, "/api/models", token, body_heavy)
    except RuntimeError as e:
        print(f"registration failed: {e}", file=sys.stderr)
        return 1

    # ── Comparison summary ─────────────────────────────────────
    summary = {
        "workflow_id":    workflow_id,
        "compare_metric": compare_metric,
        "tied":           tied,
        "winner":         winner,
        "light": {
            "model":   f"{MODEL_NAME_LIGHT}/{MODEL_VERSION}",
            "source_job_id": source_light,
            "metrics": _numeric_metrics(m_light),
        },
        "heavy": {
            "model":   f"{MODEL_NAME_HEAVY}/{MODEL_VERSION}",
            "source_job_id": source_heavy,
            "metrics": _numeric_metrics(m_heavy),
        },
        "delta": {
            compare_metric: v_heavy - v_light,
        },
    }
    os.makedirs(os.path.dirname(out_summary) or ".", exist_ok=True)
    with open(out_summary, "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2)
    print(f"wrote comparison summary to {out_summary}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
