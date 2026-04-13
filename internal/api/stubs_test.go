package api_test

import (
	"context"
	"testing"
	"time"

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

func TestJobStoreAdapter_List_SortedNewestFirst(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)
	ctx := context.Background()

	// Submit jobs with short delays so CreatedAt differs.
	_ = inner.Submit(ctx, &cpb.Job{ID: "old", Command: "echo"})
	time.Sleep(2 * time.Millisecond)
	_ = inner.Submit(ctx, &cpb.Job{ID: "mid", Command: "echo"})
	time.Sleep(2 * time.Millisecond)
	_ = inner.Submit(ctx, &cpb.Job{ID: "new", Command: "echo"})

	jobs, _, err := adapter.List(ctx, "", 1, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("want 3, got %d", len(jobs))
	}
	// Newest job must be first.
	if jobs[0].ID != "new" {
		t.Errorf("page 1 first job = %q, want 'new' (newest first)", jobs[0].ID)
	}
	if jobs[2].ID != "old" {
		t.Errorf("page 1 last job = %q, want 'old'", jobs[2].ID)
	}
}

func TestJobStoreAdapter_List_NewestOnPage1_OldOnPage2(t *testing.T) {
	inner := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(inner)
	ctx := context.Background()

	// Create 5 jobs with distinct timestamps.
	for _, id := range []string{"j1", "j2", "j3", "j4", "j5"} {
		_ = inner.Submit(ctx, &cpb.Job{ID: id, Command: "echo"})
		time.Sleep(2 * time.Millisecond)
	}

	// Page 1 (size=2) should have j5, j4 (newest).
	page1, total, _ := adapter.List(ctx, "", 1, 2)
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 len = %d, want 2", len(page1))
	}
	if page1[0].ID != "j5" {
		t.Errorf("page 1 first = %q, want j5", page1[0].ID)
	}
	if page1[1].ID != "j4" {
		t.Errorf("page 1 second = %q, want j4", page1[1].ID)
	}

	// Page 2 should have j3, j2.
	page2, _, _ := adapter.List(ctx, "", 2, 2)
	if len(page2) != 2 {
		t.Fatalf("page 2 len = %d, want 2", len(page2))
	}
	if page2[0].ID != "j3" {
		t.Errorf("page 2 first = %q, want j3", page2[0].ID)
	}

	// Page 3 should have j1 (oldest).
	page3, _, _ := adapter.List(ctx, "", 3, 2)
	if len(page3) != 1 {
		t.Fatalf("page 3 len = %d, want 1", len(page3))
	}
	if page3[0].ID != "j1" {
		t.Errorf("page 3 first = %q, want j1 (oldest)", page3[0].ID)
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
