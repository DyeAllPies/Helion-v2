# Feature: ML Job I/O Staging

**Priority:** P1
**Status:** Done
**Affected files:** `internal/api/types.go`, `internal/staging/` (new), `internal/runtime/`, `runtime-rust/`, `internal/nodeserver/server.go`, `cmd/helion-node/main.go`, `proto/node.proto`, `internal/proto/coordinatorpb/types.go`.
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## Job spec: inputs, outputs, working directory

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

## Implementation notes — job spec + runtime staging (done)

Data model:

- [`proto/node.proto`](../../../proto/node.proto) — new `ArtifactBinding`
  message; `DispatchRequest` extended with `working_dir`, `inputs`,
  `outputs`, `node_selector`. Protos regenerated.
- [`internal/proto/coordinatorpb/types.go`](../../../internal/proto/coordinatorpb/types.go) —
  `cpb.Job` gets the same four fields plus a new `cpb.ArtifactBinding`
  struct. JSON-serialised, so existing BadgerDB rows deserialize
  forward-compatibly.
- [`internal/api/types.go`](../../../internal/api/types.go) — `SubmitRequest`
  gets the same fields plus `ArtifactBindingRequest`.

API validation
([`internal/api/handlers_jobs.go`](../../../internal/api/handlers_jobs.go)):

- Binding name must match `[A-Z_][A-Z0-9_]*` so `HELION_INPUT_<NAME>`
  is always a safe env var.
- `local_path`: relative, ≤ 512 bytes, no NUL, no `\`, no `/`-prefix,
  no empty / `.` / `..` segments.
- `node_selector`: Kubernetes-shaped sizes (key ≤ 63 bytes, value ≤ 253,
  ≤ 32 entries), no `=` or NUL in keys.
- Per-direction cap: 64 bindings. URI: ≤ 2048 bytes, no NUL. Inputs
  **must** supply a URI; outputs **must not** (the runtime assigns it).
  Duplicate names rejected per direction.

Staging ([`internal/staging/`](../../../internal/staging/)):

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

- [`internal/cluster/node_dispatcher.go`](../../../internal/cluster/node_dispatcher.go) —
  forwards bindings on the wire.
- [`internal/runtime/runtime.go`](../../../internal/runtime/runtime.go) +
  [`go_runtime.go`](../../../internal/runtime/go_runtime.go) — `RunRequest.WorkingDir`
  sets `cmd.Dir`.
- [`internal/nodeserver/server.go`](../../../internal/nodeserver/server.go) —
  calls `Prepare → rt.Run → Finalize`. Stager-less nodes **reject**
  jobs that carry bindings rather than running blind. Env merge gives
  stager values precedence so a caller cannot shadow `HELION_INPUT_*`.
- [`cmd/helion-node/main.go`](../../../cmd/helion-node/main.go) — opt-in:
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
| **Compromised node reporting forged output URIs** | Coordinator-side `attestOutputs` in `grpcserver.handlers`: scheme pinned to `{file, s3}`, name regex `[A-Z_][A-Z0-9_]*`, URI length ≤2048, NUL/control rejected, count cap 64. **Plus a strict suffix match** against `jobs/<job_id>/<local_path>` — since `local_path` is validated at submit time (no `..`, no absolute, no NUL), a URI that doesn't end with the exact stager-minted key was fabricated. Prefix-mismatch drops emit a `security_violation` audit event. |
| **Compromised node reporting a result for a different node's job** | `ReportResult` cross-checks `result.NodeId` against `job.NodeID` (pinned at dispatch). Mismatch → `PermissionDenied` + `security_violation` audit. The legitimate node's job record is never mutated. |
| **Attacker-controlled input URIs at submit time** | API validator `isAllowedArtifactScheme`: input URIs must start with `file://` or `s3://`. Rejects `http://`, `https://`, `ftp://`, `data:`, `javascript:`, `gs://`, absolute paths, relative paths, and anything else — long before the node's stager would dereference them. |
| **Enumeration of artifact tree on a shared host** | `LocalStore` root + per-job subdirs created mode `0o700`; files inside remain `0o600` (default from `os.CreateTemp`). Owner-only end-to-end. |

**Terminal-event plumbing (closed):** The node's stager-finalized
output URIs now flow into `pb.JobResult.outputs`, through the
coordinator's `ReportResult` handler, into `TransitionOptions.ResolvedOutputs`,
and are persisted on `cpb.Job.ResolvedOutputs`. The `job.completed`
event carries an `outputs` array when present (via the new
`events.JobCompletedWithOutputs` constructor). [Step 3](13-ml-workflow-artifact-passing.md) reads these
persisted URIs to resolve `from: <upstream>.<output_name>` references.

**Workflow template plumbing (closed):** `cpb.WorkflowJob` now carries
`WorkingDir`, `Inputs`, `Outputs`, `NodeSelector`; `workflow_submit.Start`
copies them onto each materialised Job. The workflow API handler
([`internal/api/handlers_workflows.go`](../../../internal/api/handlers_workflows.go))
validates per-job bindings through the same validators as
`SubmitRequest` — `validateArtifactBindings`, `validateNodeSelector`,
`firstDuplicateBindingName`, `convertBindings` — so a workflow job
cannot slip past rules that a standalone submit would catch.

