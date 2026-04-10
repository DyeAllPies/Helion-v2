package api_test

import (
	"context"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── JobStoreAdapter ───────────────────────────────────────────────────────────

func TestJobStoreAdapter_Submit_Get_Roundtrip(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)

	job := &cpb.Job{ID: "j-adapter-1", Command: "ls"}
	if err := adapter.Submit(context.Background(), job); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got, err := adapter.Get("j-adapter-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "j-adapter-1" {
		t.Errorf("want id=j-adapter-1, got %q", got.ID)
	}
}

func TestJobStoreAdapter_List_ReturnsPaginated(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)

	for i := 0; i < 5; i++ {
		_ = adapter.Submit(context.Background(), &cpb.Job{
			ID:      "j-" + string(rune('a'+i)),
			Command: "echo",
		})
	}

	jobs, total, err := adapter.List(context.Background(), "", 1, 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Errorf("want total=5, got %d", total)
	}
	if len(jobs) != 3 {
		t.Errorf("want 3 jobs on page 1, got %d", len(jobs))
	}
}

func TestJobStoreAdapter_List_BeyondLastPage_ReturnsEmpty(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)
	_ = adapter.Submit(context.Background(), &cpb.Job{ID: "j1", Command: "ls"})

	jobs, _, err := adapter.List(context.Background(), "", 100, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("want 0 jobs past last page, got %d", len(jobs))
	}
}

func TestJobStoreAdapter_GetJobsByStatus_FiltersByStatus(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)

	_ = adapter.Submit(context.Background(), &cpb.Job{ID: "j1", Command: "ls"})
	_ = adapter.Submit(context.Background(), &cpb.Job{ID: "j2", Command: "echo"})

	jobs, err := adapter.GetJobsByStatus(context.Background(), "PENDING")
	if err != nil {
		t.Fatalf("GetJobsByStatus: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("want 2 pending jobs, got %d", len(jobs))
	}
}

func TestJobStoreAdapter_TerminalJobDurations_NoTerminal_ReturnsEmpty(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)
	_ = adapter.Submit(context.Background(), &cpb.Job{ID: "j1", Command: "ls"})

	durs, err := adapter.TerminalJobDurations(context.Background())
	if err != nil {
		t.Fatalf("TerminalJobDurations: %v", err)
	}
	if len(durs) != 0 {
		t.Errorf("want 0 durations for pending job, got %d", len(durs))
	}
}

func TestJobStoreAdapter_TerminalJobDurations_WithCompletedJob(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)
	ctx := context.Background()

	// Submit → dispatching → running → completed (follows state machine).
	_ = inner.Submit(ctx, &cpb.Job{ID: "j-done", Command: "ls"})
	_ = inner.Transition(ctx, "j-done", cpb.JobStatusDispatching, cluster.TransitionOptions{})
	_ = inner.Transition(ctx, "j-done", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = inner.Transition(ctx, "j-done", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	durs, err := adapter.TerminalJobDurations(context.Background())
	if err != nil {
		t.Fatalf("TerminalJobDurations: %v", err)
	}
	if len(durs) != 1 {
		t.Errorf("want 1 duration for completed job, got %d", len(durs))
	}
}

// ── StubNodeRegistry ──────────────────────────────────────────────────────────

func TestNewStubNodeRegistry_ListNodes_ReturnsEmpty(t *testing.T) {
	r := api.NewStubNodeRegistry()
	nodes, err := r.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("want 0 nodes, got %d", len(nodes))
	}
}

func TestNewStubNodeRegistry_GetNodeHealth_ReturnsUnknown(t *testing.T) {
	r := api.NewStubNodeRegistry()
	health, _, err := r.GetNodeHealth("any-node")
	if err != nil {
		t.Fatalf("GetNodeHealth: %v", err)
	}
	if health == "" {
		t.Error("expected non-empty health string")
	}
}

func TestNewStubNodeRegistry_GetRunningJobCount_ReturnsZero(t *testing.T) {
	r := api.NewStubNodeRegistry()
	if n := r.GetRunningJobCount("any-node"); n != 0 {
		t.Errorf("want 0, got %d", n)
	}
}

func TestNewStubNodeRegistry_RevokeNode_NoError(t *testing.T) {
	r := api.NewStubNodeRegistry()
	if err := r.RevokeNode(context.Background(), "node-1", "test"); err != nil {
		t.Errorf("RevokeNode: %v", err)
	}
}

// ── StubMetricsProvider ───────────────────────────────────────────────────────

func TestNewStubMetricsProvider_GetClusterMetrics_ReturnsMetrics(t *testing.T) {
	mp := api.NewStubMetricsProvider()
	m, err := mp.GetClusterMetrics(context.Background())
	if err != nil {
		t.Fatalf("GetClusterMetrics: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}
