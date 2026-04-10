package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/metrics"
)

// ── Mock interfaces ───────────────────────────────────────────────────────────

type mockJobCounter struct {
	byStatus map[string]int
	total    int
	err      error
}

func (m *mockJobCounter) CountByStatus(_ context.Context, status string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.byStatus[status], nil
}

func (m *mockJobCounter) CountTotal(_ context.Context) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.total, nil
}

type mockNodeCounter struct {
	total   int
	healthy int
	err     error
}

func (m *mockNodeCounter) CountTotal(_ context.Context) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.total, nil
}

func (m *mockNodeCounter) CountHealthy(_ context.Context) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.healthy, nil
}

// ── GetClusterMetrics ─────────────────────────────────────────────────────────

func TestGetClusterMetrics_ReturnsCorrectCounts(t *testing.T) {
	jobs := &mockJobCounter{
		byStatus: map[string]int{
			"RUNNING":   3,
			"PENDING":   1,
			"COMPLETED": 10,
			"FAILED":    2,
		},
		total: 16,
	}
	nodes := &mockNodeCounter{total: 5, healthy: 4}

	p := metrics.NewProvider(jobs, nodes)
	m, err := p.GetClusterMetrics(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.Nodes.Total != 5 {
		t.Errorf("want 5 total nodes, got %d", m.Nodes.Total)
	}
	if m.Nodes.Healthy != 4 {
		t.Errorf("want 4 healthy nodes, got %d", m.Nodes.Healthy)
	}
	if m.Jobs.Running != 3 {
		t.Errorf("want 3 running, got %d", m.Jobs.Running)
	}
	if m.Jobs.Pending != 1 {
		t.Errorf("want 1 pending, got %d", m.Jobs.Pending)
	}
	if m.Jobs.Completed != 10 {
		t.Errorf("want 10 completed, got %d", m.Jobs.Completed)
	}
	if m.Jobs.Failed != 2 {
		t.Errorf("want 2 failed, got %d", m.Jobs.Failed)
	}
	if m.Jobs.Total != 16 {
		t.Errorf("want 16 total jobs, got %d", m.Jobs.Total)
	}
}

func TestGetClusterMetrics_TimestampIsRecent(t *testing.T) {
	p := metrics.NewProvider(
		&mockJobCounter{byStatus: map[string]int{}, total: 0},
		&mockNodeCounter{},
	)
	m, err := p.GetClusterMetrics(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
}

func TestGetClusterMetrics_AllZero_WhenNoJobs(t *testing.T) {
	p := metrics.NewProvider(
		&mockJobCounter{byStatus: map[string]int{}, total: 0},
		&mockNodeCounter{total: 0, healthy: 0},
	)
	m, err := p.GetClusterMetrics(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Jobs.Running+m.Jobs.Pending+m.Jobs.Completed+m.Jobs.Failed+m.Jobs.Total != 0 {
		t.Error("expected all zero job counts")
	}
}

func TestGetClusterMetrics_NodeCountError_Propagates(t *testing.T) {
	p := metrics.NewProvider(
		&mockJobCounter{byStatus: map[string]int{}, total: 0},
		&mockNodeCounter{err: errors.New("db unavailable")},
	)
	_, err := p.GetClusterMetrics(context.Background())
	if err == nil {
		t.Error("expected error from node counter, got nil")
	}
}

func TestGetClusterMetrics_NodeHealthyError_Propagates(t *testing.T) {
	// CountTotal succeeds but CountHealthy fails.
	nodes := &errOnHealthy{}
	p := metrics.NewProvider(
		&mockJobCounter{byStatus: map[string]int{}, total: 0},
		nodes,
	)
	_, err := p.GetClusterMetrics(context.Background())
	if err == nil {
		t.Error("expected error from CountHealthy, got nil")
	}
}

// errOnHealthy succeeds on CountTotal but fails on CountHealthy.
type errOnHealthy struct{}

func (e *errOnHealthy) CountTotal(_ context.Context) (int, error) { return 3, nil }
func (e *errOnHealthy) CountHealthy(_ context.Context) (int, error) {
	return 0, errors.New("healthy query failed")
}

func TestGetClusterMetrics_MissingStatus_TreatedAsZero(t *testing.T) {
	// byStatus map has no RUNNING entry — should default to 0.
	jobs := &mockJobCounter{
		byStatus: map[string]int{
			"COMPLETED": 5,
		},
		total: 5,
	}
	p := metrics.NewProvider(jobs, &mockNodeCounter{total: 1, healthy: 1})
	m, err := p.GetClusterMetrics(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Jobs.Running != 0 {
		t.Errorf("missing status should yield 0, got %d", m.Jobs.Running)
	}
	if m.Jobs.Completed != 5 {
		t.Errorf("want 5 completed, got %d", m.Jobs.Completed)
	}
}
