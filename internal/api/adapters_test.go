// internal/api/adapters_test.go
//
// Tests for RegistryNodeAdapter and RegistryMetricsAdapter (AUDIT H5 fix).
// These drive real cluster.Registry + cluster.JobStore instances to verify
// the production wiring end-to-end.

package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

func newTestRegistry(t *testing.T) *cluster.Registry {
	t.Helper()
	// A long heartbeatInterval means registered nodes stay "healthy" (LastSeen
	// recent enough) for the duration of the test without needing to push
	// heartbeats.
	return cluster.NewRegistry(cluster.NopPersister{}, 10*time.Minute, nil)
}

// ── RegistryNodeAdapter ──────────────────────────────────────────────────────

func TestRegistryNodeAdapter_ListNodes_ReturnsRegisteredNodes(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Register(ctx, &pb.RegisterRequest{NodeId: "n1", Address: "10.0.0.1:8080"}); err != nil {
		t.Fatalf("Register n1: %v", err)
	}
	if _, err := reg.Register(ctx, &pb.RegisterRequest{NodeId: "n2", Address: "10.0.0.2:8080"}); err != nil {
		t.Fatalf("Register n2: %v", err)
	}

	adapter := api.NewRegistryNodeAdapter(reg)
	nodes, err := adapter.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("want 2 nodes, got %d", len(nodes))
	}

	found := map[string]api.NodeInfo{}
	for _, n := range nodes {
		found[n.ID] = n
	}
	if n, ok := found["n1"]; !ok || n.Address != "10.0.0.1:8080" {
		t.Errorf("n1 missing or wrong address: %+v", n)
	}
	if n, ok := found["n2"]; !ok || n.Health != "healthy" {
		t.Errorf("n2 missing or not healthy: %+v", n)
	}
}

func TestRegistryNodeAdapter_ListNodes_EmptyRegistry(t *testing.T) {
	reg := newTestRegistry(t)
	adapter := api.NewRegistryNodeAdapter(reg)

	nodes, err := adapter.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("want 0 nodes, got %d", len(nodes))
	}
}

func TestRegistryNodeAdapter_GetNodeHealth_KnownNode(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Register(ctx, &pb.RegisterRequest{NodeId: "health-n", Address: "127.0.0.1:1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	adapter := api.NewRegistryNodeAdapter(reg)
	health, lastSeen, err := adapter.GetNodeHealth("health-n")
	if err != nil {
		t.Fatalf("GetNodeHealth: %v", err)
	}
	if health != "healthy" {
		t.Errorf("health: want healthy, got %q", health)
	}
	if lastSeen.IsZero() {
		t.Error("lastSeen should be set after Register")
	}
}

func TestRegistryNodeAdapter_GetNodeHealth_UnknownNode(t *testing.T) {
	reg := newTestRegistry(t)
	adapter := api.NewRegistryNodeAdapter(reg)

	health, lastSeen, err := adapter.GetNodeHealth("ghost")
	if err != nil {
		t.Fatalf("GetNodeHealth: %v", err)
	}
	if health != "unknown" {
		t.Errorf("unknown node health: want 'unknown', got %q", health)
	}
	if !lastSeen.IsZero() {
		t.Error("unknown node lastSeen should be zero time")
	}
}

func TestRegistryNodeAdapter_GetRunningJobCount_UnknownReturnsZero(t *testing.T) {
	reg := newTestRegistry(t)
	adapter := api.NewRegistryNodeAdapter(reg)

	if n := adapter.GetRunningJobCount("ghost"); n != 0 {
		t.Errorf("unknown node running count: want 0, got %d", n)
	}
}

func TestRegistryNodeAdapter_RevokeNode_DelegatesToRegistry(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Register(ctx, &pb.RegisterRequest{NodeId: "to-revoke", Address: "127.0.0.1:1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	adapter := api.NewRegistryNodeAdapter(reg)
	if err := adapter.RevokeNode(ctx, "to-revoke", "test"); err != nil {
		t.Fatalf("RevokeNode: %v", err)
	}

	if !reg.IsRevoked("to-revoke") {
		t.Error("registry should report the node as revoked")
	}
}

// ── RegistryMetricsAdapter ───────────────────────────────────────────────────

func TestRegistryMetricsAdapter_ReportsNodeCounts(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()

	for _, id := range []string{"m1", "m2", "m3"} {
		if _, err := reg.Register(ctx, &pb.RegisterRequest{NodeId: id, Address: "127.0.0.1:1"}); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}

	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewRegistryMetricsAdapter(reg, jobs)

	m, err := adapter.GetClusterMetrics(ctx)
	if err != nil {
		t.Fatalf("GetClusterMetrics: %v", err)
	}
	if m.Nodes.Total != 3 {
		t.Errorf("total nodes: want 3, got %d", m.Nodes.Total)
	}
	if m.Nodes.Healthy != 3 {
		t.Errorf("healthy nodes: want 3, got %d", m.Nodes.Healthy)
	}
}

func TestRegistryMetricsAdapter_ReportsJobCountsByStatus(t *testing.T) {
	reg := newTestRegistry(t)
	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ctx := context.Background()

	// Submit three jobs; they all land in PENDING.
	for _, id := range []string{"j1", "j2", "j3"} {
		if err := jobs.Submit(ctx, &cpb.Job{ID: id, Command: "echo"}); err != nil {
			t.Fatalf("Submit %s: %v", id, err)
		}
	}
	// Transition one to dispatching→running→completed.
	if err := jobs.Transition(ctx, "j1", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "n"}); err != nil {
		t.Fatalf("dispatch j1: %v", err)
	}
	if err := jobs.Transition(ctx, "j1", cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
		t.Fatalf("run j1: %v", err)
	}
	if err := jobs.Transition(ctx, "j1", cpb.JobStatusCompleted, cluster.TransitionOptions{}); err != nil {
		t.Fatalf("complete j1: %v", err)
	}

	adapter := api.NewRegistryMetricsAdapter(reg, jobs)
	m, err := adapter.GetClusterMetrics(ctx)
	if err != nil {
		t.Fatalf("GetClusterMetrics: %v", err)
	}

	if m.Jobs.Total != 3 {
		t.Errorf("total: want 3, got %d", m.Jobs.Total)
	}
	if m.Jobs.Pending != 2 {
		t.Errorf("pending: want 2, got %d", m.Jobs.Pending)
	}
	if m.Jobs.Completed != 1 {
		t.Errorf("completed: want 1, got %d", m.Jobs.Completed)
	}
	if m.Jobs.Running != 0 {
		t.Errorf("running: want 0, got %d", m.Jobs.Running)
	}
}

func TestRegistryMetricsAdapter_EmptyClusterReturnsZeroes(t *testing.T) {
	reg := newTestRegistry(t)
	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewRegistryMetricsAdapter(reg, jobs)

	m, err := adapter.GetClusterMetrics(context.Background())
	if err != nil {
		t.Fatalf("GetClusterMetrics: %v", err)
	}
	if m.Nodes.Total != 0 || m.Nodes.Healthy != 0 {
		t.Errorf("nodes: want 0/0, got %d/%d", m.Nodes.Total, m.Nodes.Healthy)
	}
	if m.Jobs.Total != 0 {
		t.Errorf("job total: want 0, got %d", m.Jobs.Total)
	}
	if m.Timestamp.IsZero() {
		t.Error("timestamp should be set")
	}
}