Deferred to later steps (HTTP handler layer when artifact upload API
lands in [step 6](16-ml-dataset-model-registry.md)): per-subject rate limit on artifact upload, audit
events (`artifact.put`, `staging.prepared`, `staging.uploaded`),
`http.MaxBytesReader` on the upload handler, signed URLs for node→S3
direct transfer.

Tests: **62 new, all passing**
([`handlers_jobs_step2_test.go`](../../../internal/api/handlers_jobs_step2_test.go),
[`handlers_workflows_ml_test.go`](../../../internal/api/handlers_workflows_ml_test.go),
[`staging_test.go`](../../../internal/staging/staging_test.go),
[`go_runtime_workdir_test.go`](../../../internal/runtime/go_runtime_workdir_test.go),
[`ml_outputs_test.go`](../../../internal/cluster/ml_outputs_test.go),
[`workflow_ml_test.go`](../../../internal/cluster/workflow_ml_test.go),
[`outputs_from_proto_test.go`](../../../internal/grpcserver/outputs_from_proto_test.go),
[`outputs_to_proto_test.go`](../../../internal/nodeserver/outputs_to_proto_test.go)).

### Staging follow-ups

Landed on top of the initial step-2 implementation after a second-pass
audit identified two gaps that would have surfaced under production
ML workloads:

- **Streaming verified download
  ([`staging.Stager.download`](../../../internal/staging/staging.go)).**
  The verified path used to call `GetAndVerify` (in-memory) then
  `WriteFile` — a 5 GiB checkpoint would have OOM'd the node. Now
  it opens a `.helion-stage-*.tmp` in the destination's parent dir,
  streams the download through `GetAndVerifyTo` while the hash is
  accumulated in-flight, fsyncs, and only renames onto the final
  path on verification success. A mismatch removes the tempfile
  before returning so no partial bytes ever appear at the staged
  path; workdir-level rollback in `Prepare` catches anything this
  misses.
- **Stale workdir sweep
  ([`staging.Stager.SweepStaleWorkdirs`](../../../internal/staging/sweep.go)).**
  If the node agent dies between `Prepare` and `Finalize` (OOM,
  SIGKILL, host reboot), the per-job workdir previously stayed on
  disk forever — long-running nodes accumulated orphans. The new
  sweep walks `HELION_WORK_ROOT` and removes entries whose mtime
  predates a configurable threshold (default 1 hour). Invoked once
  from [`cmd/helion-node/main.go`](../../../cmd/helion-node/main.go)
  at node startup, skipped when `HELION_KEEP_WORKDIR=1`. Safe to
  run concurrent with live traffic — active Prepare/Finalize cycles
  keep workdir mtimes fresh, so only truly orphaned trees get
  reaped.

### Rust runtime (parity landed)

Originally flagged as a step-2 gap. Closed by commit `506680d`
("feat(runtime): wire working_dir through Go↔Rust IPC + add runtime-rust doc"):
the Rust runtime honours `RunRequest.WorkingDir` via `cmd.current_dir()`.
Because input/output *bytes* flow through the Stager at the nodeserver
level (not through the runtime), no further Rust-side work is required
for step-2 artifact handling — Rust-backed nodes with a stager
configured stage artifacts correctly end-to-end.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| Node agent downloads + uploads bytes; env var injection | Stager: workdir `0o700` under operator-controlled `HELION_WORK_ROOT`; per-job `O_EXCL` `Mkdir`; `safeJoin` on every path; stager-wins env precedence; staging error → staging.failed path rolls back workdir. Coordinator: `validateReportedOutput` trust boundary on node-attested URIs | §6 Audit logging pattern (every transition audited — extended to staging transitions); stager refusal on `stager == nil` follows §1 fail-closed threat doctrine |

Threat additions handled here:

| Threat | Mitigation |
|---|---|
| Malicious artifact fills disk | `MaxInputDownloadBytes` LimitedReader on download |
| Oversized output exhausts store | `MaxOutputUploadBytes` pre-flight Lstat |
| Cross-job access via `local_path` traversal | API validator + Stager `safeJoin` prefix-check |
| Cross-job access via symlink output | `Lstat` + regular-file gate in `Stager.upload` |
| Env shadowing via user-supplied `HELION_INPUT_*` | Stager-wins precedence in `nodeserver.mergeEnv` |
| Unconfigured nodes running ML jobs blind | Node refuses dispatch when `stager == nil` |
| **Compromised node reports forged output URIs** | Coordinator `validateReportedOutput`: scheme pinned to `{file,s3}`, name regex, URI length + NUL reject, count cap. Invalid entries dropped + logged, job still terminates. |

Planned staging audit events (follow-up — taxonomy designed, emission
deferred):

| Event | Actor | Target | Details |
|---|---|---|---|
| `staging.prepared` | `node:<node_id>` | `job:<job_id>` | `{inputs: N, outputs: N, working_dir}` |
| `staging.uploaded` | `node:<node_id>` | `job:<job_id>` | `{outputs: [{name, uri, size}], duration_ms}` |
| `staging.failed` | `node:<node_id>` | `job:<job_id>` | `{reason, phase}` (phase ∈ prepare/run/finalize) |
| `staging.output_rejected` | `coordinator` | `job:<job_id>` | `{node_id, name, reason}` — emitted by `validateReportedOutput` drops |
