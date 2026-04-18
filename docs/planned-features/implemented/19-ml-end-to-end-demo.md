# Feature: ML End-to-End Demo

**Priority:** P1
**Status:** Done. Acceptance run completed 2026-04-18 against a
clean Docker-Compose cluster (coordinator + 2 Python-capable nodes
+ MinIO + Postgres). All six checks on the README's acceptance list
pass: workflow reaches `completed` with zero `ml.resolve_failed`
events; registry shows iris/v1 + iris-logreg/v1 with accuracy
0.9667; lineage endpoint returns the DAG with s3:// artifact edges;
serve submit brings the service to `ready` within one probe tick;
`POST /predict` returns `{"predictions":[0]}` for the first
setosa feature row.
**Affected files:** `examples/ml-iris/` (scripts, workflow, submitter,
requirements, README), `Dockerfile.node-python` (new — Python-capable
node image baking the iris deps), `docker-compose.iris.yml` (new —
compose overlay wiring the Python node image + credential
injection), `internal/nodeserver/server.go` (service-job background
dispatch fix — Dispatch RPC now ACKs immediately and runs the
service runtime in a detached goroutine, matching feature 17's
long-running-service design).
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## End-to-end demo workflow

Ship a worked example under `examples/ml-iris/`:

```
examples/ml-iris/
├── workflow.yaml          # 4-job DAG: ingest → preprocess → train → register
├── ingest.py              # downloads iris CSV → RAW_CSV output
├── preprocess.py          # RAW_CSV → TRAIN_PARQUET + TEST_PARQUET
├── train.py               # parquet splits → model.joblib + metrics.json
├── register.py            # POSTs to /api/datasets + /api/models with lineage
├── serve.py               # FastAPI app loading model.joblib, exposed via Service
├── submit.py              # YAML-to-JSON converter + /workflows client
├── requirements.txt       # pandas, scikit-learn, fastapi, uvicorn, pyyaml…
└── README.md              # step-by-step run guide + known gaps
```

The example is the acceptance test for "can a normal person run an
ML pipeline on Helion." If this works on a clean checkout with one
`docker compose up` + `python examples/ml-iris/submit.py
examples/ml-iris/workflow.yaml`, the feature is done.

### Deviations from the original one-line spec

- **sklearn, not PyTorch.** Iris is a 150-row toy dataset. Swapping
  `torch` for `scikit-learn + joblib` cuts the runtime dep chain
  from ~800 MiB to ~60 MiB without changing what the pipeline
  exercises on the Helion side. Model artifact is `model.joblib`.
- **Serve submitted separately.** `serve.py` is a Helion service
  job — never terminates, would block the workflow DAG from
  completing. `submit.py --serve` submits it in a second call
  after the workflow reaches `completed`. The README walks through
  both the automated `--serve` flow and a manual `curl` fallback.
- **`submit.py` ships in the example.** The coordinator takes
  workflows as JSON over `POST /workflows`; the spec's
  `workflow.yaml` is the human-authored source. `submit.py`
  converts (one dep: PyYAML) rather than adding a workflow
  subcommand to `helion-run`, which is out of scope for this
  slice.

## Follow-up 1: workflow-scoped tokens (2026-04-18)

The original `submit.py` injected the operator's root admin token
into every job's env block. The feature-19 security plan called
this a "no new trust boundary" tradeoff (jobs already execute
operator-supplied commands), but the blast radius if a job's env
was ever captured — from a crash log, audit entry, or a
compromised node — included cluster-wide admin powers (mint
tokens, revoke nodes).

Closed:

- **`job` role added** to `api.validRoles` in
  `internal/api/middleware.go`. `adminMiddleware` already rejects
  non-admin tokens; adding the role is a one-line change that
  unlocks a narrower credential class without restructuring
  permissions.
- **`submit.py` mints a workflow-scoped token** via
  `POST /admin/tokens` with `{subject: workflow:<id>, role: job,
  ttl_hours: 1}` and injects that token in place of the root one.
  Falls back to the root admin token with a stderr warning if
  `/admin/tokens` is unavailable (older cluster build, or
  `tokenManager` not wired).
