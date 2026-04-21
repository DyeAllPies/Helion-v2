# Feature: ML GPU as a First-Class Resource

**Priority:** P1
**Status:** Code-complete, **unverified on real GPU hardware.** The scheduler bin-packs on a `GPUs` dimension, the per-node allocator tracks device indices, and `CUDA_VISIBLE_DEVICES` is set on the job env — all exercised by unit tests and a build-tag-gated `tests/gpu/` harness that probes `nvidia-smi` when present. GitHub Actions free tier has no GPU runners, so the harness has never run in CI; no CI run has ever touched a real GPU. A local developer with a CUDA-capable machine can run `go test -tags=gpu ./tests/gpu/...` to exercise the real-device path. Until that run happens on a representative GPU, treat GPU scheduling as "wired correctly on paper" rather than "validated end-to-end on hardware."
**Affected files:** `internal/proto/coordinatorpb/types.go`, `internal/api/types.go`, `internal/cluster/scheduler.go`, `internal/cluster/registry_node.go`, `internal/runtime/gpu_alloc.go`, `internal/runtime/go_runtime.go`, `internal/runtime/rust_client.go`, `cmd/helion-node/labels.go`, `tests/gpu/real_nvidia_smi_test.go`.
**Parent slice:** [feature 10 — ML pipeline](../10-minimal-ml-pipeline.md)

## GPU as a first-class resource

Promoted from the [`deferred/`](../deferred/) backlog. Minimal scope:

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

### Step-4 follow-up to pick up during this step

While the scheduler is being touched for GPU bin-packing, fix the
round-robin fairness gap introduced by selector filtering: today
`RoundRobinPolicy.counter` is global across all `PickForSelector`
calls, so a selector-narrowed candidate list inherits the global
index. A deployment with multiple distinct selectors (CPU jobs
targeting `role=cpu`, GPU jobs targeting `gpu=a100`) can produce
uneven dispatch across the members of each selector's candidate
set. Replace the single `atomic.Int64` counter with a `sync.Map` of
per-selector counters keyed by a canonical string rendering of the
selector (e.g. `gpu=a100|zone=us-east`). GPU scheduling already
wants per-selector state for device-index tracking, so the same
refactor serves both needs.

## Implementation notes — GPU as a first-class resource (done)

End-to-end GPU scheduling from API submission down to the
`CUDA_VISIBLE_DEVICES` string on the subprocess env. Label-based
matching ([step 4](14-ml-node-labels-and-selectors.md)) handles "which node has an A100?"; this slice
handles "reserve N whole GPUs and pin the job to those specific
device indices."

Data flow:

- [`cpb.ResourceRequest.GPUs`](../../../internal/proto/coordinatorpb/types.go) +
  [`api.ResourceRequestAPI.GPUs`](../../../internal/api/types.go) mirror
  each other; API validator caps requests at `maxGPUs = 16`.
- `pb.HeartbeatMessage.total_gpus` (new field 9) +
  `pb.DispatchRequest.gpus` (new field 11) carry the capacity and
  the per-job reservation on the wire.
- Node agent ([`cmd/helion-node/labels.go`](../../../cmd/helion-node/labels.go))
  probes `nvidia-smi --list-gpus` once at startup via the `gpuCountProbe`
  injection seam; the count feeds both the runtime's device-index
  allocator and the heartbeat capacity report so coordinator
  scheduling matches what the runtime can actually satisfy.
- Coordinator
  ([`cpb.Node.TotalGpus`](../../../internal/proto/coordinatorpb/types.go),
  [`nodeEntry._totalGpus`](../../../internal/cluster/registry_node.go))
  persists and snapshots total-GPU capacity per node.

Scheduler
([`filterByGPU`](../../../internal/cluster/scheduler.go)): the new
`PickForJob(selector, gpusRequested)` entry point layers a GPU
filter on top of the existing label-selector filter. A node with
`TotalGpus < gpusRequested` is invisible to the job — same semantics
as a missing label, same `ErrNoNodeMatchesSelector` surface, same
debounced `job.unschedulable` event. `gpusRequested == 0` disables
the filter entirely so legacy CPU jobs see every healthy node
regardless of GPU count.

