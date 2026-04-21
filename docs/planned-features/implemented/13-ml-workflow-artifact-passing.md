# Feature: ML Workflow Artifact Passing

**Priority:** P1
**Status:** Done
**Affected files:** `internal/cluster/dag.go`, `internal/cluster/workflow_resolve.go`, `internal/cluster/dispatch.go`, `internal/api/handlers_jobs.go`, `internal/api/types.go`, `internal/proto/coordinatorpb/types.go`.
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## Inter-job artifact passing in workflows

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

## Implementation notes — workflow artifact passing (done)

The step-2 groundwork (ResolvedOutputs on every completed job)
already surfaces output URIs on the coordinator record; this step adds
the input side: a workflow job's binding can declare
`from: "<upstream_name>.<output_name>"` instead of a concrete URI,
and the coordinator rewrites the reference at dispatch time.

A minimal end-to-end workflow that uses every step-3 primitive:

```yaml
id: iris-pipeline
name: iris ml pipeline
jobs:
  - name: preprocess
    command: python
    args: ["/app/preprocess.py"]
    outputs:
      - name: TRAIN_PARQUET
        local_path: out/train.parquet
      - name: VAL_PARQUET
        local_path: out/val.parquet

  - name: train
    command: python
    args: ["/app/train.py"]
    depends_on: [preprocess]
    inputs:
      - name: TRAIN_DATA
        from: preprocess.TRAIN_PARQUET
        local_path: in/train.parquet
      - name: VAL_DATA
        from: preprocess.VAL_PARQUET
        local_path: in/val.parquet
    outputs:
      - name: MODEL
        local_path: out/model.pt
      - name: METRICS
        local_path: out/metrics.json
```

At submit time the DAG validator checks that every `from:` resolves
to an ancestor job declaring a matching output. At dispatch time, by
the moment `train` becomes eligible, `preprocess` has completed and
its `ResolvedOutputs` carries real URIs (`s3://bucket/jobs/iris-pipeline/preprocess/out/train.parquet`
and similar). The resolver rewrites each `from:` into the
corresponding URI, persists the rewrite onto the Job record, and
sends the now-concrete `Inputs` to the node. The node's stager
downloads each one into `in/…` and exports
`HELION_INPUT_TRAIN_DATA` / `HELION_INPUT_VAL_DATA` for the python
process to read.

Data model:

- [`cpb.ArtifactBinding`](../../../internal/proto/coordinatorpb/types.go)
  gains a `From` field (JSON `from,omitempty`). The persisted Job
  record carries both `From` and the resolved `URI` once the
  coordinator rewrites — preserves lineage for audit and for retries.
- [`api.ArtifactBindingRequest`](../../../internal/api/types.go) mirrors
  it; plain-job submits still reject any `From` (no upstream context).

API validation
([`validateArtifactBindingsCtx`](../../../internal/api/handlers_jobs.go)):

- `From` only accepted when `allowFrom=true` and `requireURI=true` —
  i.e. workflow-job **inputs**. Outputs and plain submits reject it.
- `URI` and `From` are mutually exclusive on a single input. One of
  them must be present.
- `From` shape: `<upstream_job>.<output_name>`, splits at the **last**
  `.` so workflow job names containing dots still work. Output name
  must match `[A-Z_][A-Z0-9_]*` (same rule as binding names). Length
  ≤ 256, NUL/control rejected.

DAG validation
([`internal/cluster/dag.go`](../../../internal/cluster/dag.go)):

After cycle detection, `validateFromReferences` walks every input
with a `From`:

1. **Upstream exists** in the workflow — else `ErrDAGUnknownFrom`.
2. **Upstream is an ancestor** (transitive `depends_on` closure) —
   else `ErrDAGFromNotAncestor`. Without the dependency edge the
   scheduler would race and could dispatch the downstream before the
   upstream completed.
3. **Upstream declares the named output** — else
   `ErrDAGFromUnknownOutput`.

Dispatch-time resolution
([`internal/cluster/workflow_resolve.go`](../../../internal/cluster/workflow_resolve.go)):

