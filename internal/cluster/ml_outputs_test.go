package cluster_test

import (
	"context"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ml_outputs_test.go covers the step-2 "resolved outputs" flow on the
// coordinator side: a completed transition persists ResolvedOutputs on
// the Job record, and the emitted job.completed event carries them.

func newStoreWithBus(t *testing.T) (*cluster.JobStore, *events.Bus) {
	t.Helper()
	p := cluster.NewMemJobPersister()
	bus := events.NewBus(16, nil)
	s := cluster.NewJobStore(p, nil)
	s.SetEventBus(bus)
	return s, bus
}

func seedRunning(t *testing.T, s *cluster.JobStore, jobID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.Submit(ctx, &cpb.Job{ID: jobID, Command: "true"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := s.Transition(ctx, jobID, cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: "node-1"}); err != nil {
		t.Fatalf("→dispatching: %v", err)
	}
	if err := s.Transition(ctx, jobID, cpb.JobStatusRunning,
		cluster.TransitionOptions{}); err != nil {
		t.Fatalf("→running: %v", err)
	}
}

// TestTransition_Completed_PersistsResolvedOutputs asserts the
// coordinator copies ResolvedOutputs onto the Job record when a
// successful terminal transition is reported.
func TestTransition_Completed_PersistsResolvedOutputs(t *testing.T) {
	s, _ := newStoreWithBus(t)
	seedRunning(t, s, "job-ok")

	outs := []cpb.ArtifactOutput{
		{Name: "MODEL", URI: "s3://b/jobs/job-ok/out/model.pt", Size: 1024, SHA256: "deadbeef", LocalPath: "out/model.pt"},
	}
	if err := s.Transition(context.Background(), "job-ok", cpb.JobStatusCompleted,
		cluster.TransitionOptions{ResolvedOutputs: outs}); err != nil {
		t.Fatalf("→completed: %v", err)
	}

	got, err := s.Get("job-ok")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.ResolvedOutputs) != 1 {
		t.Fatalf("resolved outputs: %+v", got.ResolvedOutputs)
	}
	if got.ResolvedOutputs[0].URI != outs[0].URI ||
		got.ResolvedOutputs[0].Name != "MODEL" ||
		got.ResolvedOutputs[0].SHA256 != "deadbeef" {
		t.Fatalf("mismatch: %+v", got.ResolvedOutputs[0])
	}
}

// TestTransition_Completed_OutputsIndependentOfCallerSlice guards the
// defensive copy: mutating the caller's slice post-transition must not
// affect the persisted Job.
func TestTransition_Completed_OutputsIndependentOfCallerSlice(t *testing.T) {
	s, _ := newStoreWithBus(t)
	seedRunning(t, s, "job-copy")

	outs := []cpb.ArtifactOutput{{Name: "X", URI: "s3://b/x"}}
	if err := s.Transition(context.Background(), "job-copy", cpb.JobStatusCompleted,
		cluster.TransitionOptions{ResolvedOutputs: outs}); err != nil {
		t.Fatalf("→completed: %v", err)
	}
	// Mutate the caller's slice — the persisted copy must not change.
	outs[0].URI = "s3://attacker/"

	got, _ := s.Get("job-copy")
	if got.ResolvedOutputs[0].URI != "s3://b/x" {
		t.Fatalf("defensive copy violated: %q", got.ResolvedOutputs[0].URI)
	}
}

// TestTransition_Failed_DoesNotRecordOutputs asserts that a failed
// terminal transition ignores opts.ResolvedOutputs (the node's stager
// skips uploads on failure — the coordinator must match).
func TestTransition_Failed_DoesNotRecordOutputs(t *testing.T) {
	s, _ := newStoreWithBus(t)
	seedRunning(t, s, "job-fail")

	outs := []cpb.ArtifactOutput{{Name: "MAYBE", URI: "s3://b/whatever"}}
	if err := s.Transition(context.Background(), "job-fail", cpb.JobStatusFailed,
		cluster.TransitionOptions{ErrMsg: "boom", ResolvedOutputs: outs}); err != nil {
		t.Fatalf("→failed: %v", err)
	}
	got, _ := s.Get("job-fail")
	if len(got.ResolvedOutputs) != 0 {
		t.Fatalf("failed job should not have outputs: %+v", got.ResolvedOutputs)
	}
}

// TestTransition_Completed_EmitsOutputsOnEvent verifies the
// job.completed event payload includes an "outputs" array when the
// job produced artifacts.
func TestTransition_Completed_EmitsOutputsOnEvent(t *testing.T) {
	s, bus := newStoreWithBus(t)
	sub := bus.Subscribe(events.TopicJobCompleted)
	defer sub.Cancel()

	seedRunning(t, s, "job-evt")

	outs := []cpb.ArtifactOutput{
		{Name: "A", URI: "s3://b/a", SHA256: "aaa"},
		{Name: "B", URI: "s3://b/b"},
	}
	if err := s.Transition(context.Background(), "job-evt", cpb.JobStatusCompleted,
		cluster.TransitionOptions{ResolvedOutputs: outs}); err != nil {
		t.Fatalf("→completed: %v", err)
	}

	evt := <-sub.C
	raw, ok := evt.Data["outputs"]
	if !ok {
		t.Fatalf("event missing outputs: %+v", evt.Data)
	}
	rows, ok := raw.([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("unexpected outputs shape: %T %+v", raw, raw)
	}
	if rows[0]["name"] != "A" || rows[0]["uri"] != "s3://b/a" || rows[0]["sha256"] != "aaa" {
		t.Fatalf("row 0: %+v", rows[0])
	}
	// B has no sha256 — the key must be omitted, not set to empty.
	if _, has := rows[1]["sha256"]; has {
		t.Fatalf("row 1 should not have sha256 key: %+v", rows[1])
	}
}

// TestTransition_Completed_NoOutputs_UsesLegacyEvent asserts that jobs
// without declared outputs still emit the lean legacy event shape (no
// "outputs" key), so existing analytics subscribers stay unchanged.
func TestTransition_Completed_NoOutputs_UsesLegacyEvent(t *testing.T) {
	s, bus := newStoreWithBus(t)
	sub := bus.Subscribe(events.TopicJobCompleted)
	defer sub.Cancel()

	seedRunning(t, s, "job-legacy")
	if err := s.Transition(context.Background(), "job-legacy", cpb.JobStatusCompleted,
		cluster.TransitionOptions{}); err != nil {
		t.Fatalf("→completed: %v", err)
	}

	evt := <-sub.C
	if _, has := evt.Data["outputs"]; has {
		t.Fatalf("legacy event should not carry outputs: %+v", evt.Data)
	}
}
