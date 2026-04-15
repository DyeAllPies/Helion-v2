"""register.py — Step 4 of the iris end-to-end demo.

Registers the training artifacts against the coordinator's dataset
and model registries so:
  - The iris dataset is discoverable via GET /api/datasets.
  - The trained model is discoverable via GET /api/models with
    lineage back to this workflow's training job and dataset.
  - The serve step (run separately, not in the DAG) can fetch the
    model by name + version.

This step does *not* upload bytes — Helion's Stager already did
that when the training job exited 0. What we register is the
metadata: names, versions, URIs (which the node's Stager assigned),
metrics (from the metrics JSON), and the source-job-id lineage
pointer that makes `GET /workflows/{id}/lineage` light up the DAG.

Environment
-----------
HELION_COORDINATOR           Coordinator REST base URL (e.g. http://coordinator:8080).
HELION_TOKEN                 JWT bearer for the authenticated API calls.
HELION_WORKFLOW_ID           The parent workflow's ID. Set in the workflow
                             YAML's `env:` block for this job; register.py
                             uses it to look up the training job's runtime
                             job_id (for the model's lineage pointer) via
                             GET /workflows/{id}.
HELION_TRAIN_JOB_NAME        The workflow-local name of the training job.
                             Defaults to "train"; override if your workflow
                             uses a different name.
HELION_INPUT_RAW_CSV         Staged raw iris CSV — used to grab the uploaded
                             URI for the dataset registry entry.
HELION_INPUT_MODEL           Staged model pickle — URI goes into the model
                             entry.
HELION_INPUT_METRICS         Staged metrics JSON — parsed and attached.

Exit codes
----------
0   both registry entries written (or already existed — 409 treated as idempotent)
1   missing env var, HTTP error other than 409, or JSON parse failure
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
import urllib.error
import urllib.request

DATASET_NAME = "iris"
DATASET_VERSION = "v1"
MODEL_NAME = "iris-logreg"
MODEL_VERSION = "v1"
FRAMEWORK = "sklearn"


def _sha256_of(path: str) -> str:
    """Lower-hex SHA-256 of the file at path. The registry stores
    whatever digest is supplied; we compute a real one so the
    dashboard's Model detail shows a non-empty SHA-256 chip."""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(64 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def _get_json(base: str, path: str, token: str) -> dict:
    """GET JSON from the coordinator. Raises on non-200 so the
    caller can treat it as a hard failure (no implicit nil handling
    — an unreadable workflow record means the register step cannot
    build the lineage pointer and the right behaviour is to fail
    loud so the operator sees it)."""
    url = base.rstrip("/") + path
    req = urllib.request.Request(
        url, method="GET",
        headers={"Authorization": f"Bearer {token}"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def _resolve_train_job_id(base: str, token: str, workflow_id: str, train_name: str) -> str:
    """Call GET /workflows/{id}, scan the jobs list, and return the
    runtime job_id of the job whose workflow-local name matches
    train_name. Raises if not found so the caller fails fast rather
    than quietly registering a model with empty lineage."""
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
    """POST JSON to the coordinator's REST API. Treats 409 as
    idempotent (someone already registered this version — fine for
    a re-run)."""
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
    """The Stager writes the uploaded URI into the env on `file://`
    backends (local dev). On S3 backends the URI is known at
    submit time by the coordinator but not surfaced to the job.
    Fall back to the local staged path when the URI isn't in the
    env — the registry still records a useful pointer."""
    uri = os.environ.get(f"{env_var}_URI")
    if uri:
        return uri
    local = os.environ.get(env_var)
    if local:
        return f"file://{local}"
    return None


def main() -> int:
    base = os.environ.get("HELION_COORDINATOR")
    token = os.environ.get("HELION_TOKEN")
    workflow_id = os.environ.get("HELION_WORKFLOW_ID")
    train_name = os.environ.get("HELION_TRAIN_JOB_NAME", "train")
    raw_csv = os.environ.get("HELION_INPUT_RAW_CSV")
    model_file = os.environ.get("HELION_INPUT_MODEL")
    metrics_file = os.environ.get("HELION_INPUT_METRICS")

    required = {
        "HELION_COORDINATOR": base,
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
    # After the required-check every value is non-empty; the
    # asserts narrow the types for the static checker and would
    # only trip in the unreachable "non-empty but falsy" branch.
    assert base and token and workflow_id
    assert raw_csv and model_file and metrics_file

    # Resolve the training job's runtime ID. Fail loud if missing —
    # a model without a lineage pointer is silently broken on the
    # dashboard's Pipelines DAG view.
    try:
        source_job = _resolve_train_job_id(base, token, workflow_id, train_name)
    except (RuntimeError, urllib.error.HTTPError) as e:
        print(f"lineage lookup failed: {e}", file=sys.stderr)
        return 1

    # Parse the metrics JSON written by train.py.
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
        "tags": {"source": "uci", "task": "classification"},
    }
    model_body = {
        "name": MODEL_NAME,
        "version": MODEL_VERSION,
        "uri": model_uri,
        "framework": FRAMEWORK,
        "source_job_id": source_job,
        "source_dataset": {"name": DATASET_NAME, "version": DATASET_VERSION},
        "metrics": {k: v for k, v in metrics.items() if isinstance(v, (int, float))},
        "size_bytes": os.path.getsize(model_file),
        "sha256": _sha256_of(model_file),
        "tags": {"task": "iris-classification"},
    }

    try:
        _post(base, "/api/datasets", token, dataset_body)
        _post(base, "/api/models", token, model_body)
    except RuntimeError as e:
        print(f"registration failed: {e}", file=sys.stderr)
        return 1

    print(f"registered dataset {DATASET_NAME}/{DATASET_VERSION} + model {MODEL_NAME}/{MODEL_VERSION}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
