// internal/cluster/dispatch_parallel_siblings_test.go
//
// Feature 42 — parallel-siblings dispatch invariant.
//
// Feature 40 shipped a 5-job MNIST workflow whose train_light and
// train_heavy sibling jobs share one upstream (preprocess) and
// target different runtime labels, but no unit test previously
// asserted that those siblings are actually dispatched concurrently.
// A silent regression — e.g. a dispatcher refactor that serialises
// one job per tick, or a selector bug that pins both to the same
// node — would leave the DAG structurally parallel but behaviourally
// serial. The test below locks down the invariant at the smallest
// meaningful scope: given two ready siblings and two distinctly-
// labelled nodes, a single dispatchPending tick hands both off to
// the node layer, each to a different address.
//
// This is the coordinator-side guarantee. Node-side concurrency
// (two jobs actually Running at once on separate hosts) is the
// feature-42 E2E spec's job — it needs Docker and lives in
// dashboard/e2e/specs/ml-mnist-parallel-walkthrough.spec.ts.

package cluster_test

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// heterogeneousRegistry returns a Registry with two healthy nodes
// carrying distinct runtime labels (go, rust) — mirrors the iris
// overlay's node topology so the unit test's selector matching is
// identical to the E2E environment.
func heterogeneousRegistry(t *testing.T) *cluster.Registry {
	t.Helper()
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()
	for _, n := range []struct {
		id, addr string
		labels   map[string]string
	}{
		{"go-node", "127.0.0.1:9090", map[string]string{"runtime": "go"}},
		{"rust-node", "127.0.0.1:9091", map[string]string{"runtime": "rust"}},
	} {
		if _, err := r.Register(ctx, &pb.RegisterRequest{
			NodeId: n.id, Address: n.addr, Labels: n.labels,
		}); err != nil {
			t.Fatalf("register %s: %v", n.id, err)
		}
		if err := r.HandleHeartbeat(ctx, &pb.HeartbeatMessage{NodeId: n.id}); err != nil {
			t.Fatalf("heartbeat %s: %v", n.id, err)
		}
	}
	return r
}

// TestDispatchLoop_ParallelSiblings_BothDispatchedInOneTick is the
// feature-42 invariant: once their shared upstream completes, two
// sibling workflow jobs whose node_selectors target different
// healthy nodes are both dispatched by a single dispatchPending
// iteration, each to its matching node.
//
// The fake node dispatcher records calls in arrival order; the test
// asserts both siblings appear exactly once and that the addresses
// are distinct (no "both pinned to go-node" regression).
func TestDispatchLoop_ParallelSiblings_BothDispatchedInOneTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── wiring ──────────────────────────────────────────────────
	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	wfs := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	reg := heterogeneousRegistry(t)
	sched := cluster.NewScheduler(reg, cluster.NewRoundRobinPolicy())
	nd := &parallelDispatcher{}
	loop := cluster.NewDispatchLoop(jobs, sched, nd, 20*time.Millisecond, slog.Default())
	loop.SetWorkflowStore(wfs)

	// ── workflow: preprocess → {train_light, train_heavy} ──────
	wf := &cpb.Workflow{
		ID:   "wf-parallel",
		Name: "parallel siblings",
		Jobs: []cpb.WorkflowJob{
			{Name: "preprocess", Command: "echo"},
			{
				Name:         "train_light",
				Command:      "echo",
				DependsOn:    []string{"preprocess"},
				NodeSelector: map[string]string{"runtime": "go"},
			},
			{
				Name:         "train_heavy",
				Command:      "echo",
				DependsOn:    []string{"preprocess"},
				NodeSelector: map[string]string{"runtime": "rust"},
			},
		},
	}
	if err := wfs.Submit(ctx, wf); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := wfs.Start(ctx, "wf-parallel", jobs); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Drive preprocess to Completed so both siblings become eligible.
	preID := "wf-parallel/preprocess"
	for _, target := range []cpb.JobStatus{
		cpb.JobStatusDispatching, cpb.JobStatusRunning, cpb.JobStatusCompleted,
	} {
		if err := jobs.Transition(ctx, preID, target, cluster.TransitionOptions{
			NodeID: "go-node",
		}); err != nil {
			t.Fatalf("preprocess → %s: %v", target, err)
		}
	}

	// ── run the dispatch loop until both siblings land ─────────
	go loop.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(nd.dispatched()) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	calls := nd.dispatched()
	if len(calls) != 2 {
		t.Fatalf("expected 2 dispatch calls, got %d: %+v", len(calls), calls)
	}

	// Both sibling job IDs must appear — no pre-dispatch drops.
	sortedIDs := make([]string, 0, len(calls))
	for _, c := range calls {
		sortedIDs = append(sortedIDs, c.jobID)
	}
	sort.Strings(sortedIDs)
	want := []string{"wf-parallel/train_heavy", "wf-parallel/train_light"}
	if sortedIDs[0] != want[0] || sortedIDs[1] != want[1] {
		t.Fatalf("wrong sibling IDs dispatched:\n  got:  %v\n  want: %v",
			sortedIDs, want)
	}

	// Must go to DIFFERENT node addresses — otherwise the selector
	// filter collapsed both siblings onto one runtime and the
	// heterogeneous-dispatch story is broken.
	byID := map[string]string{}
	for _, c := range calls {
		byID[c.jobID] = c.nodeAddr
	}
	if byID["wf-parallel/train_light"] == byID["wf-parallel/train_heavy"] {
		t.Fatalf("siblings dispatched to the SAME node address (%s) — selector mismatch or scheduler regression",
			byID["wf-parallel/train_light"])
	}
	if byID["wf-parallel/train_light"] != "127.0.0.1:9090" {
		t.Errorf("train_light went to %s, want go-node at 127.0.0.1:9090",
			byID["wf-parallel/train_light"])
	}
	if byID["wf-parallel/train_heavy"] != "127.0.0.1:9091" {
		t.Errorf("train_heavy went to %s, want rust-node at 127.0.0.1:9091",
			byID["wf-parallel/train_heavy"])
	}

	// Neither sibling may be in a terminal state when both have
	// been dispatched — "both Running before either Succeeds" is
	// the wall-clock invariant feature 42 locks down. The mock
	// dispatcher returns without advancing the node-side state
	// machine, so jobs stay in Dispatching, which is specifically
	// not-terminal.
	for _, jobID := range want {
		j, err := jobs.Get(jobID)
		if err != nil {
			t.Fatalf("Get %s: %v", jobID, err)
		}
		if j.Status.IsTerminal() {
			t.Errorf("%s unexpectedly terminal: %s", jobID, j.Status)
		}
	}
}

// ── test-local mock ───────────────────────────────────────────────

// parallelDispatcher is a NodeDispatcher stub that records both the
// job ID and the node address for each call, since the existing
// mockNodeDispatcher in dispatch_test.go only records IDs — and the
// feature-42 assertion needs the address to prove the two siblings
// hit different runtimes. The mutex mirrors mockNodeDispatcher —
// DispatchLoop.Run() runs on its own goroutine so the test reader
// and the dispatcher writer share this slice across threads.
type parallelDispatcher struct {
	mu    sync.Mutex
	calls []dispatchCall
}

type dispatchCall struct {
	jobID    string
	nodeAddr string
}

func (m *parallelDispatcher) DispatchToNode(_ context.Context, nodeAddr string, job *cpb.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, dispatchCall{jobID: job.ID, nodeAddr: nodeAddr})
	return nil
}

func (m *parallelDispatcher) dispatched() []dispatchCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]dispatchCall, len(m.calls))
	copy(out, m.calls)
	return out
}
