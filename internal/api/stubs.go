// internal/api/stubs.go
//
// Stub implementations of API interfaces for testing and development.
// These allow the API server to compile and run without full Phase 4 integration.

package api

import (
	"context"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Job Store Adapter ────────────────────────────────────────────────────────

// JobStoreAdapter wraps a cluster.JobStore and implements JobStoreIface
// by adding the paginated List method.
type JobStoreAdapter struct {
	store interface {
		Submit(ctx context.Context, j *cpb.Job) error
		Get(jobID string) (*cpb.Job, error)
		List() []*cpb.Job
		GetJobsByStatus(ctx context.Context, status string) ([]*cpb.Job, error)
		CancelJob(ctx context.Context, jobID, reason string) error
	}
}

// NewJobStoreAdapter creates an adapter for cluster.JobStore.
// Returns the concrete type so callers can pass it to both JobStoreIface
// and metrics.DurationSource without a type assertion.
func NewJobStoreAdapter(store interface {
	Submit(ctx context.Context, j *cpb.Job) error
	Get(jobID string) (*cpb.Job, error)
	List() []*cpb.Job
	GetJobsByStatus(ctx context.Context, status string) ([]*cpb.Job, error)
	CancelJob(ctx context.Context, jobID, reason string) error
}) *JobStoreAdapter {
	return &JobStoreAdapter{store: store}
}

func (a *JobStoreAdapter) Submit(ctx context.Context, j *cpb.Job) error {
	return a.store.Submit(ctx, j)
}

func (a *JobStoreAdapter) Get(jobID string) (*cpb.Job, error) {
	return a.store.Get(jobID)
}

func (a *JobStoreAdapter) GetJobsByStatus(ctx context.Context, status string) ([]*cpb.Job, error) {
	return a.store.GetJobsByStatus(ctx, status)
}

func (a *JobStoreAdapter) CancelJob(ctx context.Context, jobID, reason string) error {
	return a.store.CancelJob(ctx, jobID, reason)
}

// List implements paginated list with filtering.
func (a *JobStoreAdapter) List(ctx context.Context, statusFilter string, page, size int) ([]*cpb.Job, int, error) {
	// Get all jobs
	var allJobs []*cpb.Job
	var err error

	if statusFilter != "" {
		// Filter by status
		allJobs, err = a.store.GetJobsByStatus(ctx, statusFilter)
		if err != nil {
			return nil, 0, err
		}
	} else {
		// Get all jobs
		allJobs = a.store.List()
	}

	total := len(allJobs)

	// Apply pagination
	start := (page - 1) * size
	if start >= total {
		return []*cpb.Job{}, total, nil
	}

	end := start + size
	if end > total {
		end = total
	}

	return allJobs[start:end], total, nil
}

// TerminalJobDurations returns elapsed seconds for all completed/failed jobs.
// Implements metrics.DurationSource.
func (a *JobStoreAdapter) TerminalJobDurations(ctx context.Context) ([]float64, error) {
	var durations []float64
	for _, status := range []string{"COMPLETED", "FAILED", "TIMEOUT", "LOST"} {
		jobs, err := a.store.GetJobsByStatus(ctx, status)
		if err != nil {
			continue
		}
		for _, j := range jobs {
			if !j.FinishedAt.IsZero() && !j.CreatedAt.IsZero() {
				d := j.FinishedAt.Sub(j.CreatedAt).Seconds()
				if d >= 0 {
					durations = append(durations, d)
				}
			}
		}
	}
	return durations, nil
}

// ── Stub Node Registry ───────────────────────────────────────────────────────

// stubNodeRegistry is a minimal implementation that returns empty results.
type stubNodeRegistry struct{}

func (s *stubNodeRegistry) ListNodes(ctx context.Context) ([]NodeInfo, error) {
	return []NodeInfo{}, nil
}

func (s *stubNodeRegistry) GetNodeHealth(nodeID string) (string, time.Time, error) {
	return "unknown", time.Time{}, nil
}

func (s *stubNodeRegistry) GetRunningJobCount(nodeID string) int {
	return 0
}

func (s *stubNodeRegistry) RevokeNode(ctx context.Context, nodeID, reason string) error {
	return nil
}

// ── Stub Metrics Provider ────────────────────────────────────────────────────

// stubMetricsProvider returns empty cluster metrics.
type stubMetricsProvider struct{}

func (s *stubMetricsProvider) GetClusterMetrics(ctx context.Context) (*ClusterMetrics, error) {
	return &ClusterMetrics{
		Timestamp: time.Now(),
	}, nil
}

// ── Public Constructors ──────────────────────────────────────────────────────

// NewStubNodeRegistry returns a stub node registry for testing.
func NewStubNodeRegistry() NodeRegistryIface {
	return &stubNodeRegistry{}
}

// NewStubMetricsProvider returns a stub metrics provider for testing.
func NewStubMetricsProvider() MetricsProvider {
	return &stubMetricsProvider{}
}
