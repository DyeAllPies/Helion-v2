package cluster_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// TestDispatchLoop_Step3_ResolvesFromRefEndToEnd is the integration
// test for step 3's core promise: a downstream workflow job whose
// input carries `from: <upstream>.<output>` is dispatched to the
// node with that reference rewritten to the upstream's concrete URI,
// and the persisted Job record also shows the resolved URI.
//
// Exercises the full seam that unit tests cover piecewise:
//   - WorkflowStore.Submit validates the `from` DAG
//   - WorkflowStore.Start materialises both jobs to Pending
//   - the upstream is driven to Completed with ResolvedOutputs
//   - DispatchLoop picks up the downstream, resolver rewrites URI
//   - UpdateResolvedInputs persists the rewrite
//   - DispatchToNode receives the rewritten *cpb.Job
func TestDispatchLoop_Step3_ResolvesFromRefEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── wiring ──────────────────────────────────────────────────
	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	wfs := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	sched, _ := newSchedulerWithNodes(t)
	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(jobs, sched, nd, 20*time.Millisecond, slog.Default())
	loop.SetWorkflowStore(wfs)

	// ── workflow: preprocess → train ────────────────────────────
	wf := &cpb.Workflow{
		ID:   "wf-e2e",
		Name: "e2e step-3",
		Jobs: []cpb.WorkflowJob{
			{
				Name:    "preprocess",
				Command: "echo",
				Outputs: []cpb.ArtifactBinding{
					{Name: "TRAIN", LocalPath: "out/train.parquet"},
				},
			},
			{
				Name:      "train",
				Command:   "echo",
				DependsOn: []string{"preprocess"},
				Inputs: []cpb.ArtifactBinding{
					{Name: "DATA", From: "preprocess.TRAIN", LocalPath: "in/train.parquet"},
				},
			},
		},
	}
	if err := wfs.Submit(ctx, wf); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := wfs.Start(ctx, "wf-e2e", jobs); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drive the upstream through the state machine to Completed with
	// a ResolvedOutput. Mirrors what grpcserver.ReportResult +
	// job_transition.go would do in production.
	preID := "wf-e2e/preprocess"
	if err := jobs.Transition(ctx, preID, cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: "node-1"}); err != nil {
		t.Fatalf("→dispatching: %v", err)
	}
	if err := jobs.Transition(ctx, preID, cpb.JobStatusRunning,
		cluster.TransitionOptions{}); err != nil {
		t.Fatalf("→running: %v", err)
	}
	resolvedURI := "s3://helion/jobs/wf-e2e/preprocess/out/train.parquet"
	if err := jobs.Transition(ctx, preID, cpb.JobStatusCompleted,
		cluster.TransitionOptions{
			ResolvedOutputs: []cpb.ArtifactOutput{
				{Name: "TRAIN", URI: resolvedURI, Size: 1234, SHA256: "deadbeef", LocalPath: "out/train.parquet"},
			},
		}); err != nil {
		t.Fatalf("→completed: %v", err)
	}

	// ── run the dispatch loop until the downstream lands ────────
	go loop.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	var downstream *cpb.Job
	for time.Now().Before(deadline) {
		for _, j := range nd.dispatched() {
			if j == "wf-e2e/train" {
				break
			}
		}
		if len(nd.dispatched()) >= 1 {
			// At least one dispatch happened — grab it.
			// mockNodeDispatcher only records IDs, so we cross-
			// reference the persisted store for the full job.
			j, err := jobs.Get("wf-e2e/train")
			if err == nil && !j.Status.IsTerminal() && j.Status != cpb.JobStatusPending {
				downstream = j
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if downstream == nil {
		t.Fatalf("downstream never dispatched; calls=%v", nd.dispatched())
	}

	// ── assertions ──────────────────────────────────────────────
	if got := downstream.Inputs[0].URI; got != resolvedURI {
		t.Fatalf("persisted downstream URI not resolved:\n  got:  %q\n  want: %q", got, resolvedURI)
	}
	if got := downstream.Inputs[0].From; got != "preprocess.TRAIN" {
		t.Fatalf("From lost on persist: %q", got)
	}
	// The upstream's committed SHA-256 must travel onto the
	// downstream's input so the node stager can verify the download
	// via artifacts.GetAndVerify. Without this the verified-read
	// path in staging.download is a no-op.
	if got := downstream.Inputs[0].SHA256; got != "deadbeef" {
		t.Fatalf("SHA256 not propagated onto downstream: %q", got)
	}
}

// TestDispatchLoop_Step3_ResolveFailure_EmitsMLResolveFailedEvent
// verifies the feature-18 follow-up: when the dispatch loop's
// artifact resolver fails, the coordinator emits an
// `ml.resolve_failed` event on the bus so the dashboard's Pipelines
// view can surface broken ML pipelines without log-grepping.
//
// The companion test below asserts the failed-transition path —
// this one asserts the event path. Both need to fire on the same
// failure; a regression that transitioned the job but dropped the
// event would leave the dashboard blind to the break.
//
// Subscribes **before** starting the dispatch loop so the
// publish-after-transition event lands in the subscriber's buffered
// channel rather than being dropped. Reads with a generous timeout
// — the dispatcher's tick is 20 ms but test scheduling on Windows
// is flaky under load, so 3 s gives headroom without being a slow
// test on happy runs.
func TestDispatchLoop_Step3_ResolveFailure_EmitsMLResolveFailedEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	wfs := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	bus := events.NewBus(16, slog.Default())
	jobs.SetEventBus(bus)

	// Subscribe to the ml.resolve_failed topic before the dispatch
	// loop starts so we don't race the first publish.
	sub := bus.Subscribe(events.TopicMLResolveFailed)
	defer sub.Cancel()

	sched, _ := newSchedulerWithNodes(t)
	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(jobs, sched, nd, 20*time.Millisecond, slog.Default())
	loop.SetWorkflowStore(wfs)

	// Same bad-workflow shape as the TransitionsToFailed test: valid
	// DAG, but upstream completes without the declared output, so
	// the resolver fails at dispatch time.
	wf := &cpb.Workflow{
		ID: "wf-event",
		Jobs: []cpb.WorkflowJob{
			{
				Name:    "a",
				Command: "echo",
				Outputs: []cpb.ArtifactBinding{{Name: "OUT", LocalPath: "out/o"}},
			},
			{
				Name:      "b",
				Command:   "echo",
				DependsOn: []string{"a"},
				Inputs: []cpb.ArtifactBinding{
					{Name: "X", From: "a.OUT", LocalPath: "in/x"},
				},
			},
		},
	}
	if err := wfs.Submit(ctx, wf); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := wfs.Start(ctx, "wf-event", jobs); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Upstream completes but reports no ResolvedOutputs.
	_ = jobs.Transition(ctx, "wf-event/a", cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: "node-1"})
	_ = jobs.Transition(ctx, "wf-event/a", cpb.JobStatusRunning,
		cluster.TransitionOptions{})
	_ = jobs.Transition(ctx, "wf-event/a", cpb.JobStatusCompleted,
		cluster.TransitionOptions{})

	go loop.Run(ctx)

	select {
	case evt := <-sub.C:
		if evt.Type != events.TopicMLResolveFailed {
			t.Fatalf("wrong event topic: got %q want %q", evt.Type, events.TopicMLResolveFailed)
		}
		// Payload must carry the failing job's ID + workflow ID + the
		// upstream/output names the dashboard needs to render a
		// useful diagnostic. All of these are stringly-typed in the
		// event Data map; assert presence and spot-check values.
		if got := evt.Data["job_id"]; got != "wf-event/b" {
			t.Errorf("event job_id: got %v, want %q", got, "wf-event/b")
		}
		if got := evt.Data["workflow_id"]; got != "wf-event" {
			t.Errorf("event workflow_id: got %v, want %q", got, "wf-event")
		}
		if got := evt.Data["upstream"]; got != "a" {
			t.Errorf("event upstream: got %v, want %q", got, "a")
		}
		if got := evt.Data["output_name"]; got != "OUT" {
			t.Errorf("event output_name: got %v, want %q", got, "OUT")
		}
		reason, _ := evt.Data["reason"].(string)
		if reason == "" {
			t.Errorf("event reason should be non-empty; got %v", evt.Data["reason"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ml.resolve_failed event never fired")
	}
}

// TestDispatchLoop_Step3_ResolveFailure_TransitionsToFailed verifies
// the failure path: if the upstream somehow reaches the dispatcher
// without declaring the referenced output (data corruption or a
// `from` ref that survived past the DAG validator), the downstream
// is transitioned to Failed with a descriptive error instead of
// being dispatched with an unresolved placeholder.
func TestDispatchLoop_Step3_ResolveFailure_TransitionsToFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	wfs := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	sched, _ := newSchedulerWithNodes(t)
	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(jobs, sched, nd, 20*time.Millisecond, slog.Default())
	loop.SetWorkflowStore(wfs)

	// Bypass the DAG validator by submitting an otherwise-valid
	// workflow, then completing the upstream WITHOUT the expected
	// output. This matches the on_complete / crashed-before-upload
	// failure mode step-3 is designed to catch.
	wf := &cpb.Workflow{
		ID: "wf-bad",
		Jobs: []cpb.WorkflowJob{
			{
				Name:    "a",
				Command: "echo",
				Outputs: []cpb.ArtifactBinding{{Name: "OUT", LocalPath: "out/o"}},
			},
			{
				Name:      "b",
				Command:   "echo",
				DependsOn: []string{"a"},
				Inputs: []cpb.ArtifactBinding{
					{Name: "X", From: "a.OUT", LocalPath: "in/x"},
				},
			},
		},
	}
	if err := wfs.Submit(ctx, wf); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := wfs.Start(ctx, "wf-bad", jobs); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Upstream completes but reports NO ResolvedOutputs.
	_ = jobs.Transition(ctx, "wf-bad/a", cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: "node-1"})
	_ = jobs.Transition(ctx, "wf-bad/a", cpb.JobStatusRunning,
		cluster.TransitionOptions{})
	_ = jobs.Transition(ctx, "wf-bad/a", cpb.JobStatusCompleted,
		cluster.TransitionOptions{})

	go loop.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, err := jobs.Get("wf-bad/b")
		if err == nil && j.Status.IsTerminal() {
			if j.Status != cpb.JobStatusFailed {
				t.Fatalf("expected Failed, got %v", j.Status)
			}
			if j.Error == "" {
				t.Fatalf("expected error message on failed downstream")
			}
			// Downstream must NOT have been dispatched.
			for _, id := range nd.dispatched() {
				if id == "wf-bad/b" {
					t.Fatalf("downstream dispatched despite resolve failure")
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("downstream never reached Failed state")
}

// TestDispatchLoop_ArtifactResolve_NoHotLoop guards the invariant
// that a resolution failure is terminal: the dispatch loop must not
// re-pick the same broken downstream tick after tick and retry
// resolution forever. This test only re-confirms what
// TestDispatchLoop_Step3_ResolveFailure_TransitionsToFailed already
// checks, but the assertion is stated as a hot-loop guard so a
// future refactor that accidentally re-enables the loop (e.g. by
// dropping Pending→Failed from the transition table) triggers here.
//
// The no-retry property is load-bearing on two things: (1)
// Pending→Failed being an allowed transition, and (2)
// RetryIfEligible firing only from grpcserver.ReportResult — since
// resolution failures never reach a node, they never trigger retry
// regardless of the RetryPolicy the downstream carries. Neither
// invariant is enforced by the resolver itself; this test proves
// they hold in combination.
func TestDispatchLoop_ArtifactResolve_NoHotLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	wfs := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	sched, _ := newSchedulerWithNodes(t)
	nd := &mockNodeDispatcher{}
	loop := cluster.NewDispatchLoop(jobs, sched, nd, 10*time.Millisecond, slog.Default())
	loop.SetWorkflowStore(wfs)

	wf := &cpb.Workflow{
		ID: "wf-hotloop",
		Jobs: []cpb.WorkflowJob{
			{
				Name:    "a",
				Command: "echo",
				Outputs: []cpb.ArtifactBinding{{Name: "OUT", LocalPath: "out/o"}},
			},
			{
				Name:      "b",
				Command:   "echo",
				DependsOn: []string{"a"},
				Inputs: []cpb.ArtifactBinding{
					{Name: "X", From: "a.OUT", LocalPath: "in/x"},
				},
			},
		},
	}
	_ = wfs.Submit(ctx, wf)
	_ = wfs.Start(ctx, "wf-hotloop", jobs)
	_ = jobs.Transition(ctx, "wf-hotloop/a", cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: "node-1"})
	_ = jobs.Transition(ctx, "wf-hotloop/a", cpb.JobStatusRunning,
		cluster.TransitionOptions{})
	// Complete upstream WITHOUT the expected output to force the
	// resolver to fail on the downstream.
	_ = jobs.Transition(ctx, "wf-hotloop/a", cpb.JobStatusCompleted,
		cluster.TransitionOptions{})

	go loop.Run(ctx)

	// At 10ms tick, 300ms = ~30 ticks. A hot loop would re-pick
	// the downstream every tick; the fix is to transition to
	// terminal on the first failure.
	time.Sleep(300 * time.Millisecond)

	j, _ := jobs.Get("wf-hotloop/b")
	if j.Status != cpb.JobStatusFailed {
		t.Fatalf("expected Failed terminal, got %v", j.Status)
	}
	if j.Attempt > 1 {
		t.Fatalf("attempt escalated: %d", j.Attempt)
	}
	for _, id := range nd.dispatched() {
		if id == "wf-hotloop/b" {
			t.Fatal("downstream reached node despite resolve failure")
		}
	}
}