`ResolveJobInputs(job, JobLookup)` returns a defensive copy of the
Job with every `From` rewritten to the upstream's
`ResolvedOutputs[n].URI`. Hooked into
[`DispatchLoop.dispatchPending`](../../../internal/cluster/dispatch.go)
just after the eligibility gate and before the first transition:
failures (`ErrResolveUpstreamMissing`, `ErrResolveUpstreamNotCompleted`,
`ErrResolveOutputMissing`) transition the downstream to Failed with
a descriptive error rather than dispatching a half-specified job.

The rewritten Inputs slice is then persisted via
`JobStore.UpdateResolvedInputs` so `GET /api/jobs/{id}` returns the
concrete URI the node received alongside the original `From`
reference — lineage stays queryable across retries and restarts.
Persistence failure rolls back the in-memory mutation and fails the
dispatch loudly.

Security invariant kept: the resolver reads the upstream's
**persisted** `ResolvedOutputs` — already filtered through
`attestOutputs` when the upstream reported its terminal state ([step 2](12-ml-job-io-staging.md)).
A compromised node cannot inject a cross-job or foreign-scheme URI
into a downstream job's inputs because the URI it would inject never
made it onto the upstream's record in the first place. The
`jobs/<job_id>/<local_path>` suffix match from step 2 is the gate;
step 3 just trusts what it previously blessed.

Cross-job integrity attestation
([`cpb.ArtifactBinding.SHA256`](../../../internal/proto/coordinatorpb/types.go) +
[`artifacts.GetAndVerify`](../../../internal/artifacts/store.go)):

The resolver also copies the upstream's committed SHA-256 onto the
downstream's input. The digest travels over the wire via the new
`pb.ArtifactBinding.sha256` field and the node's stager routes
verified downloads through `artifacts.GetAndVerify` — the download
is read into a capped buffer, hashed, and only written to the
workdir if the digest matches. Mismatches return
`artifacts.ErrChecksumMismatch` and the stager rolls back the
workdir, failing the downstream job.

This is defence-in-depth on top of the hybrid PQ channel
([`docs/SECURITY.md` §3](../../security/)): the ML-KEM-768 / X25519
key exchange already protects the coordinator↔node wire from
tampering, and the artifact store is typically accessed over TLS.
The digest check catches three scenarios those layers don't
directly address:

- **Leaked store credentials.** An attacker who stole S3/MinIO
  write credentials but cannot reach the coordinator's BadgerDB
  could swap bytes under a key — the committed SHA on the
  coordinator record still reflects the original upload, so the
  downstream detects the swap.
- **Bit-rot / filesystem corruption** between upload and download.
- **Store-side MITM** (belt-and-braces over the store's TLS).

The check does **not** address a compromised *upstream node* that
reports a self-consistent `{URI, SHA}` pair — that stays the primary
threat model's concern and is handled by `attestOutputs` + node-ID
cross-check. Plain-URI inputs (no committed digest) fall back to a
streaming Get so no verification overhead is paid when no digest is
available to check against.

Tests: **21 new, all passing**
([`handlers_jobs_step3_test.go`](../../../internal/api/handlers_jobs_step3_test.go),
[`handlers_workflows_step3_test.go`](../../../internal/api/handlers_workflows_step3_test.go),
[`dag_step3_test.go`](../../../internal/cluster/dag_step3_test.go),
[`workflow_resolve_test.go`](../../../internal/cluster/workflow_resolve_test.go)).
Coverage includes: `From` shape happy path + 9 rejection cases,
URI/From mutual exclusivity, DAG unknown-upstream / non-ancestor /
unknown-output / malformed-shape rejection, resolver happy path +
upstream-missing / not-completed / output-missing / nil-job /
non-workflow-job / mixed-URI-and-From / multi-From round-trips.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| Resolved URIs cross job boundaries | `from: <upstream>.<name>` references resolved against the *coordinator's* `ResolvedOutputs` record (already attested by step 2's `jobs/<job_id>/<local_path>` suffix check), never against the node's proto. DAG validator rejects non-ancestor references (would race the scheduler) and undeclared outputs. Resolver is a pure read of persisted, pre-attested data — a compromised node cannot inject a URI into a downstream job it doesn't own. Upstream's committed SHA-256 travels with each resolved input; the downstream stager runs `artifacts.GetAndVerify` to catch leaked-store-creds tamper, bit rot, and store-side MITM. | §1 threat isolation — trust boundary at the coordinator, not at the wire; §3 defence-in-depth on top of hybrid PQ channel |
