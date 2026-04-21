"""submit.py — minimal workflow submitter for the iris demo.

The coordinator accepts workflow submissions as JSON over
`POST /workflows`; `workflow.yaml` is just a human-authored source
that this script converts. Kept in the example (rather than
shipped as a top-level binary) so the example is self-contained:
one folder, one-command run.

Usage
-----
    HELION_COORDINATOR=http://127.0.0.1:8080 \
    HELION_TOKEN=<bearer> \
        python submit.py workflow.yaml

Flags
-----
    --serve    After the workflow succeeds (polled until terminal),
               submit serve.py as a separate service job so the
               trained model is reachable via /api/services/{id}.
               Default: false (workflow-only submission).

Exit codes
----------
    0  workflow submitted (+ service submitted if --serve)
    1  usage error, HTTP error, or workflow reached a failed state
       while being polled
"""
from __future__ import annotations

import argparse
import json
import os
import ssl
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


SERVE_JOB_ID = "iris-serve-1"
SERVE_PORT = 8000


def _ssl_context() -> ssl.SSLContext | None:
    """Build a strict SSL context pinned to HELION_CA_FILE.

    Feature 39 flipped the coordinator REST listener to TLS-on;
    submit.py now runs against an HTTPS URL in the E2E and CI
    setups. HELION_CA_FILE points at the coordinator's self-signed
    CA (typically state/ca.pem exported from the coordinator
    container). Returning None when HELION_CA_FILE is unset leaves
    urllib with the system trust store — appropriate for local dev
    against a publicly trusted CA, or for the legacy plain-HTTP
    escape hatch (HELION_REST_TLS=off).
    """
    ca_file = os.environ.get("HELION_CA_FILE", "").strip()
    if not ca_file or not os.path.exists(ca_file):
        return None
    ctx = ssl.create_default_context(cafile=ca_file)
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    return ctx


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
    with urllib.request.urlopen(req, timeout=30, context=_ssl_context()) as resp:
        return json.loads(resp.read())


def _get(base: str, path: str, token: str) -> "dict[str, Any]":
    url = base.rstrip("/") + path
    req = urllib.request.Request(url, method="GET",
                                 headers={"Authorization": f"Bearer {token}"})
    with urllib.request.urlopen(req, timeout=30, context=_ssl_context()) as resp:
        return json.loads(resp.read())


def _read_yaml(path: str) -> "dict[str, Any]":
    with open(path, "r", encoding="utf-8") as f:
        spec = yaml.safe_load(f)
    if not isinstance(spec, dict):
        raise ValueError(f"{path}: top-level must be a mapping")
    return spec


