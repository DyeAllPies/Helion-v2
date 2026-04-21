> **Audience:** users
> **Scope:** Writing ML workflows on Helion — training → registry → serve, built around the iris reference pipeline.
> **Depth:** guide

# Helion v2 — ML Pipelines

User-facing guide to writing ML workflows on Helion: training-to-
serving in one workflow YAML + one registry POST + one service
job. Built around the iris reference pipeline in
[`examples/ml-iris/`](../../examples/ml-iris/).

For the implementation internals behind each feature, see
[ARCHITECTURE.md § ML pipeline](../ARCHITECTURE.md#12-ml-pipeline) and
[COMPONENTS.md § ML subsystems](../COMPONENTS.md#5-ml-subsystems).

---

## Table of contents

1. [What Helion gives you](#1-what-helion-gives-you)
2. [Walkthrough: the iris pipeline](#2-walkthrough-the-iris-pipeline)
3. [Writing your own workflow](#3-writing-your-own-workflow)
4. [Artifact passing with `from:` references](#4-artifact-passing-with-from-references)
5. [Dataset and model registries](#5-dataset-and-model-registries)
6. [Node labels and GPU scheduling](#6-node-labels-and-gpu-scheduling)
7. [Inference services](#7-inference-services)
8. [Dashboard views](#8-dashboard-views)
9. [Security model — token scoping and attestation](#9-security-model--token-scoping-and-attestation)
10. [Troubleshooting](#10-troubleshooting)

---

## 1. What Helion gives you

The minimal-ML slice (features 11–19) turns Helion from a general
job orchestrator into one that can run a training → registry →
serve pipeline end-to-end. Everything rides the existing workflow
DAG + JWT auth; no ML-specific control plane.

| You write | Helion runs | You get |
|---|---|---|
| `workflow.yaml` with `inputs:`/`outputs:`/`from:` | a DAG of jobs | inputs staged on the node workdir, outputs uploaded + attested, downstream jobs get the URI automatically |
| `POST /api/datasets` / `POST /api/models` from a training job | registry write | named, versioned entries with lineage back to the producing job |
| `POST /jobs` with `"service": {...}` | a long-running job with a readiness prober | live upstream URL at `/api/services/{id}` once the `/healthz` probe passes |

Nine features compose the slice (see
[parent index](../planned-features/implemented/10-minimal-ml-pipeline.md)):

- **Artifact store** (11) — S3-compatible blob storage for training
  bytes, addressed by `s3://bucket/jobs/<job-id>/<path>`. Local
  `file://` backend for dev.
- **Job I/O staging** (12) — node-side `Stager` downloads inputs
  into a per-job workdir and uploads declared outputs on exit 0.
- **Workflow artifact passing** (13) — `from: upstream.OUT`
  references resolve at dispatch time to the upstream's concrete
  URI.
- **Node labels and selectors** (14) — `node_selector: {gpu: a100}`
  routes jobs to nodes advertising matching labels.
- **GPU as a first-class resource** (15) — `resources.gpus: 2`
  schedules onto GPU-equipped nodes; runtime injects
  `CUDA_VISIBLE_DEVICES`.
- **Dataset + model registry** (16) — `POST /api/datasets`,
  `POST /api/models`; lineage and metrics travel on the record.
- **Inference jobs** (17) — `service: {port, health_path}` turns
  a job into a long-running HTTP service with readiness probing.
- **Dashboard ML module** (18) — `/ml/datasets`, `/ml/models`,
  `/ml/services`, `/ml/pipelines` + the Pipelines DAG detail view.
- **End-to-end iris demo** (19) — full pipeline you can run locally
  with `docker compose + submit.py workflow.yaml`.

---

## 2. Walkthrough: the iris pipeline

The iris example under [`examples/ml-iris/`](../../examples/ml-iris/)
is the acceptance test for "can a normal person run an ML
pipeline on Helion." It trains a logistic regression classifier on
the 150-row iris dataset and exposes it as a prediction service.
The pipeline is four jobs:

```
 ingest ──► preprocess ──► train ──► register
 RAW_CSV   TRAIN_PARQUET   MODEL     POSTs to /api/datasets,
           TEST_PARQUET    METRICS   /api/models
```

Plus a separate service job:

```
 serve  ──► /healthz  (probed every 5 s)
        ──► /predict  (POST {"features": [[5.1, 3.5, 1.4, 0.2]]})
```

Service jobs live outside the DAG because they never terminate —
putting one inside the workflow would block the DAG from reaching
a terminal state.

### Running it

```bash
# 1. Start the cluster with the iris overlay. The overlay swaps
#    both nodes to Dockerfile.node-python (ships iris pip deps)
#    and injects HELION_API_URL / HELION_TOKEN from the shared
#    state volume.
COMPOSE_PROFILES=analytics,ml docker compose \
  -f docker-compose.yml \
  -f docker-compose.e2e.yml \
  -f docker-compose.iris.yml \
  up -d --build

# 2. Submit. submit.py injects a workflow-scoped `job`-role token
#    (not the root admin token) into each job's env.
export HELION_API_URL=http://127.0.0.1:8080
export HELION_JOB_API_URL=http://coordinator:8080
export HELION_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token)
cd examples/ml-iris
python submit.py workflow.yaml
```

On a warm cache the workflow reaches `completed` in ~10 s. The
full REST acceptance harness at
[`scripts/run-iris-e2e.sh`](../../scripts/run-iris-e2e.sh) asserts:

1. Workflow reaches `completed` with zero `ml.resolve_failed`
   events.
2. `/api/datasets` → `iris/v1`.
3. `/api/models` → `iris-logreg/v1` with `accuracy` ≈ 0.97 and
   `source_job_id=iris-wf-1/train`.
4. `/workflows/iris-wf-1/lineage` returns the DAG with `s3://`
   artifact edges.
5. Manual serve submit → `/api/services/iris-serve-1` ready within
   one probe tick (5 s).
6. `POST /predict` with a setosa row returns `{"predictions":[0]}`.

A Playwright UI-acceptance companion at
[`dashboard/e2e/specs/ml-iris.spec.ts`](../../dashboard/e2e/specs/ml-iris.spec.ts)
proves the same state renders correctly in the dashboard.

---

## 3. Writing your own workflow

An ML workflow on Helion is just a normal Helion workflow with
three extras:

- `inputs:` / `outputs:` artifact bindings on each job
- `from: <upstream>.<OUTPUT>` references for data flow
- a final `register.py`-style step that POSTs to the registry

The iris `workflow.yaml` (see the full file
[here](../../examples/ml-iris/workflow.yaml)) shows the shape:

```yaml
id: iris-wf-1
name: iris-end-to-end
priority: 60

jobs:
  - name: ingest
    command: python
    args: [/app/ml-iris/ingest.py]
    timeout_seconds: 60
    outputs:
      - name: RAW_CSV
        local_path: raw.csv

  - name: preprocess
    command: python
    args: [/app/ml-iris/preprocess.py]
    timeout_seconds: 60
    depends_on: [ingest]
    inputs:
      - name: RAW_CSV
        from: ingest.RAW_CSV
        local_path: raw.csv
    outputs:
      - name: TRAIN_PARQUET
        local_path: train.parquet
      - name: TEST_PARQUET
        local_path: test.parquet

  # ... train + register jobs with `from:` references ...
```

### What the job script sees

Helion sets a per-job working directory (the Stager's workdir) and
exports one env var per binding:

| Declaration | Env var on the job | What the script does |
|---|---|---|
| `outputs: - name: MODEL, local_path: model.joblib` | `HELION_OUTPUT_MODEL` | write the file to that absolute path |
| `inputs: - name: TRAIN, local_path: train.parquet` | `HELION_INPUT_TRAIN` | read the file from that absolute path |

Scripts never have to resolve URIs themselves. The Stager
downloads each declared input into the workdir before `Run()`
returns, and uploads each declared output after exit 0.

### Outputs on failure

If the job exits non-zero (or crashes), the Stager **does not**
upload outputs. The coordinator records the job as `failed`,
cascading failure transitions downstream jobs that declared
`from: <this-job>.<OUT>` into `lost`. A `ml.resolve_failed` event
surfaces on the `/events` feed and the dashboard's Pipelines view.

### DAG validation at submit time

`POST /workflows` rejects:

- **Cycles** — detected via Kahn's algorithm.
- **`from:` references to non-ancestors** — `j2.from: j1.X` without
  `j2.depends_on: [j1]` is a scheduling race and the submit
  validator blocks it.
- **`from:` on plain `/jobs` submits** — artifact passing only
  works inside a workflow context where upstream `ResolvedOutputs`
  are available.
- **`from:` + `on_failure` / `on_complete` conditions** — the
  Stager only uploads on success, so those combinations never
  resolve at runtime; rejecting at submit saves a confusing late
  failure.

---

## 4. Artifact passing with `from:` references

`from: <upstream_job>.<OUTPUT_NAME>` is the ML-workflow syntax for
"when this job dispatches, rewrite this input's URI to whatever
the upstream job uploaded at that output." It splits at the
**last** `.` so workflow job names containing dots still work
(`from: model.v2.TRAIN` = upstream `model.v2`, output `TRAIN`).

```
job A ──outputs:─► HELION_OUTPUT_X=/workdir/x.bin
                   │ on exit 0:
                   │   Stager.Finalize uploads
                   │   → s3://bucket/jobs/wf/A/x.bin
                   │   → Job.ResolvedOutputs[X] = that URI
                   ▼
  coordinator persists the URI on Job A's record
                   │
                   ▼
job B dispatches   │ resolver runs:
                   │   walk B.Inputs for `from:`
                   │   look up upstream's ResolvedOutputs
                   │   rewrite URI + SHA-256 onto B.Inputs[n]
                   ▼
job B ──inputs:──► HELION_INPUT_X=/workdir/x.bin
                   (Stager downloads the URI into the workdir,
                    verifying SHA-256 via artifacts.GetAndVerify)
```

**Security property.** The resolver reads **only** the upstream's
persisted `ResolvedOutputs`, which the coordinator filtered through
`attestOutputs` (scheme + prefix + suffix + declared-name checks)
when the upstream reported its terminal state. A compromised node
cannot inject a cross-job or foreign-scheme URI into a downstream
job's inputs because the URI it would inject never made it onto
the upstream's record in the first place.

**Integrity property.** The upstream's committed SHA-256 travels
with each resolved input. The downstream node's Stager routes
verified downloads through `artifacts.GetAndVerify` — the bytes
are read into a capped buffer, hashed, and only written to the
workdir if the digest matches. Mismatches return
`artifacts.ErrChecksumMismatch` and the stager rolls back the
workdir, failing the downstream job. This catches leaked-S3-creds
tamper and filesystem bit rot without depending on the store's
TLS alone.

---

## 5. Dataset and model registries

Two parallel resources, metadata only — the bytes stay in the
artifact store. Both are JWT-authenticated, rate-limited per
subject (2 rps burst 30), and emit audit + bus events on every
register / delete.

### Dataset

```bash
curl -X POST -H "Authorization: Bearer $HELION_TOKEN" \
  -H "Content-Type: application/json" \
  http://coordinator:8080/api/datasets -d '{
  "name":    "iris",
  "version": "v1",
  "uri":     "s3://helion/jobs/iris-wf-1/ingest/raw.csv",
  "size_bytes": 2734,
  "sha256":  "f13ffa8fdd56...",
  "tags":    {"source": "uci", "task": "classification"}
}'
```

### Model (lineage-bearing)

```bash
curl -X POST -H "Authorization: Bearer $HELION_TOKEN" \
  -H "Content-Type: application/json" \
  http://coordinator:8080/api/models -d '{
  "name":         "iris-logreg",
  "version":      "v1",
  "uri":          "s3://helion/jobs/iris-wf-1/train/model.joblib",
  "framework":    "sklearn",
  "source_job_id":    "iris-wf-1/train",
  "source_dataset":   {"name": "iris", "version": "v1"},
  "metrics":          {"accuracy": 0.967, "f1_macro": 0.966},
  "size_bytes": 991,
  "sha256":  "8fdc7e3e9a...",
  "tags":    {"task": "iris-classification"}
}'
```

### Name / version rules

- **Name** — lowercase alnum + `-._`. DNS-label charset so entries
  are shell-safe, URL-safe, and BadgerDB-key-safe without escaping.
- **Version** — broader charset to accept `v1.0.0`, `2026-04-18`,
  `git-abc1234`, or SemVer `+build`. Length capped at 64 bytes,
  control bytes rejected.
- **Immutable once registered.** Re-registering the same
  `(name, version)` returns 409. Version bumps create a new
  entry so lineage points at the exact training run.

### Metrics caps

`metrics` is `map[string]float64` with ≤ 64 entries, 63-byte keys.
NaN and ±Inf are rejected — they don't round-trip through
`encoding/json`.

### Lineage semantics

`Model.source_dataset` is all-or-nothing — partial pointers
(name without version) are rejected at submit so the audit record
is never half-formed. `source_job_id` is recorded verbatim; we
trust the training job to stamp the right pointer. (Inferring
lineage from artifact URIs is out of scope.)

### Reading lineage back

`GET /workflows/{id}/lineage` joins the workflow spec against the
JobStore + the model registry (via `ListBySourceJob`) and returns
a DAG shape:

```json
{
  "workflow_id": "iris-wf-1",
  "name": "iris-end-to-end",
  "status": "completed",
  "jobs": [
    {
      "name": "train",
      "job_id": "iris-wf-1/train",
      "status": "completed",
      "outputs": [
        {"name": "MODEL",   "uri": "s3://.../model.joblib",  "size": 991, "sha256": "8fdc7..."},
        {"name": "METRICS", "uri": "s3://.../metrics.json", "size": 237, "sha256": "a45e..."}
      ],
      "models_produced": [{"name": "iris-logreg", "version": "v1"}]
    }
  ],
  "artifact_edges": [
    {"from_job": "preprocess", "from_output": "TRAIN_PARQUET", "to_job": "train", "to_input": "TRAIN_PARQUET"}
  ]
}
```

Powers the dashboard's Pipelines DAG view (feature 18).

---

## 6. Node labels and GPU scheduling

Two mechanisms for "this job needs a particular kind of node":

### Labels and selectors

Nodes auto-detect `os`, `arch`, and — if `nvidia-smi` succeeds —
`gpu=<model>`. Operators can add labels via `HELION_LABEL_<KEY>`
env vars on the node agent (`HELION_LABEL_ZONE=us-east`,
`HELION_LABEL_GPU=none` to hide a physical GPU).

Jobs declare a `node_selector`:

```yaml
- name: train
  command: python
  args: [/app/ml-iris/train.py]
  node_selector:
    gpu: a100
    cuda: "12.4"
```

Exact-equality match only (no `In`, no glob). If no node matches,
the dispatch loop emits a debounced `job.unschedulable` event with
a triage reason:

- `no_healthy_node` — wait, retry (transient cluster state).
- `no_matching_label` — add a matching node or change the
  selector.
- `all_matching_unhealthy` — restart the stale matching nodes.

### GPU requests

**Status:** code-complete on paper, **unverified on real GPU
hardware.** Every unit test exercises either the CPU-path stub
or a simulated `nvidia-smi`; the GitHub Actions runners have no
GPU, so no CI run has ever touched a real device. A
build-tag-gated harness under `tests/gpu/` exists for a local
developer with CUDA to sanity-check the real path
(`go test -tags=gpu ./tests/gpu/...`). Treat the design below as
"wired correctly" rather than "validated end-to-end."

`resources.gpus` is a first-class scheduler dimension:

```yaml
- name: train
  command: python
  args: [/app/train.py]
  resources:
    gpus: 2
```

The scheduler filters out nodes whose `TotalGpus < gpus_requested`.
On the winning node the runtime:

1. Claims N whole-device indices from the per-node
   `GPUAllocator` (lowest-index-first, no MIG).
2. Exports `CUDA_VISIBLE_DEVICES=<indices>` into the subprocess
   env.
3. Releases the indices on exit (success, failure, timeout,
   cancel).

CPU jobs (`gpus: 0`) on GPU-equipped nodes get
`CUDA_VISIBLE_DEVICES=""` explicitly — without this hide, a
malicious "CPU" job could supply its own
`CUDA_VISIBLE_DEVICES="0,1,2,3"` and access devices pinned to a
concurrent GPU job. CPU-only nodes (`TotalGpus=0`) leave the env
var untouched for legacy CPU workloads.

Request cap: `resources.gpus <= 16` (commercially-available hosts
ship with 8 today; 16 is headroom).

---

## 7. Inference services

A service job is a normal job with a `service:` block:

```json
{
  "id": "iris-serve-1",
  "command": "uvicorn",
  "args": ["serve:app", "--host", "0.0.0.0", "--port", "8000"],
  "env": {"PYTHONPATH": "/app/ml-iris"},
  "inputs": [{"name": "MODEL", "uri": "s3://.../model.joblib", "local_path": "model.joblib"}],
  "service": {
    "port": 8000,
    "health_path": "/healthz",
    "health_initial_ms": 2000
  }
}
```

What changes vs. a batch job:

- **No timeout enforcement.** The runtime skips the configured
  timeout; the subprocess runs until `Cancel` (or process self-
  exit).
- **Readiness prober.** After `health_initial_ms`, the node polls
  `http://127.0.0.1:<port><health_path>` every 5 s. State flips
  (unknown → ready, ready ↔ unhealthy) emit edge-triggered
  `ReportServiceEvent` RPCs to the coordinator.
- **Upstream URL exposed.** The coordinator records the
  `(node_address, port)` mapping on the first `ready` event; read
  it via `GET /api/services/{id}`:
  ```json
  {
    "job_id": "iris-serve-1",
    "node_id": "node1",
    "upstream_url": "http://node1:8000/healthz",
    "ready": true,
    ...
  }
  ```
- **Registry cleanup on terminal.** When the service job reaches
  a terminal state, `JobCompletionCallback` removes the registry
  entry so clients don't follow stale URIs.

### Out of scope

Routing, load balancing across replicas, blue/green, autoscaling —
all out of the slice. A user who wants those puts an Nginx (or
Envoy) in front and targets the `upstream_url` fields as the pool.
The point is "you can train a model and serve it without leaving
Helion"; once that works, the rest is a standard HTTP deployment
problem.

### Validator rules

- **Port range.** 1024–65535. Privileged ports (< 1024) are
  rejected at submit — the node agent runs as a non-root
  DaemonSet in production and binding below 1024 would fail
  anyway; catching at submit gives a crisp 400 instead of a
  spawn-time crash.
- **Health path.** Must start with `/`, ≤ 256 bytes, no
  whitespace or NUL. Defense-in-depth against URL injection into
  the prober's probe URL.
- **Grace period.** `health_initial_ms` capped at 30 min — a
  misconfigured grace would delay failure detection beyond any
  operational limit.

---

## 8. Dashboard views

Four lazy-loaded Angular routes under `/ml/*`:

| Route | Reads | Shows |
|---|---|---|
| `/ml/datasets` | `GET /api/datasets` | Paginated list with URI, size, tags; register-via-form modal → `POST /api/datasets`; delete confirmation gate |
| `/ml/models` | `GET /api/models` | Lineage links (source_job_id + source_dataset), metric pills, framework chip |
| `/ml/services` | `GET /api/services` every 5 s | READY / UNHEALTHY chip, upstream URL, back-link to the service job |
| `/ml/pipelines` + `/ml/pipelines/:id` | `GET /workflows` + `GET /workflows/{id}/lineage` | DAG view with dependency + artifact edges rendered via mermaid, per-job cards with status chips and `models_produced` links |

The Playwright UI spec at
[`dashboard/e2e/specs/ml-iris.spec.ts`](../../dashboard/e2e/specs/ml-iris.spec.ts)
asserts each view renders the iris pipeline correctly.
[`docs/e2e-iris-run.mp4`](e2e-iris-run.mp4) is a 24-second paced
walkthrough of all four views.

---

## 9. Security model — token scoping and attestation

### Workflow-scoped tokens

`submit.py` mints a workflow-scoped `job`-role token via
`POST /admin/tokens` and injects THAT into each job's env
(`HELION_TOKEN=<scoped>`), not the operator's root admin token.
Properties:

- **`role: job`** — `adminMiddleware` rejects it at 403 for
  `/admin/*` endpoints, so a leaked token from a compromised
  in-workflow script cannot mint more tokens or revoke nodes.
- **`subject: workflow:<id>`** — the audit trail stamps the
  workflow ID directly in the actor column; compliance queries
  group by workflow without JTI joins.
- **`ttl_hours: 1`** — auto-expires shortly after the pipeline
  finishes (typically < 2 min end-to-end), bounding the damage
  window if the env is captured from a crash log.
- **Revocable** — operator can `DELETE /admin/tokens/{jti}` to
  invalidate mid-run.

Residual surface: the `job` token can still call the non-admin
authenticated REST surface (submit more jobs, register more
datasets/models, read workflow state). Resource-scoped
permissions (per-endpoint allowlists) are a future enhancement.

### Output attestation (coordinator-side)

When a node reports a job's terminal state with
`ResolvedOutputs[]`, the coordinator's `attestOutputs` gate
applies four checks before persisting them on the Job record:

1. **Scheme allowlist** — URI must start with `s3://` or
   `file://`.
2. **Prefix match** — `s3://<bucket>/jobs/<job-id>/...` (the
   bucket comes from the coordinator's `HELION_ARTIFACTS_S3_BUCKET`;
   `<job-id>` must match the reporting job). Blocks a compromised
   node from claiming bytes live in another job's prefix.
3. **Suffix match** — the URI's tail must equal the job's
   declared `local_path`. Blocks a node from renaming outputs at
   report time.
4. **Declared-name check** — every reported `Name` must appear in
   `Job.Outputs` (the submit-time declaration). Blocks a
   compromised node from inventing new output names that the
   downstream resolver would then accept.

The resolver (feature 13) runs AFTER attestation, so every URI
resolved onto a downstream job's `Inputs` has already passed
these four checks. A compromised node cannot inject cross-job
or foreign-scheme URIs into another job's inputs.

### Transport integrity

SHA-256 propagation is defense-in-depth on top of the hybrid PQ
channel ([SECURITY.md § 3](../SECURITY.md#3-post-quantum-cryptography)).
The digest travels with the URI; the downstream node's Stager
routes verified downloads through `artifacts.GetAndVerify`. This
catches three scenarios the channel crypto doesn't directly
address: leaked store credentials (attacker swaps bytes under a
key without changing the coordinator's committed digest), bit-rot
between upload and download, and store-side MITM (belt-and-braces
over the store's TLS).

---

## 10. Troubleshooting

### "`ml.resolve_failed` event but the workflow completed"

The resolver fires one event per `from:` reference that couldn't
be resolved at dispatch. If the downstream job later succeeded
(because its retry policy or the upstream's recovery re-populated
`ResolvedOutputs`), the workflow can still reach `completed`
overall. The event is diagnostic, not terminal — it tells you
the dispatch loop saw a transient miss.

### "Registered model URI is `file://` on a local cluster"

Documented gap: the Stager doesn't currently surface the assigned
S3 URI to the running job, so `register.py` falls back to
`file://<local-path>`. The job's `ResolvedOutputs` (and therefore
the lineage endpoint) still carry the correct `s3://` URI — only
the registry's record is `file://`. To work around, resolve the
model URI from lineage:

```bash
curl -sf -H "Authorization: Bearer $TOKEN" \
  $API_URL/workflows/iris-wf-1/lineage \
  | python -c "import sys,json; d=json.load(sys.stdin); \
      [print(o['uri']) for j in d['jobs'] if j['name']=='train' \
       for o in j.get('outputs',[]) if o['name']=='MODEL']"
```

### "Service job dispatches but `/api/services/{id}` returns 404"

Probe hasn't fired a `ready` event yet. Check:

- `GET /jobs/{id}` — is the job `running`? Services stay in
  `dispatching` until first ready, then remain there until Cancel
  (no separate running-service status on the job record).
- `GET /events` — look for `service.ready` for your job_id. If
  you see `service.unhealthy` the app is up but the health path
  is 4xx/5xx.
- Health path works inside the container — `docker exec helion-node-1
  wget -qO- http://127.0.0.1:<port><health_path>`.

### "GPU job stays pending with `no_matching_label`"

The `job.unschedulable` event's `reason` field distinguishes:

- `no_matching_label` — no node in the cluster advertises the
  requested labels at all. Fix: `POST /nodes` a node with the
  label, or drop the selector.
- `all_matching_unhealthy` — nodes have the labels but none are
  healthy. Fix: restart the stale nodes.
- `no_healthy_node` — zero healthy nodes in the cluster. Wait or
  investigate the heartbeat stream.

### "`submit.py` fails minting the scoped token"

Pre-feature-19 coordinator builds don't know the `job` role.
`submit.py` falls back to the root admin token with a stderr
warning. Upgrade the coordinator or live with the downgraded
security posture.

### "Workflow is stuck in `running` after register completes"

Make sure the terminal job (usually `register`) exits 0. If the
registry POST returned 409 (already registered on a re-run),
`register.py` treats it as idempotent success, but any other
HTTP error raises and fails the job.

---

## Reference

- Parent slice: [feature 10 — ML pipeline umbrella](../planned-features/implemented/10-minimal-ml-pipeline.md)
- Implementation docs for each feature:
  [11](../planned-features/implemented/11-ml-artifact-store.md) /
  [12](../planned-features/implemented/12-ml-job-io-staging.md) /
  [13](../planned-features/implemented/13-ml-workflow-artifact-passing.md) /
  [14](../planned-features/implemented/14-ml-node-labels-and-selectors.md) /
  [15](../planned-features/implemented/15-ml-gpu-first-class-resource.md) /
  [16](../planned-features/implemented/16-ml-dataset-model-registry.md) /
  [17](../planned-features/implemented/17-ml-inference-jobs.md) /
  [18](../planned-features/implemented/18-ml-dashboard-module.md) /
  [19](../planned-features/implemented/19-ml-end-to-end-demo.md)
- Example code: [`examples/ml-iris/`](../../examples/ml-iris/)
- Videos: [full e2e run](../e2e-full-run.mp4) · [iris walkthrough](../e2e-iris-run.mp4)
- Security: [SECURITY.md](../SECURITY.md) · [jwt.md](../operators/jwt.md)
- Architecture: [ARCHITECTURE.md § ML pipeline](../ARCHITECTURE.md#12-ml-pipeline)
- Components: [COMPONENTS.md § ML subsystems](../COMPONENTS.md#5-ml-subsystems)
