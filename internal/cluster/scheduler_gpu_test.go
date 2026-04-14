package cluster_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func gpuNode(id string, gpus uint32) *cpb.Node {
	return &cpb.Node{
		NodeID:    id,
		Address:   id + ":9090",
		Healthy:   true,
		LastSeen:  time.Now(),
		MaxSlots:  8,
		TotalGpus: gpus,
	}
}

func TestPickForJob_ZeroGPUs_AllNodesEligible(t *testing.T) {
	// GPU filter disabled when gpusRequested == 0 — CPU-only node
	// and GPU node should both be candidates.
	src := &staticNodeSource{nodes: []*cpb.Node{
		gpuNode("cpu", 0),
		gpuNode("gpu", 4),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	got, err := s.PickForJob(nil, 0)
	if err != nil {
		t.Fatalf("PickForJob: %v", err)
	}
	if got == nil {
		t.Fatal("expected a node")
	}
}

func TestPickForJob_GPUFilter_ExcludesCPUOnly(t *testing.T) {
	src := &staticNodeSource{nodes: []*cpb.Node{
		gpuNode("cpu", 0),
		gpuNode("gpu", 4),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	got, err := s.PickForJob(nil, 1)
	if err != nil {
		t.Fatalf("PickForJob: %v", err)
	}
	if got.NodeID != "gpu" {
		t.Fatalf("picked %q; CPU-only node should be invisible", got.NodeID)
	}
}

func TestPickForJob_GPURequestExceedsAll_ReturnsNoMatch(t *testing.T) {
	// All nodes have GPUs, but none has enough. The dispatch loop
	// should see ErrNoNodeMatchesSelector and emit job.unschedulable.
	src := &staticNodeSource{nodes: []*cpb.Node{
		gpuNode("small", 1),
		gpuNode("medium", 2),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	_, err := s.PickForJob(nil, 4)
	if !errors.Is(err, cluster.ErrNoNodeMatchesSelector) {
		t.Fatalf("expected ErrNoNodeMatchesSelector, got %v", err)
	}
}

func TestPickForJob_SelectorAndGPUCombined(t *testing.T) {
	// Only the node matching BOTH label AND GPU count is eligible.
	src := &staticNodeSource{nodes: []*cpb.Node{
		{NodeID: "labelled-cpu", Address: "1", Healthy: true, LastSeen: time.Now(), MaxSlots: 8, TotalGpus: 0, Labels: map[string]string{"zone": "us-east"}},
		{NodeID: "unlabelled-gpu", Address: "2", Healthy: true, LastSeen: time.Now(), MaxSlots: 8, TotalGpus: 4, Labels: map[string]string{"zone": "eu"}},
		{NodeID: "labelled-gpu", Address: "3", Healthy: true, LastSeen: time.Now(), MaxSlots: 8, TotalGpus: 4, Labels: map[string]string{"zone": "us-east"}},
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	got, err := s.PickForJob(map[string]string{"zone": "us-east"}, 2)
	if err != nil {
		t.Fatalf("PickForJob: %v", err)
	}
	if got.NodeID != "labelled-gpu" {
		t.Fatalf("picked %q; only labelled-gpu satisfies both filters", got.NodeID)
	}
}

func TestPickForJob_NoHealthyNodes_PropagatesSentinel(t *testing.T) {
	s := cluster.NewScheduler(&staticNodeSource{}, cluster.NewRoundRobinPolicy())
	_, err := s.PickForJob(nil, 1)
	if !errors.Is(err, cluster.ErrNoHealthyNodes) {
		t.Fatalf("expected ErrNoHealthyNodes, got %v", err)
	}
}