def _mint_workflow_token(api_url: str, admin_token: str, wf_id: str,
                         ttl_hours: int = 1) -> str:
    """Mint a short-lived admin-role token scoped to this workflow.

    Posted by the submitter against POST /admin/tokens using the
    operator's admin token. The returned token:

      - Has `role: admin` — register.py needs to GET /workflows/{id}
        (read lineage) and POST /api/datasets + /api/models (write
        registry entries). Feature 37's authz policy confines job-role
        tokens to reading JOB resources only (see internal/authz/authz.go
        KindJob rule), so a job-role token returns 403 on all three
        calls. Admin-role is the simplest role the registry handlers
        accept for writes today; narrower scope (e.g. a per-workflow
        "creator" role that can only POST to /api/datasets and
        /api/models for entries whose source_job_id belongs to the
        scoping workflow) is the proper long-term fix but out of scope
        for a demo submitter.
      - Has `subject: workflow:<id>` — audit-log entries stamp the
        workflow ID directly in the actor column, so compliance
        queries can group by workflow without joining on JTI. A leaked
        token's damage is still narrowed by the subject tag: operators
        can grep audit for workflow-scoped actions and revoke quickly.
      - Expires in `ttl_hours` hours — bounds the damage window if a
        job's env is ever dumped. One hour is enough headroom for the
        iris pipeline (typically <2 min end-to-end) plus any operator-
        driven retries.

    Falls back to the root admin token if the cluster's coordinator
    rejects /admin/tokens (older build without tokenManager wired,
    etc.). The fallback logs a warning so the operator sees the
    downgrade.
    """
    body = {
        "subject": f"workflow:{wf_id}",
        "role": "admin",
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


def _inject_api_env(spec: dict, api_url: str, token: str) -> None:
    """Inject HELION_API_URL + HELION_TOKEN + HELION_CA_FILE into
    every workflow job's env block so in-workflow scripts
    (register.py) can POST back to the coordinator over TLS. The
    Go runtime does not forward node-agent env to subprocess jobs —
    only the job spec's declared env reaches them — so the
    submitter owns credential plumbing.

    HELION_CA_FILE is stamped to `/app/state/ca.pem` unconditionally:
    the e2e + iris overlays mount the state volume across
    coordinator + nodes so that path is valid inside every job
    container. On local dev with a publicly trusted CA the file
    doesn't exist and register.py's `_ssl_context()` gracefully
    falls back to the system trust store.

    The injected token is typically a workflow-scoped `job`-role
    token minted via _mint_workflow_token, NOT the operator's root
    admin token. Scoping properties:

      - role=job → adminMiddleware returns 403 for /admin/*
      - subject=workflow:<id> → audit trail names the workflow
      - ttl=1h → bounded damage window if the env is exposed

    The remaining blast radius is "anything authMiddleware-only":
    the token can register more datasets/models, submit jobs, read
    workflow state. Resource-scoped permissions (e.g. restrict to
    specific name/version writes) are a future enhancement;
    documenting the residual surface here so a future reader
    knows what the token CAN still do.
    """
    jobs = spec.get("jobs", [])
    for job in jobs:
        env = job.setdefault("env", {})
        env.setdefault("HELION_API_URL", api_url)
        env.setdefault("HELION_TOKEN", token)
        env.setdefault("HELION_CA_FILE", "/app/state/ca.pem")


def _poll_until_terminal(base: str, token: str, wf_id: str, timeout_s: int = 600) -> str:
    """Poll GET /workflows/{id} every 2s until Status leaves the
    running set. Returns the terminal status string so the caller
    can decide whether to proceed with the service submit."""
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


def _serve_job_body(model_name: str, model_version: str) -> dict:
    """Build the SubmitRequest for the serve job. The model must
    already be registered — register.py runs at the end of the
    workflow, so by the time _serve is called this is true."""
    return {
        "id": SERVE_JOB_ID,
        "command": "uvicorn",
        "args": ["serve:app", "--host", "0.0.0.0", "--port", str(SERVE_PORT)],
        # timeout_seconds intentionally omitted — the service spec
        # makes Helion skip timeout enforcement anyway.
        "inputs": [
            {
                "name": "MODEL",
                # Resolve by registry lookup: the URI comes from
                # the most recent register of this (name, version).
                # The node agent's stager understands the
                # `registry://` scheme as "look this up in the
                # model registry and substitute the stored URI"…
                #
                # NOTE: `registry://` resolution is NOT implemented
                # as of this commit. Until it lands, the serve job
                # needs to be submitted by hand with the model's
                # concrete URI substituted in. See the README's
                # "Serving — manual step" for the copy-paste.
                "uri": f"registry://models/{model_name}/{model_version}",
                "local_path": "model.joblib",
            },
        ],
        "service": {
            "port": SERVE_PORT,
            "health_path": "/healthz",
            "health_initial_ms": 2000,
        },
        # Pin to the Go runtime: the iris serve job runs `uvicorn`, which
        # needs Python on the node. The mnist iris overlay also wires a
        # Rust-runtime node for MNIST parallel-train walk-throughs; that
        # node has no Python and the Rust runtime env_clear()'s PATH, so
        # a serve job landing there fails with exec error. The workflow
        # jobs (preprocess/train/eval/register) pin the same selector —
        # see examples/ml-iris/workflow.yaml line 28.
        "node_selector": {"runtime": "go"},
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("workflow_yaml", help="path to workflow.yaml")
    ap.add_argument("--serve", action="store_true",
                    help="after the workflow completes, submit serve.py as a Service")
    ap.add_argument("--poll-timeout", type=int, default=600,
                    help="seconds to wait for the workflow to terminate (default 600)")
    args = ap.parse_args()

    # HELION_API_URL wins if set; otherwise accept HELION_COORDINATOR
    # when it's an http(s):// URL. submit.py runs from the operator's
    # laptop where HELION_COORDINATOR is conventionally the HTTP URL,
    # so the fallback keeps the documented one-env-var workflow.
    base = os.environ.get("HELION_API_URL") or os.environ.get("HELION_COORDINATOR")
    token = os.environ.get("HELION_TOKEN")
    if not base or not token:
        print("HELION_API_URL (or HELION_COORDINATOR) and HELION_TOKEN must both be set",
              file=sys.stderr)
        return 1

    spec = _read_yaml(args.workflow_yaml)

    # The URL that jobs running INSIDE the cluster use to call back
    # to the coordinator. Defaults to HELION_API_URL (host-visible)
    # but for docker-compose demos the submitter runs on the host
    # and the jobs run in-cluster, so the container-visible URL
    # (e.g. http://coordinator:8080) needs to be different.
    job_api_url = os.environ.get("HELION_JOB_API_URL") or base

    # Security: mint a workflow-scoped `job`-role token for the jobs
    # rather than leaking the operator's root admin token into each
    # env block. adminMiddleware rejects the scoped token at 403 for
    # /admin/*, and the 1-hour TTL bounds the damage window if a
    # job's env is ever captured from a crash log or audit entry.
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

    # --serve: wait for the workflow to complete, then submit the
    # service job. The workflow's final step (register) makes the
    # model available in the registry, which the serve job depends
    # on via `registry://` lookup.
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
        # NOTE: registry:// scheme is not yet implemented. See
        # _serve_job_body for the details; this call will fail
        # until that lands. Kept wired so once the scheme is
        # supported, --serve just works.
        _post(base, "/jobs", token, _serve_job_body("iris-logreg", "v1"))
        print(f"serve job submitted: id={SERVE_JOB_ID}")
        print(f"  upstream will be visible at /api/services/{SERVE_JOB_ID} once ready")
    except urllib.error.HTTPError as e:
        print(f"serve submit failed: {e.code} {e.read().decode('utf-8', errors='replace')}",
              file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
