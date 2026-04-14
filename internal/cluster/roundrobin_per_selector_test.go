package cluster_test

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Step-4 follow-up: round-robin under selector filtering must rotate
// fairly within each candidate subset. Before the per-set counter
// landed, a shared global counter would skip over members of a
// narrowed subset depending on how many times the unfiltered pool
// had been asked.

func rrNode(id string) *cpb.Node {
	return &cpb.Node{NodeID: id, Address: id + ":1", Healthy: true, LastSeen: time.Now(), MaxSlots: 8}
}

// Four Picks over a 4-node slice must visit each node exactly once
// — the classic round-robin guarantee, still intact after the
// per-set counter refactor.
func TestRoundRobin_FullPool_EachNodeOnce(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	nodes := []*cpb.Node{rrNode("a"), rrNode("b"), rrNode("c"), rrNode("d")}
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		seen[p.Pick(nodes).NodeID]++
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if seen[id] != 1 {
			t.Fatalf("node %q picked %d times, want 1; full distribution: %+v", id, seen[id], seen)
		}
	}
}

// Two Picks on a different subset start at index 0 of that subset,
// not wherever the full-pool counter happens to be. Otherwise the
// subset's first Pick could skip half its members depending on
// how many full-pool calls happened before.
func TestRoundRobin_PerSubsetCounter_NotBiasedByOtherSubsets(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	full := []*cpb.Node{rrNode("a"), rrNode("b"), rrNode("c"), rrNode("d")}

	// Exhaust the full-pool counter a few times so the global
	// counter in the old implementation would have been at a
	// non-trivial index.
	for i := 0; i < 7; i++ {
		_ = p.Pick(full)
	}

	// Now rotate through a two-node subset. With per-subset
	// counters, the two Picks hit each member exactly once.
	subset := []*cpb.Node{rrNode("a"), rrNode("b")}
	first := p.Pick(subset).NodeID
	second := p.Pick(subset).NodeID
	if first == second {
		t.Fatalf("subset rotation degenerate: both Picks returned %q", first)
	}
}

// Two distinct subsets share a member; each subset must rotate
// independently. This is the scenario the selector filter creates:
// one workflow targets `zone=us-east`, another targets `gpu=a100`,
// a node with both labels appears in both subsets but its rotation
// slot differs per subset.
func TestRoundRobin_TwoSubsets_IndependentRotation(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	subsetA := []*cpb.Node{rrNode("shared"), rrNode("onlyA")}
	subsetB := []*cpb.Node{rrNode("shared"), rrNode("onlyB")}

	// Each subset should produce every member exactly once over
	// two Picks regardless of interleaving.
	calls := []struct {
		set  []*cpb.Node
		want map[string]bool // remaining
	}{
		{subsetA, map[string]bool{"shared": true, "onlyA": true}},
		{subsetB, map[string]bool{"shared": true, "onlyB": true}},
		{subsetA, map[string]bool{"shared": true, "onlyA": true}},
		{subsetB, map[string]bool{"shared": true, "onlyB": true}},
	}
	subsetPicks := map[string]map[string]int{}
	for i, c := range calls {
		id := p.Pick(c.set).NodeID
		key := id // for debug printing if a fail happens
		_ = key
		if subsetPicks[memberKey(c.set)] == nil {
			subsetPicks[memberKey(c.set)] = map[string]int{}
		}
		subsetPicks[memberKey(c.set)][id]++
		_ = i
	}
	// After the four calls, each subset should have seen each of
	// its members exactly once (two picks per subset, two members
	// each).
	for subsetKey, counts := range subsetPicks {
		for id, n := range counts {
			if n != 1 {
				t.Errorf("subset %q: node %q picked %d times, want 1 (full distribution: %+v)", subsetKey, id, n, counts)
			}
		}
	}
}

func memberKey(nodes []*cpb.Node) string {
	out := ""
	for _, n := range nodes {
		out += n.NodeID + "|"
	}
	return out
}