GPU allocator
([`internal/runtime/gpu_alloc.go`](../../../internal/runtime/gpu_alloc.go)):
per-node device-index tracker, lowest-index-first allocation,
whole-device reservations only (no MIG slicing, no memory-fraction
tracking — deferred). `GoRuntime.Run` claims indices before exec,
sets `CUDA_VISIBLE_DEVICES=<csv>` on `cmd.Env`, releases on exit
(success, failure, timeout, context cancel). Oversubscription
returns an error before the subprocess starts so the coordinator's
filter slip (e.g. racy capacity update) never produces duplicate
device indices on a shared host.

Step-4 follow-up landed here: round-robin fairness under selector
filtering. `RoundRobinPolicy` replaced its single global
`atomic.Int64` with a `sync.Map` of per-candidate-set counters
keyed by the sorted node-ID list. Each distinct selector +
GPU-filter candidate set now rotates fairly within its own members
instead of inheriting a biased starting index from the global
counter.

Testing:

- **Pure-Go tests run on CI** (no GPU needed, 19 new):
  [`gpu_alloc_test.go`](../../../internal/runtime/gpu_alloc_test.go)
  covers allocator happy path / oversubscription rejection / zero
  request no-op / release-and-reuse / concurrent distinct
  allocations / zero-total fail / env string formatting;
  [`gpu_runtime_test.go`](../../../internal/runtime/gpu_runtime_test.go)
  end-to-end runs through a stub echo command asserting
  `CUDA_VISIBLE_DEVICES` is set on GPU jobs and not on CPU jobs;
  [`scheduler_gpu_test.go`](../../../internal/cluster/scheduler_gpu_test.go)
  covers filter semantics;
  [`roundrobin_per_selector_test.go`](../../../internal/cluster/roundrobin_per_selector_test.go)
  pins the per-set fairness invariant.
- **Real-GPU harness, local only**, under
  [`tests/gpu/`](../../tests/gpu/) gated by `//go:build gpu` so CI
  never compiles or runs it. Invoked via
  [`scripts/test-gpu.sh`](../../scripts/test-gpu.sh) after a
  developer confirms the normal e2e suite passes. Spot-checks
  `runNvidiaSmi` / `runNvidiaSmiCount` against a real device
  inventory — the one code path the production labels+count
  probes take that every other test stubs out.

