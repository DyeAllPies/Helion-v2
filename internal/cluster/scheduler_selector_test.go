package cluster_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// staticNodeSource satisfies cluster.NodeSource with a fixed list of
// pre-built nodes. Lets selector tests focus on the filter behaviour
// without running a real registry.
type staticNodeSource struct{ nodes []*cpb.Node }

func (s *staticNodeSource) HealthyNodes() []*cpb.Node { return s.nodes }

func labeledNode(id, addr string, labels map[string]string) *cpb.Node {
	return &cpb.Node{
		NodeID:      id,
		Address:     addr,
		Healthy:     true,
		LastSeen:    time.Now(),
		MaxSlots:    8,
		Labels:      labels,
	}
}

func TestPickForSelector_EmptySelectorMatchesAnyNode(t *testing.T) {
	src := &staticNodeSource{nodes: []*cpb.Node{
		labeledNode("n1", "a:1", nil),
		labeledNode("n2", "a:2", map[string]string{"gpu": "a100"}),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	got, err := s.PickForSelector(nil)
	if err != nil {
		t.Fatalf("PickForSelector(nil): %v", err)
	}
	if got == nil {
		t.Fatal("expected a node")
	}
}

func TestPickForSelector_ExactMatch(t *testing.T) {
	src := &staticNodeSource{nodes: []*cpb.Node{
		labeledNode("cpu-1", "a:1", map[string]string{"role": "cpu"}),
		labeledNode("gpu-1", "a:2", map[string]string{"role": "gpu", "gpu": "a100"}),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	got, err := s.PickForSelector(map[string]string{"gpu": "a100"})
	if err != nil {
		t.Fatalf("PickForSelector: %v", err)
	}
	if got.NodeID != "gpu-1" {
		t.Fatalf("wrong node picked: %q", got.NodeID)
	}
}

func TestPickForSelector_NoMatch_ReturnsSentinel(t *testing.T) {
	src := &staticNodeSource{nodes: []*cpb.Node{
		labeledNode("cpu-1", "a:1", map[string]string{"role": "cpu"}),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	_, err := s.PickForSelector(map[string]string{"gpu": "a100"})
	if !errors.Is(err, cluster.ErrNoNodeMatchesSelector) {
		t.Fatalf("expected ErrNoNodeMatchesSelector, got %v", err)
	}
}

func TestPickForSelector_PartialMatchAllRequired(t *testing.T) {
	// Selector {role:gpu, cuda:12.4} — node advertises role=gpu but
	// wrong cuda. Must not match.
	src := &staticNodeSource{nodes: []*cpb.Node{
		labeledNode("gpu-old", "a:1", map[string]string{"role": "gpu", "cuda": "11.8"}),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	_, err := s.PickForSelector(map[string]string{"role": "gpu", "cuda": "12.4"})
	if !errors.Is(err, cluster.ErrNoNodeMatchesSelector) {
		t.Fatalf("expected ErrNoNodeMatchesSelector, got %v", err)
	}
}

func TestPickForSelector_NoHealthyNodes(t *testing.T) {
	s := cluster.NewScheduler(&staticNodeSource{}, cluster.NewRoundRobinPolicy())
	_, err := s.PickForSelector(map[string]string{"x": "y"})
	if !errors.Is(err, cluster.ErrNoHealthyNodes) {
		t.Fatalf("expected ErrNoHealthyNodes, got %v", err)
	}
}

func TestPickForSelector_NilLabelsOnNodeWithSelector(t *testing.T) {
	// A node with no labels cannot satisfy a non-empty selector.
	src := &staticNodeSource{nodes: []*cpb.Node{
		labeledNode("bare", "a:1", nil),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	_, err := s.PickForSelector(map[string]string{"gpu": "a100"})
	if !errors.Is(err, cluster.ErrNoNodeMatchesSelector) {
		t.Fatalf("expected ErrNoNodeMatchesSelector, got %v", err)
	}
}
