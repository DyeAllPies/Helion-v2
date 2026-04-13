// internal/cluster/policy_resource.go
//
// ResourceAwarePolicy — best-fit scheduling based on node capacity.
//
// Selects the node with the least remaining capacity after placing the job.
// This minimizes fragmentation (bin-packing). Nodes without capacity info
// or with insufficient resources are skipped.

package cluster

import (
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ResourceAwarePolicy picks the healthy node with the least remaining
// capacity after placing the job (best-fit bin-packing).
type ResourceAwarePolicy struct{}

// NewResourceAwarePolicy returns a ready-to-use ResourceAwarePolicy.
func NewResourceAwarePolicy() *ResourceAwarePolicy {
	return &ResourceAwarePolicy{}
}

func (p *ResourceAwarePolicy) Name() string { return "resource-aware" }

// Pick selects a node using best-fit bin-packing. Nodes without capacity
// info (CpuMillicores == 0) are skipped. If no node has capacity, falls
// back to the node with the fewest running jobs (least-loaded fallback).
func (p *ResourceAwarePolicy) Pick(nodes []*cpb.Node) *cpb.Node {
	if len(nodes) == 0 {
		return nil
	}

	// Find nodes that have capacity info and available slots.
	var best *cpb.Node
	bestScore := uint64(0) // lower is better for best-fit

	for _, n := range nodes {
		if n.CpuMillicores == 0 && n.MaxSlots == 0 {
			continue // no capacity info — skip for resource-aware
		}

		// Check slot availability.
		if n.MaxSlots > 0 && uint32(n.RunningJobs) >= n.MaxSlots {
			continue // full
		}

		// Score = remaining capacity after placement.
		// Lower score = tighter fit = better for bin-packing.
		score := remainingScore(n)
		if best == nil || score < bestScore {
			best = n
			bestScore = score
		}
	}

	if best != nil {
		return best
	}

	// Fallback: no node has capacity info or all are full.
	// Pick least-loaded (same as LeastLoadedPolicy).
	fallback := nodes[0]
	for _, n := range nodes[1:] {
		if n.RunningJobs < fallback.RunningJobs {
			fallback = n
		}
	}
	return fallback
}

// remainingScore computes a composite score representing remaining resources.
// Lower = less remaining = tighter fit (better for bin-packing).
func remainingScore(n *cpb.Node) uint64 {
	var score uint64

	// Remaining slots.
	if n.MaxSlots > 0 {
		used := uint32(n.RunningJobs)
		if used < n.MaxSlots {
			score += uint64(n.MaxSlots - used)
		}
	}

	// Remaining CPU (in millicores) — weighted into score.
	// We don't track per-job CPU usage at the coordinator yet, so use
	// running_jobs as a proxy: each job uses DefaultResourceRequest().CpuMillicores.
	defaultCPU := uint64(cpb.DefaultResourceRequest().CpuMillicores)
	usedCPU := uint64(n.RunningJobs) * defaultCPU
	if uint64(n.CpuMillicores) > usedCPU {
		score += (uint64(n.CpuMillicores) - usedCPU) / 100 // scale down
	}

	return score
}
