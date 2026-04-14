# Feature: Minimal ML Pipeline

**Priority:** P1
**Status:** In progress (steps 1–2 implemented; 3–10 pending)
**Affected files:**
`internal/api/types.go`,
`internal/cluster/registry_node.go`,
`internal/cluster/policy_resource.go`,
`internal/cluster/dag.go`,
`internal/cluster/persistence_jobs.go`,
`internal/runtime/` (new artifact staging),
`runtime-rust/` (file staging on the executor),
`internal/artifacts/` (new package),
`internal/registry/` (new package — dataset & model metadata),
`internal/api/handlers_artifacts.go` (new),
`internal/api/handlers_registry.go` (new),
`cmd/helion-coordinator/main.go`,
`dashboard/src/app/features/ml/` (new module),
`docker-compose.yml` (MinIO service).

## Problem

Helion v2 is a generic command executor. It can already orchestrate a DAG of
arbitrary processes, retry them, prioritise them, schedule them by CPU/memory,
audit them, and stream their logs. What it **cannot** do today is run a
recognisable ML workflow end-to-end without the user inventing a parallel
control plane outside of Helion:

- A typical ML pipeline is `ingest → preprocess → train → evaluate → register →
  serve`. Each stage produces *data* (a parquet shard, a tokeniser, a model
  checkpoint, a metrics blob) that the next stage *consumes*. Helion's job
  spec carries no notion of inputs or outputs — only `command`, `args`, `env`.
- Workflow DAG edges express *ordering* (`depends_on`), but not *data flow*.
  Job B cannot read what job A produced; the user has to bake S3 paths into
  the command line by hand and hope the convention holds.
- Nodes report CPU and memory only. There is no way to say "this node has a
  GPU" or "this node has CUDA 12.4 + PyTorch 2.5 installed", and no way to
  ask the scheduler for one. ML jobs need targeted placement.
- There is no artifact store. BadgerDB is a key-value store; ML artifacts are
  multi-megabyte binary blobs. Stuffing them into Badger is wrong; logging
  the S3 URI in audit-event detail is hand-rolled.
- There is no dataset or model registry. Tracking which model was trained
  from which dataset, on which run, with which metrics, is the basic lineage
  story that lets ML platforms exist. Without it, the "what's in production"
  question is unanswerable.
- There is no inference path. Even after a model exists, Helion has no way to
  expose it as a long-running serving job that downstream callers can hit.

The goal of this feature is the **smallest** set of changes that lets a user
submit a workflow that ingests a dataset, trains a model, registers it, and
serves it for inference — using only Helion primitives, not a parallel system.

## Design principle: orchestration, not framework

Helion stays framework-agnostic. We do **not** ship a Python SDK that wraps
PyTorch, a hyperparameter sweeper, a distributed training launcher, an
experiment tracker, or model-format converters. Those belong in user code
inside the job's `command`.

What we add is the **plumbing** that ML jobs need from any orchestrator:
artifact references, inter-job data passing, node selectors, a dataset/model
registry, and an inference job mode. Anything beyond that is deferred.

## Current state

Reusing the survey from this branch:

