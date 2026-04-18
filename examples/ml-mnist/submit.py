"""submit.py — workflow submitter for the MNIST-784 demo.

Mirrors examples/ml-iris/submit.py exactly: converts the YAML
workflow spec to the coordinator's JSON format, mints a
workflow-scoped `job`-role token via POST /admin/tokens (so the
register step's env doesn't carry the root admin token), injects
HELION_API_URL + HELION_TOKEN into every job's env, and POSTs
to /workflows.

Usage
-----
    HELION_API_URL=http://127.0.0.1:8080            # submitter's own use
    HELION_JOB_API_URL=http://coordinator:8080      # injected into job env
    HELION_TOKEN=<bearer>                           # operator's admin token
        python submit.py workflow.yaml

Flags
-----
    --serve         After the workflow succeeds (polled until
                    terminal), submit serve.py as a separate
                    service job. NOTE: serve submission today
                    needs the concrete model URI; the `registry://`
                    scheme is not implemented. See README's
                    "Serving — manual step" for the copy-paste.
    --poll-timeout  Seconds to wait for workflow termination
                    (default 600 — MNIST fetch + training can take
                    up to a minute on a cold cache).
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from typing import Any

try:
    import yaml
except ImportError:
    print("PyYAML is required — `pip install pyyaml`", file=sys.stderr)
    sys.exit(1)


SERVE_JOB_ID = "mnist-serve-1"
SERVE_PORT = 8000


def _auth_headers(token: str) -> dict:
    return {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {token}",
    }


def _post(base: str, path: str, token: str, body: "dict[str, Any]") -> "dict[str, Any]":
    url = base.rstrip("/") + path
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST",
                                 headers=_auth_headers(token))
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def _get(base: str, path: str, token: str) -> "dict[str, Any]":
    url = base.rstrip("/") + path
    req = urllib.request.Request(url, method="GET",
                                 headers={"Authorization": f"Bearer {token}"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def _read_yaml(path: str) -> "dict[str, Any]":
    with open(path, "r", encoding="utf-8") as f:
        spec = yaml.safe_load(f)
    if not isinstance(spec, dict):
        raise ValueError(f"{path}: top-level must be a mapping")
    return spec


def _mint_workflow_token(api_url: str, admin_token: str, wf_id: str,
                         ttl_hours: int = 1) -> str:
    """Mint a short-lived `job`-role token scoped to this workflow.
    adminMiddleware rejects `job` role at 403 for /admin/*, so a
    leaked env cannot escalate. Falls back to the admin token if
    /admin/tokens rejects (older coordinator without the `job` role
    or missing tokenManager)."""
    body = {
        "subject": f"workflow:{wf_id}",
        "role": "job",
        "ttl_hours": ttl_hours,
    }
    try:
        resp = _post(api_url, "/admin/tokens", admin_token, body)
        tok = resp.get("token")
        if isinstance(tok, str) and tok:
            return tok
        print("warning: /admin/tokens response missing token; "
              "falling back to admin token", file=sys.stderr)
    except urllib.error.HTTPError as e:
        msg = e.read().decode("utf-8", errors="replace")
        print(f"warning: /admin/tokens {e.code}: {msg} — "
              f"falling back to admin token", file=sys.stderr)
    return admin_token


def _inject_api_env(spec: "dict[str, Any]", api_url: str, token: str) -> None:
    """Inject HELION_API_URL + HELION_TOKEN into every workflow
    job's env so in-workflow scripts can call back to the
    coordinator. Go runtime does not forward node-agent env to
    subprocess jobs; the submitter owns credential plumbing."""
    jobs = spec.get("jobs", [])
    for job in jobs:
        env = job.setdefault("env", {})
        env.setdefault("HELION_API_URL", api_url)
        env.setdefault("HELION_TOKEN", token)


def _poll_until_terminal(base: str, token: str, wf_id: str, timeout_s: int = 600) -> str:
    deadline = time.time() + timeout_s
    last_status = ""
    while time.time() < deadline:
        wf = _get(base, f"/workflows/{wf_id}", token)
        status = str(wf.get("status", ""))
        if status != last_status:
            print(f"  workflow status: {status}")
            last_status = status
        if status in {"completed", "failed", "cancelled"}:
            return status
        time.sleep(2)
    raise TimeoutError(f"workflow {wf_id} still {last_status!r} after {timeout_s}s")


def _serve_job_body(model_name: str, model_version: str) -> "dict[str, Any]":
    """Build a SubmitRequest for the serve job using the
    registry:// model URI scheme. NOTE: that scheme is not yet
    implemented by the coordinator; until it lands, submit the
    serve job manually with the concrete URI — see README."""
    return {
        "id": SERVE_JOB_ID,
        "command": "uvicorn",
        "args": ["serve:app", "--host", "0.0.0.0", "--port", str(SERVE_PORT)],
        "env": {"PYTHONPATH": "/app/ml-mnist"},
        "inputs": [
            {
                "name": "MODEL",
                "uri": f"registry://models/{model_name}/{model_version}",
                "local_path": "model.joblib",
            },
        ],
        "service": {
            "port": SERVE_PORT,
            "health_path": "/healthz",
            "health_initial_ms": 2000,
        },
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("workflow_yaml", help="path to workflow.yaml")
    ap.add_argument("--serve", action="store_true",
                    help="after the workflow completes, submit serve.py as a Service")
    ap.add_argument("--poll-timeout", type=int, default=600,
                    help="seconds to wait for the workflow to terminate (default 600)")
    args = ap.parse_args()

    base = os.environ.get("HELION_API_URL") or os.environ.get("HELION_COORDINATOR")
    token = os.environ.get("HELION_TOKEN")
    if not base or not token:
        print("HELION_API_URL (or HELION_COORDINATOR) and HELION_TOKEN must both be set",
              file=sys.stderr)
        return 1

    spec = _read_yaml(args.workflow_yaml)

    job_api_url = os.environ.get("HELION_JOB_API_URL") or base

    raw_id = spec.get("id")
    wf_id = str(raw_id) if isinstance(raw_id, str) and raw_id else "unnamed-workflow"
    job_token = _mint_workflow_token(base, token, wf_id)
    _inject_api_env(spec, job_api_url, job_token)

    try:
        print(f"submitting workflow {spec.get('id', '<unnamed>')}…")
        resp = _post(base, "/workflows", token, spec)
    except urllib.error.HTTPError as e:
        print(f"submit failed: {e.code} {e.read().decode('utf-8', errors='replace')}",
              file=sys.stderr)
        return 1
    resp_id = resp.get("id")
    if isinstance(resp_id, str) and resp_id:
        wf_id = resp_id
    print(f"submitted: id={wf_id}")

    if not args.serve:
        return 0

    try:
        status = _poll_until_terminal(base, token, wf_id, args.poll_timeout)
    except TimeoutError as e:
        print(str(e), file=sys.stderr)
        return 1
    if status != "completed":
        print(f"workflow terminated {status!r}; not starting serve job",
              file=sys.stderr)
        return 1

    print("submitting serve job…")
    try:
        _post(base, "/jobs", token, _serve_job_body("mnist-logreg", "v1"))
        print(f"serve job submitted: id={SERVE_JOB_ID}")
        print(f"  upstream will be visible at /api/services/{SERVE_JOB_ID} once ready")
    except urllib.error.HTTPError as e:
        print(f"serve submit failed: {e.code} {e.read().decode('utf-8', errors='replace')}",
              file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
