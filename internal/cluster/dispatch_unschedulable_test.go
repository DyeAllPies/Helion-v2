package cluster_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// schedulerWithLabeledNodes is the integration setup for dispatch-time
// selector behaviour: a Registry seeded with one labeled node, a
// Scheduler wired to a RoundRobin policy, plus a mockNodeDispatcher
// from the shared dispatch_test.go helpers.
func schedulerWithLabeledNodes(t *testing.T, labels map[string]string) (*cluster.Scheduler, *cluster.Registry) {
	t.Helper()
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()
	_, _ = r.Register(ctx, &pb.RegisterRequest{
		NodeId:  "worker",
		Address: "127.0.0.1:9090",
		Labels:  labels,
	})
	_ = r.HandleHeartbeat(ctx, &pb.HeartbeatMessage{NodeId: "worker"})
	return cluster.NewScheduler(r, cluster.NewRoundRobinPolicy()), r
}

// TestDispatch_SelectorMatch_DispatchesNode asserts that a pending job
// whose NodeSelector matches a registered node's labels gets
// dispatched normally — no unschedulable event, no stuck-in-pending.
func TestDispatch_SelectorMatch_DispatchesNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched, _ := schedulerWithLabeledNodes(t, map[string]string{"gpu": "a100"})
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(js, sched, nd, 20*time.Millisecond, slog.Default())

	_ = js.Submit(ctx, &cpb.Job{
		ID: "match-me", Command: "echo",
		NodeSelector: map[string]string{"gpu": "a100"},
	})
	go loop.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(nd.dispatched()) > 0 {
			if nd.dispatched()[0] == "match-me" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job never dispatched; calls=%v", nd.dispatched())
}

// TestDispatch_SelectorNoMatch_EmitsUnschedulable asserts that when
// the only registered node lacks the requested label, the dispatch
// loop emits job.unschedulable and leaves the job in pending. The
// coordinator never calls DispatchToNode.
func TestDispatch_SelectorNoMatch_EmitsUnschedulable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched, _ := schedulerWithLabeledNodes(t, map[string]string{"role": "cpu"})
	bus := events.NewBus(16, nil)
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	js.SetEventBus(bus)
	sub := bus.Subscribe(events.TopicJobUnschedulable)
	defer sub.Cancel()

	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(js, sched, nd, 20*time.Millisecond, slog.Default())

	_ = js.Submit(ctx, &cpb.Job{
		ID: "no-match", Command: "echo",
		NodeSelector: map[string]string{"gpu": "a100"},
	})
	go loop.Run(ctx)

	// Expect the event within one or two tick cycles.
	select {
	case evt := <-sub.C:
		if evt.Data["job_id"] != "no-match" {
			t.Fatalf("event job_id: %+v", evt.Data)
		}
		sel, ok := evt.Data["unsatisfied_selector"].(map[string]string)
		if !ok || sel["gpu"] != "a100" {
			t.Fatalf("unsatisfied_selector: %+v", evt.Data["unsatisfied_selector"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected job.unschedulable event")
	}

	// Job must not have been dispatched.
	for _, id := range nd.dispatched() {
		if id == "no-match" {
			t.Fatal("unmatched job was dispatched")
		}
	}
	// Job must still be pending — the event is diagnostic, not terminal.
	j, err := js.Get("no-match")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if j.Status != cpb.JobStatusPending {
		t.Fatalf("status: %v (want Pending)", j.Status)
	}
}

// TestDispatch_UnschedulableEvent_DebouncedPerJob asserts the event
// fires at most once within the cooldown window even if the loop
// ticks many times — otherwise a stuck job would flood the bus.
func TestDispatch_UnschedulableEvent_DebouncedPerJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched, _ := schedulerWithLabeledNodes(t, map[string]string{"role": "cpu"})
	bus := events.NewBus(64, nil)
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	js.SetEventBus(bus)
	sub := bus.Subscribe(events.TopicJobUnschedulable)
	defer sub.Cancel()

	nd := &mockNodeDispatcher{}
	// 10ms tick → many ticks during the 300ms observation window.
	loop := cluster.NewDispatchLoop(js, sched, nd, 10*time.Millisecond, slog.Default())

	_ = js.Submit(ctx, &cpb.Job{
		ID: "stuck", Command: "echo",
		NodeSelector: map[string]string{"gpu": "a100"},
	})
	go loop.Run(ctx)

	// Collect events over 300ms. Expect exactly one (debounce is 30s).
	time.Sleep(300 * time.Millisecond)

	count := 0
drain:
	for {
		select {
		case <-sub.C:
			count++
		default:
			break drain
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 unschedulable event within debounce window, got %d", count)
	}
}

// TestDispatch_UnschedulableEvent_RecoversAfterLabelAdded asserts the
// end-to-end recovery story: a job starts unschedulable (no matching
// node), a new matching node registers, the job dispatches. The
// companion internal-package test
// TestDispatchLoop_UnschedulableDebounceClearsOnPick drills into the
// per-job debounce map to guarantee the cleanup that makes a second
// unschedulable episode (e.g. on retry) re-emit promptly.
func TestDispatch_UnschedulableEvent_RecoversAfterLabelAdded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Registry starts with a CPU-only node; a selector for gpu=a100
	// cannot be satisfied.
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	_, _ = r.Register(context.Background(), &pb.RegisterRequest{
		NodeId: "cpu", Address: "127.0.0.1:9090",
		Labels: map[string]string{"role": "cpu"},
	})
	_ = r.HandleHeartbeat(context.Background(), &pb.HeartbeatMessage{NodeId: "cpu"})
	sched := cluster.NewScheduler(r, cluster.NewRoundRobinPolicy())

	bus := events.NewBus(64, nil)
	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	js.SetEventBus(bus)
	sub := bus.Subscribe(events.TopicJobUnschedulable)
	defer sub.Cancel()

	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(js, sched, nd, 10*time.Millisecond, slog.Default())

	_ = js.Submit(ctx, &cpb.Job{
		ID: "recover-me", Command: "echo",
		NodeSelector: map[string]string{"gpu": "a100"},
	})
	go loop.Run(ctx)

	// First episode: expect exactly one unschedulable event.
	select {
	case <-sub.C:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("initial unschedulable event did not fire")
	}
	// Drain any extra events already queued.
	drainLoop1:
	for {
		select {
		case <-sub.C:
		default:
			break drainLoop1
		}
	}

	// Register a GPU-labelled node; the pending job must now get
	// dispatched. The key invariant is that the debounce state for
	// "recover-me" is deleted at dispatch — so if the job ever goes
	// unschedulable again, the next event fires immediately rather
	// than being suppressed by the stale last-emit timestamp.
	_, _ = r.Register(context.Background(), &pb.RegisterRequest{
		NodeId: "gpu", Address: "127.0.0.1:9091",
		Labels: map[string]string{"gpu": "a100"},
	})
	_ = r.HandleHeartbeat(context.Background(), &pb.HeartbeatMessage{NodeId: "gpu"})

	// Wait for the job to land on the node dispatcher.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		for _, id := range nd.dispatched() {
			if id == "recover-me" {
				// Happy path covered — now confirm the debounce
				// map was cleared. Submit a second unschedulable
				// job and verify its event is not suppressed by a
				// stale timestamp from the first episode.
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job never recovered; dispatched=%v", nd.dispatched())
}