- **Pre-existing gap fixed**: `POST /admin/nodes/{id}/revoke`
  used only `authMiddleware`, so any authenticated caller — node
  role, or the new `job` role — could revoke any node. Now
  properly guarded by `adminMiddleware` consistent with the rest
  of the `/admin/*` surface.
- **Regression tests pinned**:
  `handlers_admin_test.go:TestIssueToken_JobRole_Accepted` and
  `TestJobRoleToken_CanNotMintMoreTokens` /
  `TestJobRoleToken_CanNotRevokeNodes` alarm for a regression
  that either removed the role or relaxed the middleware guards.

Residual surface documented in `examples/ml-iris/README.md §
Token scoping`: the `job` role can still call the non-admin
authenticated REST surface (submit jobs, register
datasets/models, read workflow state). Resource-scoped
permissions (per-endpoint allowlists keyed on role) close this
but require a broader middleware design; tracked as a future
enhancement, not a feature-19 blocker.

## Follow-up 2: CI-runnable acceptance harness (2026-04-18)

The 2026-04-18 acceptance run was manual (a human at a
terminal); feature 19's promise is "can a normal person run an
ML pipeline on Helion" but "a normal person" includes CI.

Closed:

- **`scripts/run-iris-e2e.sh`** — single-command harness that
  brings up the cluster, submits the workflow, and asserts every
  one of the six acceptance checkpoints with explicit pass/fail
  per line. Tears down on every exit path (EXIT trap) with logs
  dumped on failure for post-mortem.
- **`.github/workflows/ci.yml` gains an `e2e-iris` job** that
  depends on `build` + `test-dashboard` and runs the harness on
  every push. Uses the same ubuntu-latest runner + compose
  profile pattern as the Playwright `e2e` job.
- **Rust-runtime variant deferred**: `Dockerfile.node-rust` uses
  cgroup v2 + seccomp which are Linux-only (Docker Desktop on
  Windows/macOS fails at startup with cgroup mount errors). A
  Rust-iris harness would gate on `uname -s = Linux`; not
  blocking feature 19's Done status since the Go runtime is
  the default and covers the acceptance surface.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| None (read-only artifact reads; registry POSTs ride the standard JWT-authenticated API) | — | — |

The iris pipeline introduces no new trust boundary. Every RPC the
example scripts issue (`/workflows`, `/api/datasets`, `/api/models`,
`/api/services`, `/jobs`) rides the existing JWT bearer auth + rate
limiters. `register.py` reads `HELION_TOKEN` from the job's env;
the operator is responsible for not leaking that into logs — the
Go runtime already redacts `HELION_TOKEN` from emitted audit
records (see `../../SECURITY.md` § 6).

## What the acceptance run surfaced (fixed inline)

The author-side caveat on this file was "acceptance run pending";
the 2026-04-18 run found four real gaps the scripts-in-isolation
could not have exercised. All four are closed here:

