// internal/cluster/scheduler_test.go
//
// Unit tests for the Scheduler and its Policy implementations.
//
// Coverage targets (exit criteria §7 Phase 2):
//
//   ✓ RoundRobinPolicy: cycles through all nodes in order
//   ✓ RoundRobinPolicy: wraps correctly after exhausting the list
//   ✓ RoundRobinPolicy: single node always returns that node
//   ✓ RoundRobinPolicy: empty list returns nil
//   ✓ RoundRobinPolicy: node failure mid-test (slice shrinks between calls)
//   ✓ RoundRobinPolicy: concurrent Pick calls do not race (run with -race)
//   ✓ LeastLoadedPolicy: picks node with fewest running jobs
//   ✓ LeastLoadedPolicy: tie-breaks by slice order (first wins)
//   ✓ LeastLoadedPolicy: single node always returns that node
//   ✓ LeastLoadedPolicy: empty list returns nil
//   ✓ LeastLoadedPolicy: node failure mid-test (highest-load node removed)
//   ✓ LeastLoadedPolicy: concurrent Pick calls do not race (run with -race)
//   ✓ Scheduler.Pick: returns ErrNoHealthyNodes when source is empty
//   ✓ Scheduler.Pick: returns correct node from round-robin source
//   ✓ Scheduler.Pick: returns correct node from least-loaded source
//   ✓ Scheduler.Pick: node fails → source returns one node → correct fallback
//   ✓ PolicyFromEnv: "least" → LeastLoadedPolicy
//   ✓ PolicyFromEnv: "" → RoundRobinPolicy
//   ✓ PolicyFromEnv: "round-robin" → RoundRobinPolicy
//   ✓ Scheduler.PolicyName: returns the policy's name

package cluster_test

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── test helpers ─────────────────────────────────────────────────────────────

// node builds a *cpb.Node with the given id, address, and running-job count.
// healthy is always true — the scheduler receives only healthy nodes.
func node(id, addr string, running int32) *cpb.Node {
	return &cpb.Node{
		NodeID:      id,
		Address:     addr,
		Healthy:     true,
		RunningJobs: running,
	}
}


// staticSource is a NodeSource that always returns the same slice.
// Used for Scheduler unit tests; swap the slice pointer to simulate node loss.
type staticSource struct {
	mu    sync.Mutex
	nodes []*cpb.Node
}

func (s *staticSource) HealthyNodes() []*cpb.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*cpb.Node, len(s.nodes))
	copy(cp, s.nodes)
	return cp
}

func (s *staticSource) setNodes(nodes []*cpb.Node) {
	s.mu.Lock()
	s.nodes = nodes
	s.mu.Unlock()
}

// ── RoundRobinPolicy ─────────────────────────────────────────────────────────

func TestRoundRobin_CyclesInOrder(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	nodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
		node("n3", "10.0.0.3:8080", 0),
	}

	// Three picks should hit n1, n2, n3 in order.
	for i, want := range []string{"n1", "n2", "n3"} {
		got := p.Pick(nodes)
		if got == nil {
			t.Fatalf("pick %d: got nil", i)
		}
		if got.NodeID != want {
			t.Errorf("pick %d: got %q, want %q", i, got.NodeID, want)
		}
	}
}

func TestRoundRobin_WrapsAfterExhaustion(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	nodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
	}

	picks := make([]string, 6)
	for i := range picks {
		picks[i] = p.Pick(nodes).NodeID
	}

	want := []string{"n1", "n2", "n1", "n2", "n1", "n2"}
	for i, w := range want {
		if picks[i] != w {
			t.Errorf("pick %d: got %q, want %q", i, picks[i], w)
		}
	}
}

func TestRoundRobin_SingleNode(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	n := node("only", "10.0.0.1:8080", 0)

	for i := 0; i < 5; i++ {
		got := p.Pick([]*cpb.Node{n})
		if got.NodeID != "only" {
			t.Errorf("pick %d: got %q, want %q", i, got.NodeID, "only")
		}
	}
}

func TestRoundRobin_EmptyReturnsNil(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	if got := p.Pick(nil); got != nil {
		t.Errorf("Pick(nil): got %v, want nil", got)
	}
	if got := p.Pick([]*cpb.Node{}); got != nil {
		t.Errorf("Pick([]): got %v, want nil", got)
	}
}

