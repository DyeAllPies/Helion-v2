// internal/cluster/registry_read.go
//
// Read-only Registry operations: Snapshot, HealthyNodes, Lookup, Len,
// CountTotal, CountHealthy, and the startup Restore.

package cluster

import (
	"context"
	"fmt"
	"log/slog"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Snapshot returns a point-in-time copy of all nodes (healthy and unhealthy).
func (r *Registry) Snapshot() []*cpb.Node {
	r.mu.RLock()
	entries := make([]*nodeEntry, 0, len(r.nodes))
	for _, e := range r.nodes {
		entries = append(entries, e)
	}
	r.mu.RUnlock()

	nodes := make([]*cpb.Node, 0, len(entries))
	for _, e := range entries {
		nodes = append(nodes, e.snapshot(r.staleAfter))
	}
	return nodes
}

// HealthyNodes returns only healthy nodes, used by the Scheduler.
func (r *Registry) HealthyNodes() []*cpb.Node {
	r.mu.RLock()
	entries := make([]*nodeEntry, 0, len(r.nodes))
	for _, e := range r.nodes {
		entries = append(entries, e)
	}
	r.mu.RUnlock()

	var healthy []*cpb.Node
	for _, e := range entries {
		if e.isHealthy(r.staleAfter) {
			healthy = append(healthy, e.snapshot(r.staleAfter))
		}
	}
	return healthy
}

// Lookup returns the node with the given ID, or false if not found.
func (r *Registry) Lookup(nodeID string) (*cpb.Node, bool) {
	r.mu.RLock()
	e, ok := r.nodes[nodeID]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return e.snapshot(r.staleAfter), true
}

// Len returns the total number of registered nodes.
func (r *Registry) Len() int {
	r.mu.RLock()
	n := len(r.nodes)
	r.mu.RUnlock()
	return n
}

// CountTotal implements metrics.NodeCounter.
func (r *Registry) CountTotal(_ context.Context) (int, error) {
	return r.Len(), nil
}

// CountHealthy implements metrics.NodeCounter.
func (r *Registry) CountHealthy(_ context.Context) (int, error) {
	return len(r.HealthyNodes()), nil
}

// Restore loads persisted nodes into memory on startup.
// Restored nodes start unhealthy — they must send a heartbeat to become healthy.
func (r *Registry) Restore(ctx context.Context) error {
	nodes, err := r.persister.LoadAllNodes(ctx)
	if err != nil {
		return fmt.Errorf("registry.Restore: %w", err)
	}

	r.mu.Lock()
	for _, n := range nodes {
		entry := &nodeEntry{
			nodeID:       n.NodeID,
			registeredAt: n.RegisteredAt,
		}
		entry.storeAddress(n.Address)
		// _lastSeenNano stays 0 → isHealthy() returns false until heartbeat arrives.
		r.nodes[n.NodeID] = entry
	}
	r.mu.Unlock()

	r.log.Info("registry: restored from persistence", slog.Int("count", len(nodes)))
	return nil
}