Security (matches [step-4](14-ml-node-labels-and-selectors.md)'s posture — no new crypto, inherits mTLS +
hybrid PQ channel). A compromised node can still over-report
`total_gpus` and win GPU jobs it can't serve; defending that needs
hardware attestation, already captured in the
[deferred backlog](../deferred/README.md#hardware-attestation-of-node-labels).
The per-job allocator on the node itself catches the subset of
this failure where a *friendly-but-misconfigured* node reports
more GPUs than it really has: the first over-commit attempt fails
with an "insufficient devices" error before the subprocess starts.

What we did **not** do here (explicit deferrals):

- GPU memory tracking / fractional sharing (MIG) — capacity is
  whole-device count only.
- Multi-host collective training (NCCL, topology-aware placement) —
  the scheduler treats GPUs as a count per node, not a topology.
- Per-device metrics (SM utilisation, memory pressure, thermal
  throttling) — node reports only the total; runtime-side
  observability is step 7-adjacent work.
- **Coordinator-side in-use GPU tracking.** `filterByGPU` checks
  total capacity per node, not free capacity. An oversubscribed
  candidate is caught at the runtime allocator (job fails fast,
  retry policy re-dispatches elsewhere); the cost is one wasted
  RPC + retry round-trip per attempt. The fix is the same shape
  as the existing CPU/memory blind spot — capture them together
  in the [in-use resource tracking entry](../deferred/README.md#coordinator-side-in-use-resource-tracking-cpu-memory-gpu)
  on the deferred backlog.

Closed-here follow-ups from the second-pass audit (commit `<this slice>`):

- **`CUDA_VISIBLE_DEVICES` override safety** — fixed in
  [`go_runtime.go`](../../../internal/runtime/go_runtime.go) by
  building the subprocess env via a `map[string]string` so a
  user-supplied `CUDA_VISIBLE_DEVICES` cannot shadow the
  allocator's value. The Rust runtime path already used a
  map-based override; the Go runtime now matches. Without this,
  POSIX env precedence (first-set wins) let a malicious or
  confused caller escape per-job device pinning by setting their
  own value. Tests pin the invariant in both runtimes
  ([`gpu_runtime_test.go`](../../../internal/runtime/gpu_runtime_test.go)
  + [`rust_client_gpu_test.go`](../../../internal/runtime/rust_client_gpu_test.go)).
- **CPU jobs on GPU-equipped nodes can no longer see GPUs.**
  Both runtimes now stamp `CUDA_VISIBLE_DEVICES=""` for
  `req.GPUs == 0` whenever the node's allocator capacity is
  > 0. Without this hide, a malicious "CPU" job could supply
  its own `CUDA_VISIBLE_DEVICES="0,1,2,3"` and access devices
  pinned to a concurrent GPU job — escaping the per-job
  allocator's assignment from the side. The shared
  `withCudaVisibleDevices` helper handles the map-based
  override for both runtimes; CPU-only nodes (allocator
  capacity == 0) still pass user env through unchanged so
  legacy CPU workloads on GPU-less hosts are unaffected. Five
  new tests cover the three policy branches (GPU job → set
  to indices, CPU job on GPU node → hide, CPU job on CPU node
  → untouched) plus user-override blocked on the hide path
  for both runtimes.
- **BadgerDB roundtrip for `Node.TotalGpus`** — three new tests in
  [`persistence_labels_test.go`](../../../internal/cluster/persistence_labels_test.go)
  pin Save→LoadAll for the field, omitempty on zero, and forward
  compatibility with pre-GPU node rows. Symmetric to the
  step-4 labels persistence tests; without it a JSON-tag typo
  could silently zero out GPU capacity on coordinator restart
  and every GPU job would become unschedulable until the next
  heartbeat arrived.

### Rust runtime — GPU parity

**Step-5 GPU parity, Go-side only.** The GPU allocator and
`CUDA_VISIBLE_DEVICES` injection live entirely in
[`internal/runtime/rust_client.go`](../../../internal/runtime/rust_client.go):
`RustClient.Run` claims device indices from a shared `GPUAllocator`
*before* any IPC frame is built and stamps
`CUDA_VISIBLE_DEVICES=<csv>` into `req.Env`. The Rust executor
inherits the env unchanged — no `proto/runtime.proto` changes, no
`runtime-rust/src/executor.rs` changes. CUDA-aware libraries inside
the spawned subprocess see the standard env var regardless of which
runtime Helion used to launch them.

Why that's the right boundary: the Rust binary's job is process
isolation (cgroup v2 limits, seccomp-bpf). GPU device pinning is
universally a CUDA-runtime concern via the env var, not a kernel-
isolation concern; pulling it into the Rust executor would have
duplicated the allocator state, doubled the test surface, and
required a Rust-side proto regeneration on every future allocator
tweak. Doing it Go-side keeps the IPC schema stable and concentrates
the "node owns N GPUs, allocates indices to jobs" logic in one
place that both backends share via composition.

Tests
([`rust_client_gpu_test.go`](../../../internal/runtime/rust_client_gpu_test.go),
5 new): mock Rust server captures the encoded RunRequest payload
and we assert `CUDA_VISIBLE_DEVICES` is present + correct on GPU
jobs / absent on CPU jobs / oversubscription fails before IPC /
no-capacity allocator fails before IPC / device claims released
across back-to-back runs.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| Device enumeration & isolation | GPU indices derived by node at registration time (via `nvidia-smi`) — same trust level as CPU/memory counts; `CUDA_VISIBLE_DEVICES` set by stager, never by user env (stager-wins precedence applies) | — |
