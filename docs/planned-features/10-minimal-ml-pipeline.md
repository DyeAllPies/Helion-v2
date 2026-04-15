# Feature: Minimal ML Pipeline

**Priority:** P1
**Status:** In progress (steps 1–8 implemented; step 9 written but awaiting acceptance run; step 10 pending)
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

This feature is the umbrella slice for turning Helion v2 from a generic
command executor into a minimal ML orchestrator. Each step is now a
standalone feature spec; see the [Steps](#steps) table below.

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

Each of the 10 steps that delivers the above is now its own feature file,
enumerated below.

---

## Steps

| # | Feature file | One-line summary | Status |
|---|-------------|------------------|--------|
| 1 | [11-ml-artifact-store.md](11-ml-artifact-store.md) | `internal/artifacts/` Store interface with Local + S3 backends | Done |
| 2 | [12-ml-job-io-staging.md](12-ml-job-io-staging.md) | `SubmitRequest` inputs/outputs/working_dir + node-side Stager | Done |
| 3 | [13-ml-workflow-artifact-passing.md](13-ml-workflow-artifact-passing.md) | `from: upstream.OUTPUT` references resolved at dispatch time | Done |
| 4 | [14-ml-node-labels-and-selectors.md](14-ml-node-labels-and-selectors.md) | Node labels + `node_selector` scheduler filter | Done |
| 5 | [15-ml-gpu-first-class-resource.md](15-ml-gpu-first-class-resource.md) | Whole-GPU scheduling + `CUDA_VISIBLE_DEVICES` pinning | Done |
| 6 | [16-ml-dataset-model-registry.md](16-ml-dataset-model-registry.md) | `/api/datasets` + `/api/models` with lineage metadata | Done |
| 7 | [17-ml-inference-jobs.md](17-ml-inference-jobs.md) | `ServiceSpec` long-running jobs with readiness probes | Done |
| 8 | [implemented/18-ml-dashboard-module.md](implemented/18-ml-dashboard-module.md) | Angular ML module: Datasets / Models / Services / Pipelines (all four views shipped) | Done |
| 9 | [19-ml-end-to-end-demo.md](19-ml-end-to-end-demo.md) | `examples/ml-iris/` worked example + acceptance test | Written, awaiting acceptance run |
| 10 | [20-ml-documentation.md](20-ml-documentation.md) | Architecture / Components / persistence / ml-pipelines docs | Pending |

---

## Implementation order

| Step | Description                                      | Depends on   | Effort | Status |
|------|--------------------------------------------------|--------------|--------|--------|
| 1    | Artifact store abstraction (local + S3)          | —            | Medium | Done   |
| 2    | Job spec: inputs/outputs/working_dir + runtime staging | 1      | Medium | Done   |
| 3    | Workflow artifact passing (`from: job.output`)   | 2            | Medium | Done   |
| 4    | Node labels + node_selector scheduling           | —            | Small  | Done   |
| 5    | GPU as a scheduling resource                     | 4            | Medium | Done   |
| 6    | Dataset + model registries (API + storage)       | 1            | Medium | Done   |
| 7    | Inference jobs (Service spec + readiness)        | 2            | Small  | Done    |
| 8    | Dashboard ML module                              | 6, 7         | Medium | Done    |
| 9    | Iris end-to-end example                          | 2, 3, 6, 7   | Small  | Written (acceptance run pending) |
| 10   | Documentation                                    | All          | Small  | Pending |

Roughly: steps 1-3 unlock data-flow workflows, 4-5 unlock GPU scheduling,
6-7 unlock the registry-and-serve loop, 8-10 are surface polish + the
acceptance test.

---

## Security plan

This section anchors every step of the feature to the project's
existing security doctrine ([`docs/SECURITY.md`](../SECURITY.md),
[`docs/SECURITY-OPS.md`](../SECURITY-OPS.md),
[`docs/audits/TEMPLATE.md`](../audits/TEMPLATE.md)). The ML pipeline expands Helion's
trust surface in three new directions — bulk artifact bytes, cross-job
data references, and node-attested output metadata — each of which
needs treatment consistent with the existing JWT / mTLS / audit model
rather than parallel ad-hoc checks.

Per-step security controls, threat-table rows, and audit event
taxonomies have been moved into each step's own feature file. This
section retains only the cross-cutting "what is not yet covered"
caveats that apply to the slice as a whole.

### What is NOT (yet) covered

Called out so an audit (`docs/audits/TEMPLATE.md`) can pick them up as
findings if they ship before the mitigation lands:

- **Signed URLs for direct node→S3 transfer (step 6).** Current design
  pipes artifact bytes through the coordinator for upload. For large
  models this won't scale; signed URLs are the intended fix but they
  need careful scoping (per-object, per-subject, short TTL) that we
  want to get right, not retrofit.
- **Per-tenant artifact isolation.** Single shared bucket / store is
  fine for a single-tenant deployment. Multi-tenant deployments need
  per-tenant prefixes and a policy engine on top of the store — out of
  scope for the minimal pipeline.
- **Job-to-job authorization.** Step 3 will let job B read job A's
  outputs purely because A's workflow includes B. Fine-grained cross-
  workflow ACLs are not in scope.
- **Content scanning of uploaded artifacts.** No malware scan, no
  provenance attestation (SLSA / Sigstore). Deferred; registry hooks
  are an obvious place to add them.
- **Staging audit events.** The staging/output taxonomy in
  [step 2](12-ml-job-io-staging.md) is designed but not yet emitted.
  A small follow-up commit wires `s.audit.Log(...)` calls into
  `Stager.Prepare/Finalize` and the coordinator's
  `validateReportedOutput` drop path. Low risk; purely additive.

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
- **Hardware attestation of node labels.** A compromised node can
  register with `gpu=a100` on a CPU-only host and win GPU-targeted
  jobs it cannot run. Defending against this needs TPM / SGX /
  confidential-VM attestation and is well beyond the minimal
  pipeline's scope. The mTLS + ML-DSA certificate chain proves
  "this is node X that we issued a cert for"; it does not prove
  "this node actually owns the hardware it claims." Operator
  mitigation today: treat labels as deployment-supplied via
  `HELION_LABEL_*` env in orchestration manifests (k8s
  Deployment/DaemonSet, Nomad job spec, etc.) where the orchestrator
  controls both the label set and the node image. The node-agent's
  `nvidia-smi` auto-probe is best-effort metadata for a friendly
  cluster, **not** a security-grade claim. Captured in
  [deferred/16-hardware-attestation-of-node-labels.md](deferred/16-hardware-attestation-of-node-labels.md).

These are all reasonable follow-on features. None of them are required for
"a user can train and serve a model on Helion."
