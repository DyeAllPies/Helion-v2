# Deferred: Coordinator-side in-use resource tracking (CPU, memory, GPU)

**Priority:** P2
**Status:** Deferred
**Originating feature:** [feature 10 — minimal ML pipeline](../10-minimal-ml-pipeline.md)

## Context

The scheduler's [`ResourceAwarePolicy`](../../../internal/cluster/policy_resource.go) already uses node-reported total CPU / memory / slots / GPUs as bin-packing dimensions, but it does **not** subtract resources currently claimed by jobs that have already been dispatched to a node. The `running_jobs` count is used as a coarse proxy ("each job uses DefaultResourceRequest().CpuMillicores"), which works because the existing CPU/memory tracking is itself coarse — but the proxy breaks down for GPUs, where each whole-device claim is exact and the coordinator should know what's free per node.

**Today's behaviour, all dimensions:**

- Node A reports `TotalGpus=4`. Four 1-GPU jobs are currently dispatched.
- Scheduler picks Node A again for a fifth 1-GPU job (no in-use tracking → still looks free).
- Runtime allocator on Node A correctly fails the dispatch ("insufficient devices"), the dispatch loop transitions the job to Failed, the retry policy re-enqueues, and eventually it lands on a node with capacity.
- Net effect: one wasted RPC + retry round-trip per oversubscription. Correct outcome, slow path.

**The fix:** before each `Pick`, walk `JobStore.NonTerminal()` and count the resource reservations of jobs whose `NodeID` matches each candidate. Subtract from the node's reported totals. Cache the per-node usage per dispatch tick to avoid O(jobs × nodes) cost — the dispatch loop runs at 100 ms today so a single per-tick pass is fine.

Apply the same treatment to CPU millicores and memory bytes (the existing proxy logic was always a placeholder until proper tracking landed). GPU is the case that motivated the work — it's the dimension where the coordinator-side oversubscription cost is highest because every dispatched-then-failed GPU job ties up real device slots on the wrong node for a tick.

## Why deferred

The runtime-side allocator already provides correctness — bad scheduling decisions fail fast rather than producing duplicate device assignments. The fix is a UX/efficiency improvement, not a correctness one. Bundling it with the broader scheduler-tracking refactor (CPU + memory + GPU all at once) is cleaner than three separate slices.

## Revisit trigger

Revisit when the wasted dispatch RPC per oversubscription becomes visible in operator feedback, or alongside a broader scheduler-tracking refactor.