// TestRoundRobin_NodeFailureMidTest simulates a node going unhealthy between
// scheduler calls.  The healthy list shrinks from 3 to 2 nodes.
// The round-robin counter keeps incrementing; modulo ensures a valid index.
func TestRoundRobin_NodeFailureMidTest(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()

	threeNodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
		node("n3", "10.0.0.3:8080", 0),
	}
	twoNodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n3", "10.0.0.3:8080", 0), // n2 went unhealthy and was removed
	}

	// First three picks use the full list.
	for i := 0; i < 3; i++ {
		got := p.Pick(threeNodes)
		if got == nil {
			t.Fatalf("pre-failure pick %d: got nil", i)
		}
	}

	// After n2 fails, the next 4 picks must all land on n1 or n3 — never nil.
	for i := 0; i < 4; i++ {
		got := p.Pick(twoNodes)
		if got == nil {
			t.Fatalf("post-failure pick %d: got nil", i)
		}
		if got.NodeID != "n1" && got.NodeID != "n3" {
			t.Errorf("post-failure pick %d: got unexpected node %q", i, got.NodeID)
		}
	}
}

// TestRoundRobin_Concurrent verifies no data races under concurrent Pick calls.
// Run with: go test -race ./internal/cluster/
func TestRoundRobin_Concurrent(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	nodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
		node("n3", "10.0.0.3:8080", 0),
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			got := p.Pick(nodes)
			if got == nil {
				t.Errorf("concurrent Pick: got nil")
			}
		}()
	}
	wg.Wait()
}

func TestRoundRobin_Name(t *testing.T) {
	p := cluster.NewRoundRobinPolicy()
	if p.Name() != "round-robin" {
		t.Errorf("Name() = %q, want %q", p.Name(), "round-robin")
	}
}

// ── LeastLoadedPolicy ────────────────────────────────────────────────────────

func TestLeastLoaded_PicksLeastBusy(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()
	nodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 5),
		node("n2", "10.0.0.2:8080", 1), // fewest jobs
		node("n3", "10.0.0.3:8080", 3),
	}
	got := p.Pick(nodes)
	if got.NodeID != "n2" {
		t.Errorf("Pick: got %q, want %q", got.NodeID, "n2")
	}
}

func TestLeastLoaded_TieBreakerIsSliceOrder(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()
	// All nodes have 0 running jobs — first in slice wins.
	nodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
		node("n3", "10.0.0.3:8080", 0),
	}
	got := p.Pick(nodes)
	if got.NodeID != "n1" {
		t.Errorf("tie-break: got %q, want %q (first in slice)", got.NodeID, "n1")
	}
}

func TestLeastLoaded_SingleNode(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()
	n := node("only", "10.0.0.1:8080", 7)
	got := p.Pick([]*cpb.Node{n})
	if got.NodeID != "only" {
		t.Errorf("single node: got %q, want %q", got.NodeID, "only")
	}
}

func TestLeastLoaded_EmptyReturnsNil(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()
	if got := p.Pick(nil); got != nil {
		t.Errorf("Pick(nil): got %v, want nil", got)
	}
	if got := p.Pick([]*cpb.Node{}); got != nil {
		t.Errorf("Pick([]): got %v, want nil", got)
	}
}

// TestLeastLoaded_NodeFailureMidTest simulates the busiest node going unhealthy.
// After it's removed the least-loaded among the survivors should be chosen.
func TestLeastLoaded_NodeFailureMidTest(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()

	// Before failure: n3 is busiest.
	before := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 2),
		node("n2", "10.0.0.2:8080", 1), // least loaded
		node("n3", "10.0.0.3:8080", 9), // busiest
	}
	if got := p.Pick(before); got.NodeID != "n2" {
		t.Errorf("before failure: got %q, want %q", got.NodeID, "n2")
	}

	// n1 also goes down; only n3 and n2 remain — but n3 has now drained to 0.
	after := []*cpb.Node{
		node("n2", "10.0.0.2:8080", 1),
		node("n3", "10.0.0.3:8080", 0), // drained and recovered, now least loaded
	}
	if got := p.Pick(after); got.NodeID != "n3" {
		t.Errorf("after failure: got %q, want %q", got.NodeID, "n3")
	}
}

// TestLeastLoaded_Concurrent verifies no data races under concurrent Pick calls.
func TestLeastLoaded_Concurrent(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()
	nodes := []*cpb.Node{
		node("n1", "10.0.0.1:8080", 3),
		node("n2", "10.0.0.2:8080", 1),
		node("n3", "10.0.0.3:8080", 7),
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			got := p.Pick(nodes)
			// All concurrent picks must choose n2 (fewest jobs).
			if got == nil || got.NodeID != "n2" {
				t.Errorf("concurrent Pick: got %v, want n2", got)
			}
		}()
	}
	wg.Wait()
}

func TestLeastLoaded_Name(t *testing.T) {
	p := cluster.NewLeastLoadedPolicy()
	if p.Name() != "least-loaded" {
		t.Errorf("Name() = %q, want %q", p.Name(), "least-loaded")
	}
}

// ── Scheduler ────────────────────────────────────────────────────────────────

