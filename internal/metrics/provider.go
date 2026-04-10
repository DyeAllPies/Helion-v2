// internal/metrics/provider.go
//
// Cluster metrics computation for the REST API.
//
// Phase 3/4: Provides snapshot metrics for GET /metrics endpoint
// and periodic updates for WebSocket /ws/metrics stream.

package metrics

import (
	"context"
	"time"
)

// Provider computes cluster metrics from job and node state.
type Provider struct {
	jobs  JobCounter
	nodes NodeCounter
}

// JobCounter provides job statistics.
type JobCounter interface {
	CountByStatus(ctx context.Context, status string) (int, error)
	CountTotal(ctx context.Context) (int, error)
}

// NodeCounter provides node statistics.
type NodeCounter interface {
	CountTotal(ctx context.Context) (int, error)
	CountHealthy(ctx context.Context) (int, error)
}

// ClusterMetrics represents a snapshot of cluster state.
type ClusterMetrics struct {
	Nodes struct {
		Total   int `json:"total"`
		Healthy int `json:"healthy"`
	} `json:"nodes"`
	Jobs struct {
		Running   int `json:"running"`
		Pending   int `json:"pending"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
		Total     int `json:"total"`
	} `json:"jobs"`
	Timestamp time.Time `json:"timestamp"`
}

// NewProvider creates a metrics provider.
func NewProvider(jobs JobCounter, nodes NodeCounter) *Provider {
	return &Provider{
		jobs:  jobs,
		nodes: nodes,
	}
}

// GetClusterMetrics computes current cluster metrics.
func (p *Provider) GetClusterMetrics(ctx context.Context) (*ClusterMetrics, error) {
	m := &ClusterMetrics{
		Timestamp: time.Now(),
	}

	// Get node counts
	totalNodes, err := p.nodes.CountTotal(ctx)
	if err != nil {
		return nil, err
	}
	m.Nodes.Total = totalNodes

	healthyNodes, err := p.nodes.CountHealthy(ctx)
	if err != nil {
		return nil, err
	}
	m.Nodes.Healthy = healthyNodes

	// Get job counts by status
	running, _ := p.jobs.CountByStatus(ctx, "RUNNING")
	m.Jobs.Running = running

	pending, _ := p.jobs.CountByStatus(ctx, "PENDING")
	m.Jobs.Pending = pending

	completed, _ := p.jobs.CountByStatus(ctx, "COMPLETED")
	m.Jobs.Completed = completed

	failed, _ := p.jobs.CountByStatus(ctx, "FAILED")
	m.Jobs.Failed = failed

	total, _ := p.jobs.CountTotal(ctx)
	m.Jobs.Total = total

	return m, nil
}
