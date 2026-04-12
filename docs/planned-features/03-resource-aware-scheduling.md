# Feature: Resource-Aware Scheduling

**Priority:** P1
**Status:** Partial — tracks `running_jobs` count only, no CPU/memory reservation
**Affected files:** `proto/coordinator.proto`, `proto/node.proto`, `internal/cluster/scheduler.go`, `internal/cluster/policy.go`, `internal/cluster/registry.go`

## Problem

The scheduler picks nodes based on job count (least-loaded) or round-robin. It has no concept of node capacity or job resource requirements. This leads to:

1. **Overprovisioning** — a node with 2 CPU cores can receive 50 jobs
2. **Underutilization** — large nodes treated the same as small nodes
3. **OOM risk** — no memory reservation; multiple memory-heavy jobs on same node
4. **No bin-packing** — no optimization for resource utilization

## Current state

- `HeartbeatMessage` carries `running_jobs` (uint32) — only load signal
- `NodeMetrics` (gRPC) reports `cpu_percent` and `mem_percent` but these aren't used by the scheduler
- `LeastLoadedPolicy` picks the node with fewest `running_jobs`
- `ResourceLimits` exist in job proto (memory_bytes, cpu_quota_us) but aren't considered during scheduling
- No node capacity metadata stored by the coordinator

## Design

### Node capacity declaration

Nodes report their total capacity at registration and in heartbeats:

```protobuf
message NodeCapacity {
  uint32 cpu_millicores = 1;    // total CPU (e.g., 4000 = 4 cores)
  uint64 memory_bytes = 2;      // total memory
  uint32 max_slots = 3;         // max concurrent jobs (soft limit)
}

message HeartbeatMessage {
  // ... existing fields ...
  NodeCapacity capacity = 7;          // total resources (reported once, cached)
  ResourceUsage current_usage = 8;    // live usage snapshot
}

message ResourceUsage {
  uint32 cpu_millicores_used = 1;  // sum of reserved CPU across running jobs
  uint64 memory_bytes_used = 2;    // sum of reserved memory across running jobs
  uint32 slots_used = 3;           // running job count
}
```

### Job resource requests

Jobs declare what they need (requests) separately from limits:

```protobuf
message ResourceRequest {
  uint32 cpu_millicores = 1;   // CPU reservation (default: 100 = 0.1 core)
  uint64 memory_bytes = 2;     // memory reservation (default: 64MB)
  uint32 slots = 3;            // slot count (default: 1)
}
```

- **Request** = what the scheduler reserves (guaranteed)
- **Limit** = what the runtime enforces (ceiling, already exists as `ResourceLimits`)
- Request <= Limit always. If only limit specified, request = limit.

### Scheduling algorithm: best-fit

New `ResourceAwarePolicy` replaces or supplements existing policies:

```go
type ResourceAwarePolicy struct{}

func (p *ResourceAwarePolicy) Pick(nodes []NodeEntry, job Job) (NodeEntry, error) {
    var candidates []NodeEntry
    for _, n := range nodes {
        avail := n.Capacity.Sub(n.Usage)
        if avail.Fits(job.ResourceRequest) {
            candidates = append(candidates, n)
        }
    }
    if len(candidates) == 0 {
        return NodeEntry{}, ErrNoCapacity
    }
    // Best-fit: pick node with least remaining capacity after placing job
    // (minimizes fragmentation)
    sort.Slice(candidates, func(i, j int) bool {
        ri := candidates[i].RemainingAfter(job.ResourceRequest)
        rj := candidates[j].RemainingAfter(job.ResourceRequest)
        return ri.Score() < rj.Score()
    })
    return candidates[0], nil
}
```

### Handling `ErrNoCapacity`

When no node can fit a job:
1. Job stays `pending` (not failed)
2. Dispatch loop skips it this tick
3. On next tick, re-evaluate (a job may have finished, freeing capacity)
4. After configurable `queue_timeout` (default: 5m), mark job as `failed` with error "no capacity available"

### Registry changes

`NodeEntry` in the registry gains:

```go
type NodeEntry struct {
    // ... existing fields ...
    Capacity  NodeCapacity   // set at registration, updated on heartbeat
    Usage     ResourceUsage  // updated on every heartbeat
}
```

### Coordinator bookkeeping

The coordinator maintains a reservation ledger (in-memory, rebuilt from heartbeats):

- On dispatch: `reserved[nodeID] += job.ResourceRequest`
- On job completion: `reserved[nodeID] -= job.ResourceRequest`
- On heartbeat: reconcile with node-reported `current_usage` (node is authoritative)

This prevents race conditions where the coordinator dispatches faster than heartbeats arrive.

## Policy selection

```
HELION_SCHEDULER=resource-aware   # new default when capacity is reported
HELION_SCHEDULER=least-loaded     # fallback (current default)
HELION_SCHEDULER=round-robin      # simple, no capacity check
```

If a node doesn't report capacity (old agent), the resource-aware policy skips it and falls back to least-loaded for that node.

## Implementation order

1. Proto changes: `NodeCapacity`, `ResourceUsage`, `ResourceRequest`
2. Node agent: collect capacity at startup (runtime.NumCPU, mem from /proc/meminfo or sysinfo)
3. Heartbeat: include capacity + current usage
4. Registry: store capacity/usage per node
5. `ResourceAwarePolicy`: best-fit placement
6. Dispatch loop: handle `ErrNoCapacity` (skip, not fail)
7. Queue timeout for unplaceable jobs
8. Dashboard: show node capacity utilization

## Defaults

| Field | Default | Rationale |
|-------|---------|-----------|
| `cpu_millicores` request | 100 (0.1 core) | Minimal reservation for lightweight jobs |
| `memory_bytes` request | 67108864 (64MB) | Reasonable for script-type jobs |
| `max_slots` | runtime.NumCPU() * 2 | Conservative concurrency limit |
| `queue_timeout` | 5 minutes | Prevent indefinite pending |

## Open questions

- Should overcommit be allowed? (e.g., reserve 120% of CPU). Start with no overcommit.
- GPU/accelerator resources? Defer — model as custom resource labels later.
- Affinity/anti-affinity? Defer — not needed for minimal orchestrator.