- Job spec (`SubmitRequest`, [internal/api/types.go:53-63](../../internal/api/types.go#L53-L63)) has
  `id`, `command`, `args`, `env`, `timeout_seconds`, retry policy, priority,
  resource block. **No** `inputs`, `outputs`, `working_dir`, `node_selector`.
- DAG validation ([internal/cluster/dag.go:39-71](../../internal/cluster/dag.go#L39-L71)) links jobs by
  name only — there is no edge metadata for artifact passing.
- Node entry ([internal/cluster/registry_node.go:41-58](../../internal/cluster/registry_node.go#L41-L58)) tracks CPU
  millicores, total memory, max slots, health. **No** labels, tags, or
  accelerator inventory.
- Resource policy ([internal/cluster/policy_resource.go](../../internal/cluster/policy_resource.go))
  bin-packs by CPU + memory + slots. **No** GPU dimension.
- Persistence is BadgerDB JSON ([internal/cluster/persistence_jobs.go:20-29](../../internal/cluster/persistence_jobs.go#L20-L29)).
  Suitable for metadata, unsuitable for blobs.
- Feature 08 already lists GPU scheduling as deferred — this feature
  promotes it from "deferred" to "in scope, minimal cut."

---

## Step 1 — Artifact store abstraction

New package: `internal/artifacts/`.

```go
// internal/artifacts/store.go

// URI is an opaque artifact reference, e.g. "s3://helion/run-42/model.pt"
// or "file:///var/lib/helion/artifacts/run-42/model.pt".
type URI string

type Store interface {
    Put(ctx context.Context, key string, r io.Reader, size int64) (URI, error)
    Get(ctx context.Context, uri URI) (io.ReadCloser, error)
    Stat(ctx context.Context, uri URI) (Metadata, error)
    Delete(ctx context.Context, uri URI) error
}

type Metadata struct {
    Size      int64
    SHA256    string
    UpdatedAt time.Time
}
```

Two implementations:

- `LocalStore` — backed by a directory on the coordinator's host. Default for
  dev / single-node deployments. No new dependencies.
- `S3Store` — backed by any S3-compatible service (MinIO, AWS S3, GCS S3
  gateway). Uses `aws-sdk-go-v2`. The `docker-compose.yml` ships a MinIO
  service so the dev path matches prod.

Selection is by env var: `HELION_ARTIFACTS_BACKEND={local|s3}`,
`HELION_ARTIFACTS_PATH=/var/lib/helion/artifacts` (local) or
`HELION_ARTIFACTS_S3_ENDPOINT=...` + bucket + creds (S3).

The store is **not** in the coordinator's hot path — it is called by the API
when a user uploads an artifact, by nodes when staging inputs/outputs, and by
the registry when computing checksums.

---

## Step 2 — Job spec: inputs, outputs, working directory

Extend `SubmitRequest`:

```go
type SubmitRequest struct {
    // ... existing fields ...

    WorkingDir   string             `json:"working_dir,omitempty"`
    Inputs       []ArtifactBinding  `json:"inputs,omitempty"`
    Outputs      []ArtifactBinding  `json:"outputs,omitempty"`
    NodeSelector map[string]string  `json:"node_selector,omitempty"`
}

type ArtifactBinding struct {
    Name      string `json:"name"`        // env var name exposed to the job
    URI       URI    `json:"uri,omitempty"`   // for inputs: where to pull from
    LocalPath string `json:"local_path"`  // path inside working_dir
    // For outputs the URI is assigned by the runtime after upload.
}
```

Semantics:

- **Inputs**: before the command runs, the node downloads each `URI` to
  `WorkingDir/LocalPath` and exports `HELION_INPUT_<NAME>=<absolute path>`.
- **Outputs**: after the command exits 0, the node uploads each
  `WorkingDir/LocalPath` to the artifact store (key derived from
  `<job_id>/<name>`) and records the resulting URI in the job's terminal
  event payload.
- **WorkingDir**: if empty, the node mints a per-job temp dir under
  `HELION_WORK_ROOT` (default `$TMPDIR/helion-jobs`). Cleaned up on success
  unless `HELION_KEEP_WORKDIR=1`.

The runtime (`runtime-rust/` and the Go in-process runner) is the only place
that needs to learn about this. The coordinator stores the binding metadata
verbatim and forwards it on dispatch.

---

## Step 3 — Inter-job artifact passing in workflows

Extend the workflow DAG node to express *which output of A becomes which
input of B*:

```yaml
jobs:
  - name: preprocess
    command: python preprocess.py
    outputs:
      - name: TRAIN_PARQUET
        local_path: out/train.parquet

  - name: train
    command: python train.py
    depends_on: [preprocess]
    inputs:
      - name: TRAIN_DATA
        from: preprocess.TRAIN_PARQUET   # NEW: artifact reference
        local_path: in/train.parquet
    outputs:
      - name: MODEL
        local_path: out/model.pt
```

The DAG validator (`internal/cluster/dag.go`) gains a check: any
`from: <job>.<name>` reference must resolve to an `outputs[].name` declared
on a job that this job `depends_on` (transitively or directly).

At dispatch time, when job B becomes ready, the workflow engine resolves
each `from` ref to the URI recorded by job A's terminal event and rewrites
the binding's `URI` field before sending it to the node.

This is **the** core ML feature: it turns a DAG of commands into a DAG of
data transformations. Everything else in this spec is in service of this.

---

## Step 4 — Node labels and selectors

Extend node registration:

```go
type nodeEntry struct {
    // ... existing fields ...
    Labels map[string]string // e.g. {"gpu": "a100", "cuda": "12.4", "zone": "us-east"}
}
```

Labels are reported by the node binary at registration time, sourced from:

- Environment variables prefixed with `HELION_LABEL_` (operator-set).
- Auto-detected: `gpu=<model>` if `nvidia-smi` succeeds, `os=<linux|darwin|windows>`,
  `arch=<amd64|arm64>`. Auto-detection is best-effort and additive.

The scheduler gains a `node_selector` filter applied **before** the
resource policy runs — selectors narrow the candidate set, then bin-packing
chooses among survivors. Selector semantics are exact-match equality only
(no `In`, no `NotIn`, no glob) — this is the minimal cut.

If no node matches, the job stays pending and emits a
`job.unschedulable` event with the unsatisfied selector. (We do **not**
add a "wait forever vs. fail fast" policy here; it surfaces in feedback
naturally and can be a P2 follow-up.)

---

## Step 5 — GPU as a first-class resource

Promoted from feature 08's deferred list. Minimal scope:

- `nodeEntry.Resources` gains `GPUs int` (whole-GPU count, no fractional
  sharing, no MIG slicing).
- `SubmitRequest.Resources` gains `GPUs int` (request count).
- `ResourceAwarePolicy` adds GPU as a third bin-packing dimension. A node
  with `GPUs=0` is invisible to jobs requesting `GPUs>0`.
- The node runtime sets `CUDA_VISIBLE_DEVICES=<comma-separated indices>`
  in the job's env, derived from a per-node GPU allocator that tracks
  which device indices are in use by which job.

What we do **not** do here: GPU memory tracking, MIG, multi-host
collective scheduling, topology awareness. Those are explicit P3 in the
deferred list and stay there.

---

## Step 6 — Dataset and model registries

New package: `internal/registry/`. Two parallel resources, both backed by
BadgerDB (metadata only — the bytes live in the artifact store).

```go
type Dataset struct {
    Name      string            // unique
    Version   string            // semver-ish, user-supplied
    URI       artifacts.URI
    SizeBytes int64
    SHA256    string
    Tags      map[string]string // free-form
    CreatedAt time.Time
    CreatedBy string            // JWT subject
}

type Model struct {
    Name        string
    Version     string
    URI         artifacts.URI
    Framework   string            // "pytorch" | "onnx" | "tensorflow" | "other"
    SourceJobID string            // training job that produced it
    SourceDataset struct {        // lineage pointer
        Name    string
        Version string
    }
    Metrics  map[string]float64   // user-reported eval metrics
    Tags     map[string]string
    CreatedAt time.Time
    CreatedBy string
}
```

REST API:

```
POST   /api/datasets                 — register a new dataset (multipart upload)
GET    /api/datasets                 — list with filter on tags
GET    /api/datasets/:name/:version  — fetch metadata + signed URI
DELETE /api/datasets/:name/:version  — delete (artifact + metadata)

POST   /api/models                   — register a new model
GET    /api/models                   — list
GET    /api/models/:name/:version    — fetch metadata + URI
GET    /api/models/:name/latest      — convenience: highest semver
DELETE /api/models/:name/:version
```

All endpoints JWT-authenticated, audited (`dataset.registered`,
`model.registered`, etc. — emitted on the event bus so the analytics
pipeline picks them up automatically).

Lineage is recorded but not enforced — a model's `SourceJobID` is whatever
the registering call says it is. We trust the training job to register its
own outputs and stamp the lineage; we do not attempt to *infer* lineage
from artifact URIs.

---

## Step 7 — Inference jobs

The minimum viable serving story is a long-running job with a port mapping
and a readiness probe. We add **one** thing to the job spec:

```go
type SubmitRequest struct {
    // ...
    Service *ServiceSpec `json:"service,omitempty"`
}

type ServiceSpec struct {
    Port            int    `json:"port"`              // port the job binds to
    HealthPath      string `json:"health_path"`       // e.g. "/healthz"
    HealthInitialMS int    `json:"health_initial_ms"` // grace period before first probe
}
```

When `Service` is set:

- The job is treated as long-running (no timeout enforcement, terminal
  state is only reached on explicit stop or process exit).
- After `HealthInitialMS`, the node polls `http://127.0.0.1:<Port><HealthPath>`
  every 5s and reports `service.ready` / `service.unhealthy` events.
- The coordinator records the `node_address:port` mapping and exposes a
  read-only lookup: `GET /api/services/:job_id` returns the upstream URL.

Routing, load balancing across replicas, blue/green, autoscaling — **all
out of scope**. A user who wants those puts an Nginx in front. The point
of this step is "you can train a model and serve it without leaving Helion."

---

## Step 8 — Dashboard: ML module

New lazy-loaded Angular module at `dashboard/src/app/features/ml/`:

- **Datasets** view — list, tag filter, register-via-upload modal, delete.
- **Models** view — list, lineage column (links to source job + dataset),
  metrics column.
- **Pipelines** view — workflow list filtered to those that produced a
  registered model, with a small DAG visualisation showing artifact flow
  on edges (not just dependency arrows).
- **Services** view — currently-serving models, upstream URL, last health
  status.

Reuses the existing auth-guard, JWT interceptor, error banner, and date
range patterns from the analytics module.

---

## Step 9 — End-to-end demo workflow

Ship a worked example under `examples/ml-iris/`:

```
examples/ml-iris/
├── workflow.yaml          # 4-job DAG: ingest → preprocess → train → register
├── ingest.py              # downloads iris CSV → outputs raw.csv
├── preprocess.py          # raw.csv → train.parquet + test.parquet
├── train.py               # train.parquet → model.pt + metrics.json
├── register.py            # POSTs to /api/models with metrics + lineage
└── serve.py               # FastAPI app loading model.pt, exposed via Service
```

The example is the acceptance test for "can a normal person run an ML
pipeline on Helion." If this works on a clean checkout with one
`docker compose up` + `helion-cli submit examples/ml-iris/workflow.yaml`,
the feature is done.

---

## Step 10 — Documentation

- `docs/ARCHITECTURE.md` — add ML pipeline section + dual-store note
  (artifacts on object store, metadata in Badger, analytics in PG).
- `docs/COMPONENTS.md` — artifact store, registry, service mode.
- `docs/persistence.md` — clarify three-tier storage:
  Badger (operational metadata) / object store (artifact bytes) /
  PostgreSQL (analytics).
- `docs/dashboard.md` — ML module pages.
- New `docs/ml-pipelines.md` — user-facing "how to write an ML workflow"
  guide built around the iris example.

---

## Implementation order

| Step | Description                                      | Depends on   | Effort | Status |
|------|--------------------------------------------------|--------------|--------|--------|
| 1    | Artifact store abstraction (local + S3)          | —            | Medium | Done   |
| 2    | Job spec: inputs/outputs/working_dir + runtime staging | 1      | Medium | Done   |
| 3    | Workflow artifact passing (`from: job.output`)   | 2            | Medium | Pending |
| 4    | Node labels + node_selector scheduling           | —            | Small  | Pending |
| 5    | GPU as a scheduling resource                     | 4            | Medium | Pending |
| 6    | Dataset + model registries (API + storage)       | 1            | Medium | Pending |
| 7    | Inference jobs (Service spec + readiness)        | 2            | Small  | Pending |
| 8    | Dashboard ML module                              | 6, 7         | Medium | Pending |
| 9    | Iris end-to-end example                          | 2, 3, 6, 7   | Small  | Pending |
| 10   | Documentation                                    | All          | Small  | Pending |

Roughly: steps 1-3 unlock data-flow workflows, 4-5 unlock GPU scheduling,
6-7 unlock the registry-and-serve loop, 8-10 are surface polish + the
acceptance test.

---

## Implementation notes

### Step 1 — artifact store (done)

Landed in [`internal/artifacts/`](../../internal/artifacts/) with two
backends behind a single `Store` interface:

- `LocalStore` — filesystem root, `file://` URIs, atomic writes
  (tempfile → fsync → rename), streaming SHA-256 in `Stat`, context
  cancellation on every I/O chunk.
- `S3Store` — S3-compatible, `s3://<bucket>/<key>` URIs, via
  `github.com/minio/minio-go/v7`. Interface-level `s3Client` abstraction
  so unit tests run against an in-memory fake; a live integration test
  gates on `MINIO_TEST_ENDPOINT` for real-MinIO round-trips.
- `Open(Config)` factory driven by `HELION_ARTIFACTS_BACKEND` +
  backend-specific env vars.

Store-layer hardening (API-layer hardening deferred to the handler
step): key length cap (`MaxKeyLength = 1024`, matches S3 ceiling),
rejection of NUL + ASCII control bytes, rejection of absolute paths,
backslashes, Windows drive letters, traversal via `..`, URIs that
escape the store root, URIs that name a different bucket, and wrong
schemes. Tests: **35 pass + 1 skipped live integration**
([`local_test.go`](../../internal/artifacts/local_test.go),
[`s3_test.go`](../../internal/artifacts/s3_test.go),
[`config_test.go`](../../internal/artifacts/config_test.go)).

### Step 2 — job spec + runtime staging (done)

Data model:

- [`proto/node.proto`](../../proto/node.proto) — new `ArtifactBinding`
  message; `DispatchRequest` extended with `working_dir`, `inputs`,
  `outputs`, `node_selector`. Protos regenerated.
- [`internal/proto/coordinatorpb/types.go`](../../internal/proto/coordinatorpb/types.go) —
  `cpb.Job` gets the same four fields plus a new `cpb.ArtifactBinding`
  struct. JSON-serialised, so existing BadgerDB rows deserialize
  forward-compatibly.
- [`internal/api/types.go`](../../internal/api/types.go) — `SubmitRequest`
  gets the same fields plus `ArtifactBindingRequest`.

API validation
([`internal/api/handlers_jobs.go`](../../internal/api/handlers_jobs.go)):

- Binding name must match `[A-Z_][A-Z0-9_]*` so `HELION_INPUT_<NAME>`
  is always a safe env var.
- `local_path`: relative, ≤ 512 bytes, no NUL, no `\`, no `/`-prefix,
  no empty / `.` / `..` segments.
- `node_selector`: Kubernetes-shaped sizes (key ≤ 63 bytes, value ≤ 253,
  ≤ 32 entries), no `=` or NUL in keys.
- Per-direction cap: 64 bindings. URI: ≤ 2048 bytes, no NUL. Inputs
  **must** supply a URI; outputs **must not** (the runtime assigns it).
  Duplicate names rejected per direction.

Staging ([`internal/staging/`](../../internal/staging/)):

- `Stager.Prepare` — mints a 0o700 workdir under
  `HELION_WORK_ROOT` (or `$TMPDIR/helion-jobs`), downloads each input
  with `O_EXCL` + 0o600, enforces `MaxInputDownloadBytes`
  (2 GiB, tunable) via `io.LimitedReader`, pre-creates output parent
  dirs, exports `HELION_INPUT_<NAME>` and `HELION_OUTPUT_<NAME>`.
  Rolls back the workdir on any failure.
- `Stager.Finalize` — uploads outputs **only on success** under
  `jobs/<job_id>/<local_path>` keys, refuses symlinks + irregular files
  + oversize outputs via `Lstat`, always cleans up the workdir unless
  `HELION_KEEP_WORKDIR=1`.

Wiring:

- [`internal/cluster/node_dispatcher.go`](../../internal/cluster/node_dispatcher.go) —
  forwards bindings on the wire.
- [`internal/runtime/runtime.go`](../../internal/runtime/runtime.go) +
  [`go_runtime.go`](../../internal/runtime/go_runtime.go) — `RunRequest.WorkingDir`
  sets `cmd.Dir`.
- [`internal/nodeserver/server.go`](../../internal/nodeserver/server.go) —
  calls `Prepare → rt.Run → Finalize`. Stager-less nodes **reject**
  jobs that carry bindings rather than running blind. Env merge gives
  stager values precedence so a caller cannot shadow `HELION_INPUT_*`.
- [`cmd/helion-node/main.go`](../../cmd/helion-node/main.go) — opt-in:
  stager wires up only when `HELION_ARTIFACTS_BACKEND` is set.

Security matrix applied in this step:

| Concern | Mitigation |
|---|---|
| Path traversal in `local_path` | API validator + `safeJoin` prefix-check |
| Workdir escape via symlink outputs | `Lstat` + regular-file gate |
| Disk fill via oversized download | `MaxInputDownloadBytes` LimitedReader |
| Artifact-store fill via oversized upload | `MaxOutputUploadBytes` pre-flight Lstat |
| Cross-job key collisions | `jobs/<job_id>/` prefix |
| Cross-job workdir collisions | Per-job `Mkdir`; fail-loud on reuse |
| Env shadowing attack | Stager wins in merge |
| Partial workdir on failure | Prepare rollback + guaranteed Finalize cleanup |
| Shell-unsafe artifact names | Submit-time regex check |
| Absolute `local_path` tricks on Windows | Reject `/`, `\`, drive-letter prefix |
| Submit-bomb via huge binding list | 64-per-direction cap |
| Unconfigured nodes running ML jobs blind | Refuse dispatch when `stager == nil` |

Deferred to later steps (HTTP handler layer when artifact upload API
lands in step 6): per-subject rate limit on artifact upload, audit
events (`artifact.put`, `staging.prepared`, `staging.uploaded`),
`http.MaxBytesReader` on the upload handler, signed URLs for node→S3
direct transfer, resolved output URIs surfaced in terminal event
payloads (marked `TODO(step 3)` in the server).

Tests: **47 new, all passing**
([`handlers_jobs_step2_test.go`](../../internal/api/handlers_jobs_step2_test.go),
[`staging_test.go`](../../internal/staging/staging_test.go),
[`go_runtime_workdir_test.go`](../../internal/runtime/go_runtime_workdir_test.go)).

### Parity gap: Rust runtime

The Rust runtime ([`runtime-rust/`](../../runtime-rust/)) still honours
only the legacy `RunRequest` fields. Rust-backed nodes refuse jobs
carrying bindings (because they are not wired to a Stager yet). Parity
is tracked as a follow-up before step 3 can promise workflow-level
artifact passing on Rust nodes.

## Open questions

- **Artifact GC.** When a workflow run is deleted, do we delete its
  artifacts? Auto-delete risks losing a registered model whose URI still
  points there; manual-only risks unbounded growth. Proposed default:
  artifacts referenced by a registered dataset/model are pinned;
  unreferenced run artifacts are GC'd after a TTL (default 30 days).
  Worth confirming before implementation.
- **Multipart upload limits.** S3 multipart upload for >5 GiB models is
  standard but adds API surface. For minimal cut, cap dataset/model
  uploads at 5 GiB and document the limit; revisit if real workloads
  exceed it.
- **Service replicas.** A `replicas: N` field on `ServiceSpec` would be
  natural and cheap (submit N copies, route via Nginx upstream). Held out
  of the minimal cut to avoid the load-balancer rabbit hole — confirm
  this is the right call for the first release.
- **Python SDK.** A thin `helion` Python package (submit, log artifact,
  register model from inside a job) would dramatically improve the iris
  example's ergonomics. Held out for the same reason — the REST API works
  without it. Worth a follow-up feature spec if this lands.

## What this does NOT include

- Distributed training (Horovod, NCCL collective scheduling, gang scheduling).
- Hyperparameter sweep orchestration (Optuna integration, parallel trials with
  early stopping).
- Experiment tracking (MLflow-style runs/params/metrics UI beyond the
  metrics field on `Model`).
- Model format conversion (ONNX export, TorchScript, TensorRT).
- Feature stores, vector databases, embedding pipelines.
- Notebook execution (Papermill / Jupyter kernels as a job type).
- Auto-scaling inference, traffic splitting, A/B testing, canary deploys.
- Data versioning beyond a string `version` field (no DVC, no LakeFS).

These are all reasonable follow-on features. None of them are required for
"a user can train and serve a model on Helion."
