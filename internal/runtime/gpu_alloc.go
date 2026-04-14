// internal/runtime/gpu_alloc.go
//
// Per-node GPU device-index allocator. The scheduler already filters
// nodes by total-GPU capacity (see internal/cluster/scheduler.go
// filterByGPU), so by the time a job reaches the runtime the node is
// known to have enough GPUs *in aggregate*. This allocator tracks
// which specific indices are currently in use on this node and hands
// out a comma-separated CUDA_VISIBLE_DEVICES string to each dispatched
// GPU job.
//
// The allocator is deliberately simple: whole-device reservations only
// (no MIG slicing, no memory-fraction tracking), lowest-index-first
// allocation so repeated runs on the same node tend to reuse index 0
// (easier to reason about than random scatter), and a single sync.Mutex
// because allocation traffic happens at most per dispatch — not a hot
// path.

package runtime

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// GPUAllocator tracks which device indices on this node are currently
// claimed by a running job. Methods are safe for concurrent use.
type GPUAllocator struct {
	mu    sync.Mutex
	total uint32          // capacity; 0 means "no GPUs on this node"
	busy  map[int]string  // device index → job ID that owns it
}

// NewGPUAllocator returns an allocator for a node with `total` whole
// GPUs. A total of 0 is legal (CPU-only node); callers that request
// GPUs on such an allocator get an error, never a partial allocation.
func NewGPUAllocator(total uint32) *GPUAllocator {
	return &GPUAllocator{
		total: total,
		busy:  make(map[int]string, total),
	}
}

// Allocate claims n whole GPUs for jobID and returns the selected
// device indices in ascending order. The caller is expected to set
// CUDA_VISIBLE_DEVICES=<csv of indices> on the subprocess env.
//
// If fewer than n devices are free, returns a non-nil error and no
// partial allocation is recorded — the node has to wait (or the
// coordinator re-schedules elsewhere).
//
// Allocating 0 devices is a no-op that returns nil, nil; callers
// that never request GPUs can ignore the allocator entirely.
func (a *GPUAllocator) Allocate(jobID string, n uint32) ([]int, error) {
	if n == 0 {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if uint32(len(a.busy))+n > a.total {
		return nil, fmt.Errorf("gpu: insufficient free devices (want %d, free %d/%d)",
			n, a.total-uint32(len(a.busy)), a.total)
	}

	picked := make([]int, 0, n)
	for idx := 0; idx < int(a.total) && uint32(len(picked)) < n; idx++ {
		if _, claimed := a.busy[idx]; claimed {
			continue
		}
		a.busy[idx] = jobID
		picked = append(picked, idx)
	}
	return picked, nil
}

// Release returns every device index currently claimed by jobID to
// the free pool. Safe to call for a job that never claimed any
// devices (no-op).
func (a *GPUAllocator) Release(jobID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for idx, owner := range a.busy {
		if owner == jobID {
			delete(a.busy, idx)
		}
	}
}

// InUse returns the current count of claimed devices. Intended for
// observability (metrics, readyz introspection, tests) — not part of
// the allocation fast path.
func (a *GPUAllocator) InUse() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.busy)
}

// VisibleDevicesEnv formats a slice of device indices as the
// comma-separated form CUDA_VISIBLE_DEVICES expects. Empty input
// returns the empty string so the caller can decide whether to set
// the env var at all (unset means "see every GPU", which we want
// to avoid for CPU jobs — the decision lives at the call site).
func VisibleDevicesEnv(indices []int) string {
	if len(indices) == 0 {
		return ""
	}
	parts := make([]string, len(indices))
	for i, idx := range indices {
		parts[i] = strconv.Itoa(idx)
	}
	return strings.Join(parts, ",")
}
