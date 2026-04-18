"""register.py — Step 4 of the MNIST-784 end-to-end demo.

Registers the dataset + trained model against the coordinator's
registries so they're discoverable via /api/datasets and /api/models.

This step does *not* upload bytes — Helion's Stager already did that
when the training job exited 0. We register metadata: names, versions,
URIs (which the node's Stager assigned), metrics (from the metrics
JSON), and the source-job-id lineage pointer that lights up the DAG
view's `models_produced` chip.

Environment
-----------
HELION_API_URL (or HELION_COORDINATOR as http URL)  Coordinator REST base.
HELION_TOKEN                                        JWT bearer.
HELION_WORKFLOW_ID                                  Parent workflow ID.
HELION_TRAIN_JOB_NAME                               Workflow-local name of train step.
HELION_INPUT_RAW_CSV                                Staged raw CSV (for dataset URI).
HELION_INPUT_MODEL                                  Staged model (for model URI).
HELION_INPUT_METRICS                                Staged metrics JSON.

Exit codes
----------
0   both registry entries written (or 409 already — treated as
    idempotent)
1   missing env, lineage lookup failure, or non-409 HTTP error
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
import urllib.error
import urllib.request

DATASET_NAME = "mnist"
DATASET_VERSION = "v1"
MODEL_NAME = "mnist-logreg"
MODEL_VERSION = "v1"
FRAMEWORK = "sklearn"


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
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def _resolve_train_job_id(base: str, token: str, workflow_id: str, train_name: str) -> str:
    wf = _get_json(base, f"/workflows/{workflow_id}", token)
    for j in wf.get("jobs", []):
        if j.get("name") == train_name:
            job_id = j.get("job_id")
            if not job_id:
                raise RuntimeError(
                    f"train job {train_name!r} has no runtime job_id yet "
                    f"(workflow {workflow_id!r} not fully started?)"
                )
            return job_id
    raise RuntimeError(
        f"no job named {train_name!r} in workflow {workflow_id!r} "
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
        with urllib.request.urlopen(req, timeout=30) as resp:
            print(f"POST {path} → {resp.status}")
    except urllib.error.HTTPError as e:
        if e.code == 409:
            print(f"POST {path} → 409 (already registered; treating as idempotent)")
            return
        body_text = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"{path}: {e.code} {body_text}") from e


def _resolve_uri(env_var: str) -> str | None:
    """Prefer HELION_INPUT_<NAME>_URI (S3 backends); fall back to
    file:// + local path (local dev). Matches the iris demo so the
    registry's uri field is always useful even without S3."""
    uri = os.environ.get(f"{env_var}_URI")
    if uri:
        return uri
    local = os.environ.get(env_var)
    if local:
        return f"file://{local}"
    return None


def _api_base() -> str | None:
    api = os.environ.get("HELION_API_URL")
    if api:
        return api
    coord = os.environ.get("HELION_COORDINATOR", "")
    if coord.startswith("http://") or coord.startswith("https://"):
        return coord
    return None


def main() -> int:
    base = _api_base()
    token = os.environ.get("HELION_TOKEN")
    workflow_id = os.environ.get("HELION_WORKFLOW_ID")
    train_name = os.environ.get("HELION_TRAIN_JOB_NAME", "train")
    raw_csv = os.environ.get("HELION_INPUT_RAW_CSV")
    model_file = os.environ.get("HELION_INPUT_MODEL")
    metrics_file = os.environ.get("HELION_INPUT_METRICS")

    required = {
        "HELION_API_URL (or HELION_COORDINATOR as http URL)": base,
        "HELION_TOKEN": token,
        "HELION_WORKFLOW_ID": workflow_id,
        "HELION_INPUT_RAW_CSV": raw_csv,
        "HELION_INPUT_MODEL": model_file,
        "HELION_INPUT_METRICS": metrics_file,
    }
    for name, value in required.items():
        if not value:
            print(f"{name} not set", file=sys.stderr)
            return 1
    assert base and token and workflow_id
    assert raw_csv and model_file and metrics_file

    try:
        source_job = _resolve_train_job_id(base, token, workflow_id, train_name)
    except (RuntimeError, urllib.error.HTTPError) as e:
        print(f"lineage lookup failed: {e}", file=sys.stderr)
        return 1

    with open(metrics_file, "r", encoding="utf-8") as f:
        metrics = json.load(f)

    dataset_uri = _resolve_uri("HELION_INPUT_RAW_CSV") or ""
    model_uri = _resolve_uri("HELION_INPUT_MODEL") or ""

    dataset_body = {
        "name": DATASET_NAME,
        "version": DATASET_VERSION,
        "uri": dataset_uri,
        "size_bytes": os.path.getsize(raw_csv),
        "sha256": _sha256_of(raw_csv),
        "tags": {"source": "openml", "task": "image-classification"},
    }
    model_body = {
        "name": MODEL_NAME,
        "version": MODEL_VERSION,
        "uri": model_uri,
        "framework": FRAMEWORK,
        "source_job_id": source_job,
        "source_dataset": {"name": DATASET_NAME, "version": DATASET_VERSION},
        # Only numeric metrics — accuracy + f1_macro + the shape
        # integers. n_features etc. double as useful audit signal.
        "metrics": {k: v for k, v in metrics.items() if isinstance(v, (int, float))},
        "size_bytes": os.path.getsize(model_file),
        "sha256": _sha256_of(model_file),
        "tags": {"task": "mnist-digit-classification"},
    }

    try:
        _post(base, "/api/datasets", token, dataset_body)
        _post(base, "/api/models", token, model_body)
    except RuntimeError as e:
        print(f"registration failed: {e}", file=sys.stderr)
        return 1

    print(f"registered dataset {DATASET_NAME}/{DATASET_VERSION} + "
          f"model {MODEL_NAME}/{MODEL_VERSION}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