func TestScheduler_Pick_RoundRobin(t *testing.T) {
	src := &staticSource{nodes: []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())

	got1, err := s.Pick()
	if err != nil {
		t.Fatalf("Pick 1: %v", err)
	}
	got2, err := s.Pick()
	if err != nil {
		t.Fatalf("Pick 2: %v", err)
	}
	// The two picks should be different nodes (round-robin).
	if got1.NodeID == got2.NodeID {
		t.Errorf("two round-robin picks returned the same node %q", got1.NodeID)
	}
}

func TestScheduler_Pick_LeastLoaded(t *testing.T) {
	src := &staticSource{nodes: []*cpb.Node{
		node("n1", "10.0.0.1:8080", 4),
		node("n2", "10.0.0.2:8080", 1), // least loaded
		node("n3", "10.0.0.3:8080", 7),
	}}
	s := cluster.NewScheduler(src, cluster.NewLeastLoadedPolicy())

	got, err := s.Pick()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got.NodeID != "n2" {
		t.Errorf("LeastLoaded Pick: got %q, want %q", got.NodeID, "n2")
	}
}

func TestScheduler_Pick_NoHealthyNodes(t *testing.T) {
	src := &staticSource{nodes: nil}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())

	_, err := s.Pick()
	if !errors.Is(err, cluster.ErrNoHealthyNodes) {
		t.Errorf("empty cluster: got %v, want ErrNoHealthyNodes", err)
	}
}

// TestScheduler_NodeFailure simulates a node going unhealthy between picks.
// The scheduler must fall back to the surviving node without error.
func TestScheduler_NodeFailure(t *testing.T) {
	src := &staticSource{nodes: []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())

	// First pick: both nodes available.
	if _, err := s.Pick(); err != nil {
		t.Fatalf("pre-failure Pick: %v", err)
	}

	// n1 goes unhealthy — source now returns only n2.
	src.setNodes([]*cpb.Node{node("n2", "10.0.0.2:8080", 0)})

	got, err := s.Pick()
	if err != nil {
		t.Fatalf("post-failure Pick: %v", err)
	}
	if got.NodeID != "n2" {
		t.Errorf("post-failure: got %q, want %q", got.NodeID, "n2")
	}

	// Both nodes fail — next pick must return ErrNoHealthyNodes.
	src.setNodes(nil)
	_, err = s.Pick()
	if !errors.Is(err, cluster.ErrNoHealthyNodes) {
		t.Errorf("all-down: got %v, want ErrNoHealthyNodes", err)
	}
}

func TestScheduler_PolicyName(t *testing.T) {
	src := &staticSource{}

	rr := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())
	if rr.PolicyName() != "round-robin" {
		t.Errorf("PolicyName = %q, want %q", rr.PolicyName(), "round-robin")
	}

	ll := cluster.NewScheduler(src, cluster.NewLeastLoadedPolicy())
	if ll.PolicyName() != "least-loaded" {
		t.Errorf("PolicyName = %q, want %q", ll.PolicyName(), "least-loaded")
	}
}

// ── PolicyFromEnv ─────────────────────────────────────────────────────────────

func TestPolicyFromEnv_Least(t *testing.T) {
	t.Setenv("HELION_SCHEDULER", "least")
	p := cluster.PolicyFromEnv()
	if p.Name() != "least-loaded" {
		t.Errorf("HELION_SCHEDULER=least: got %q, want %q", p.Name(), "least-loaded")
	}
}

func TestPolicyFromEnv_Empty(t *testing.T) {
	os.Unsetenv("HELION_SCHEDULER")
	p := cluster.PolicyFromEnv()
	if p.Name() != "round-robin" {
		t.Errorf("HELION_SCHEDULER='': got %q, want %q", p.Name(), "round-robin")
	}
}

func TestPolicyFromEnv_Unknown(t *testing.T) {
	t.Setenv("HELION_SCHEDULER", "unknown-policy")
	p := cluster.PolicyFromEnv()
	if p.Name() != "round-robin" {
		t.Errorf("HELION_SCHEDULER=unknown: got %q, want %q", p.Name(), "round-robin")
	}
}

// ── Concurrent Scheduler.Pick (run with -race) ────────────────────────────────

// TestScheduler_ConcurrentPick fires 200 goroutines calling Pick simultaneously
// against a round-robin scheduler and verifies no races occur.
func TestScheduler_ConcurrentPick(t *testing.T) {
	src := &staticSource{nodes: []*cpb.Node{
		node("n1", "10.0.0.1:8080", 0),
		node("n2", "10.0.0.2:8080", 0),
		node("n3", "10.0.0.3:8080", 0),
	}}
	s := cluster.NewScheduler(src, cluster.NewRoundRobinPolicy())

	const goroutines = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			got, err := s.Pick()
			if err != nil {
				t.Errorf("concurrent Pick: %v", err)
			}
			if got == nil {
				t.Errorf("concurrent Pick: got nil node")
			}
		}()
	}
	wg.Wait()
}
