package cluster_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestResourceAwarePolicy_Name(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	if p.Name() != "resource-aware" {
		t.Errorf("Name() = %q, want resource-aware", p.Name())
	}
}

func TestResourceAwarePolicy_EmptyNodes(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	if n := p.Pick(nil); n != nil {
		t.Error("expected nil for empty nodes")
	}
}

func TestResourceAwarePolicy_SingleNodeWithCapacity(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	nodes := []*cpb.Node{
		{NodeID: "n1", CpuMillicores: 4000, MaxSlots: 8, RunningJobs: 2},
	}
	got := p.Pick(nodes)
	if got == nil || got.NodeID != "n1" {
		t.Errorf("expected n1, got %v", got)
	}
}

func TestResourceAwarePolicy_PicksTighterFit(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	nodes := []*cpb.Node{
		{NodeID: "big", CpuMillicores: 8000, MaxSlots: 16, RunningJobs: 2},
		{NodeID: "small", CpuMillicores: 2000, MaxSlots: 4, RunningJobs: 2},
	}
	got := p.Pick(nodes)
	if got == nil || got.NodeID != "small" {
		t.Errorf("expected small (tighter fit), got %v", got.NodeID)
	}
}

func TestResourceAwarePolicy_SkipsFullNode(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	nodes := []*cpb.Node{
		{NodeID: "full", CpuMillicores: 2000, MaxSlots: 4, RunningJobs: 4},
		{NodeID: "avail", CpuMillicores: 2000, MaxSlots: 4, RunningJobs: 1},
	}
	got := p.Pick(nodes)
	if got == nil || got.NodeID != "avail" {
		t.Errorf("expected avail (full node skipped), got %v", got)
	}
}

func TestResourceAwarePolicy_FallbackToLeastLoaded(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	// No capacity info — should fall back to least-loaded.
	nodes := []*cpb.Node{
		{NodeID: "a", RunningJobs: 5},
		{NodeID: "b", RunningJobs: 1},
	}
	got := p.Pick(nodes)
	if got == nil || got.NodeID != "b" {
		t.Errorf("expected b (least loaded fallback), got %v", got)
	}
}

func TestResourceAwarePolicy_AllFull_FallbackToLeastLoaded(t *testing.T) {
	p := cluster.NewResourceAwarePolicy()
	nodes := []*cpb.Node{
		{NodeID: "a", CpuMillicores: 2000, MaxSlots: 2, RunningJobs: 2},
		{NodeID: "b", CpuMillicores: 2000, MaxSlots: 2, RunningJobs: 2},
	}
	got := p.Pick(nodes)
	// Both full — falls back to least-loaded (tied, picks first).
	if got == nil {
		t.Error("expected non-nil even when all full (fallback)")
	}
}

func TestPolicyFromEnv_ResourceAware(t *testing.T) {
	t.Setenv("HELION_SCHEDULER", "resource-aware")
	p := cluster.PolicyFromEnv()
	if p.Name() != "resource-aware" {
		t.Errorf("PolicyFromEnv() = %q, want resource-aware", p.Name())
	}
}
