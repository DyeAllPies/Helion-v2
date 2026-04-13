// internal/api/adapters.go
//
// Production adapters that bridge the API layer to the real cluster/job
// subsystems. The coordinator wires these in place of the dev-only stubs
// defined in stubs.go.
//
// AUDIT H5 (fixed): previously cmd/helion-coordinator/main.go wired
// NewStubNodeRegistry() and NewStubMetricsProvider() into the HTTP API,
// so GET /nodes and GET /metrics returned fabricated empty data instead
// of real cluster state. Operators had no way to observe live nodes or
// job counts. RegistryNodeAdapter and RegistryMetricsAdapter below close
// that gap by delegating to *cluster.Registry and the existing JobStore
// counters.

package api

import (
	"context"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
)

// ── RegistryNodeAdapter ──────────────────────────────────────────────────────

// RegistryNodeAdapter implements NodeRegistryIface by delegating to a live
// *cluster.Registry. Safe for concurrent use — the underlying Registry
// serialises its own state.
type RegistryNodeAdapter struct {
	reg *cluster.Registry
}

// NewRegistryNodeAdapter wraps reg so it can be passed to NewServer as a
// NodeRegistryIface.
func NewRegistryNodeAdapter(reg *cluster.Registry) *RegistryNodeAdapter {
	return &RegistryNodeAdapter{reg: reg}
}

// ListNodes returns a snapshot of every node the registry knows about.
func (a *RegistryNodeAdapter) ListNodes(_ context.Context) ([]NodeInfo, error) {
	snap := a.reg.Snapshot()
	out := make([]NodeInfo, 0, len(snap))
	for _, n := range snap {
		out = append(out, NodeInfo{
			ID:            n.NodeID,
			Health:        healthLabel(n.Healthy),
			LastSeen:      n.LastSeen,
			RunningJobs:   int(n.RunningJobs),
			Address:       n.Address,
			CpuMillicores: n.CpuMillicores,
			TotalMemBytes: n.TotalMemBytes,
			MaxSlots:      n.MaxSlots,
		})
	}
	return out, nil
}

// GetNodeHealth returns "healthy"/"unhealthy" and the node's last-heartbeat
// timestamp. If the node is unknown, returns "unknown" and a zero time with
// nil error — matching the stub's semantics for callers that treat an
// unknown node as a normal (not erroneous) state.
func (a *RegistryNodeAdapter) GetNodeHealth(nodeID string) (string, time.Time, error) {
	n, ok := a.reg.Lookup(nodeID)
	if !ok {
		return "unknown", time.Time{}, nil
	}
	return healthLabel(n.Healthy), n.LastSeen, nil
}

// GetRunningJobCount returns the last-reported running-job count for the node,
// or 0 if the node is unknown.
func (a *RegistryNodeAdapter) GetRunningJobCount(nodeID string) int {
	n, ok := a.reg.Lookup(nodeID)
	if !ok {
		return 0
	}
	return int(n.RunningJobs)
}

// RevokeNode delegates to the registry's revocation path, which closes any
// active heartbeat stream, clears the cert pin, and writes an audit event.
func (a *RegistryNodeAdapter) RevokeNode(ctx context.Context, nodeID, reason string) error {
	return a.reg.RevokeNode(ctx, nodeID, reason)
}

// ── RegistryMetricsAdapter ───────────────────────────────────────────────────

// jobStatusCounter is the minimal JobStore surface the metrics adapter needs.
// Satisfied by *cluster.JobStore (both methods are already defined).
type jobStatusCounter interface {
	CountByStatus(ctx context.Context, status string) (int, error)
	CountTotal(ctx context.Context) (int, error)
}

// RegistryMetricsAdapter implements MetricsProvider by composing node counts
// from the Registry and job counts from a JobStore.
type RegistryMetricsAdapter struct {
	reg  *cluster.Registry
	jobs jobStatusCounter
}

// NewRegistryMetricsAdapter wraps a Registry + JobStore for GET /metrics.
func NewRegistryMetricsAdapter(reg *cluster.Registry, jobs jobStatusCounter) *RegistryMetricsAdapter {
	return &RegistryMetricsAdapter{reg: reg, jobs: jobs}
}

// GetClusterMetrics computes live counts for nodes and jobs. A per-status
// lookup failure (e.g. BadgerDB read error) is propagated as a non-nil error
// so /metrics returns 500 rather than silently reporting zeroes.
func (a *RegistryMetricsAdapter) GetClusterMetrics(ctx context.Context) (*ClusterMetrics, error) {
	m := &ClusterMetrics{Timestamp: time.Now()}

	snap := a.reg.Snapshot()
	m.Nodes.Total = len(snap)
	for _, n := range snap {
		if n.Healthy {
			m.Nodes.Healthy++
		}
	}

	total, err := a.jobs.CountTotal(ctx)
	if err != nil {
		return nil, err
	}
	m.Jobs.Total = total

	for _, pair := range []struct {
		status string
		dst    *int
	}{
		{"RUNNING", &m.Jobs.Running},
		{"PENDING", &m.Jobs.Pending},
		{"COMPLETED", &m.Jobs.Completed},
		{"FAILED", &m.Jobs.Failed},
	} {
		n, err := a.jobs.CountByStatus(ctx, pair.status)
		if err != nil {
			return nil, err
		}
		*pair.dst = n
	}
	return m, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func healthLabel(healthy bool) string {
	if healthy {
		return "healthy"
	}
	return "unhealthy"
}