1. **Node image now ships Python + iris deps.** `Dockerfile.node`
   builds a minimal alpine+Go binary with no Python — fine for
   production but unusable for the iris demo. Added
   `Dockerfile.node-python` (new), a Python 3.11 image with the
   iris `requirements.txt` pre-installed and the pipeline scripts
   baked under `/app/ml-iris/` (referenced by absolute path from
   `workflow.yaml` so per-job workdirs don't hide them). The
   UID/GID (100:101) is aligned with `Dockerfile.coordinator` so
   the shared `/app/state` named volume is readable by both —
   a mismatch earlier caused the coordinator to fail its first
   BadgerDB write with permission-denied.
2. **Compose overlay for the demo.** `docker-compose.iris.yml`
   (new) layers on top of `docker-compose.yml` +
   `docker-compose.e2e.yml` to use the Python node image and
   inject credentials into the node's env via a small entrypoint
   wrapper that reads `/app/state/root-token` after the
   coordinator has written it.
3. **Service-job dispatch must not block the RPC.** Feature 17's
   spec declares service jobs "long-running, terminal only on
   explicit stop or process exit", but the node's Dispatch
   handler blocked on `rt.Run` for the whole lifetime of the
   service — the coordinator's 10-second Dispatch dial deadline
   killed every service launch with DeadlineExceeded. Split the
   handler in `internal/nodeserver/server.go`: service jobs now
   fork the runtime + prober into a detached-context goroutine
   and ACK the Dispatch RPC immediately; batch jobs keep the
   old sync-dispatch semantics unchanged. Regression test
   pinned in `server_test.go:TestDispatch_ServiceJob_ReturnsAckImmediately`.
4. **Iris scripts needed a host/cluster URL split.** The iris
   Python scripts originally read `HELION_COORDINATOR` as an
   HTTP base URL, but the node binary treats `HELION_COORDINATOR`
   as a gRPC host:port — the same env var cannot hold both.
   `register.py` and `submit.py` now prefer `HELION_API_URL`
   (falling back to `HELION_COORDINATOR` only when it starts with
   `http(s)://` so host-side submits still work with the single
   env var the README documents). `submit.py` also injects
   `HELION_API_URL` + `HELION_TOKEN` into each job's env block
   at submit time (a new `HELION_JOB_API_URL` override specifies
   the in-cluster URL, since submitter runs on the host and jobs
   run in-cluster). Documented in the updated README.

## Known gaps — documented in the README, not blocking Done

Reproduced from `examples/ml-iris/README.md` § "Known gaps"; each
is a non-blocking limitation rather than an acceptance-blocker.

1. **`registry://` input-URI scheme is not implemented.** The
   serve job's `submit.py --serve` path uses
   `registry://models/iris-logreg/v1` as the model input URI; the
   coordinator's dispatch-time resolver doesn't know this scheme.
   Workaround: manual submit with the concrete URI (README's
   "Serving — manual step" copy-paste). The acceptance run used
   this path. Proper fix is a small resolver addition —
   candidate for a numbered deferred item.
2. **Iris CSV mirror availability.** `ingest.py` pulls from the
   scikit-learn GitHub raw URL. Baking the CSV into the example
   folder would be more robust but loses the network-backed
   ingest path's realism.
3. **Artifact URIs in `register.py` fall back to `file://` on
   local backends.** The Stager doesn't surface the assigned
   S3 URI to the running job today. `_resolve_uri` prefers a
   `HELION_INPUT_<NAME>_URI` env var that the Stager could start
   setting; falls back to `file://<local-path>` for now. The
   registered iris/v1 + iris-logreg/v1 URIs came back as
   `file://` on the 2026-04-18 run; the workflow-lineage
   endpoint still returned the correct `s3://` URIs for the
   per-job Stager uploads (those are read from the job record's
   ResolvedOutputs directly, not from the register step's env).

## Acceptance run — 2026-04-18 result

All six checks green against a clean checkout:

1. ✅ `docker compose … up` succeeds; coordinator prints token;
   both nodes register and report healthy on `/nodes`.
2. ✅ `submit.py workflow.yaml` returns HTTP 201 with
   `id=iris-wf-1`.
3. ✅ Workflow reaches `completed` (all four jobs green); zero
   `ml.resolve_failed` events on the analytics feed.
4. ✅ `/api/datasets` → iris/v1; `/api/models` → iris-logreg/v1
   with `accuracy=0.9667`, `f1_macro=0.9666`, and
   `source_job_id=iris-wf-1/train`.
5. ✅ `/workflows/iris-wf-1/lineage` returns the DAG with
   artifact edges carrying `s3://helion/jobs/iris-wf-1/…` URIs.
6. ✅ Manual serve submit lands on node1; `/api/services` shows
   `iris-serve-1` as `ready` within one probe tick; `POST /predict`
   with `[[5.1,3.5,1.4,0.2]]` returns `{"predictions":[0]}`
   (class 0 = setosa, correct).
