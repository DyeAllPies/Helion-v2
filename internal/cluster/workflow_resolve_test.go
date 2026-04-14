package cluster_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// fakeLookup satisfies cluster.JobLookup for unit tests — no BadgerDB
// or in-memory JobStore needed.
type fakeLookup map[string]*cpb.Job

func (f fakeLookup) Get(id string) (*cpb.Job, error) {
	j, ok := f[id]
	if !ok {
		return nil, fmt.Errorf("fakeLookup: %q not found", id)
	}
	return j, nil
}

// ── short-circuit paths ─────────────────────────────────────────────────

func TestResolveJobInputs_NoFromRefs_ReturnsSamePointer(t *testing.T) {
	job := &cpb.Job{
		ID: "j1",
		Inputs: []cpb.ArtifactBinding{
			{Name: "A", URI: "s3://b/a", LocalPath: "in/a"},
		},
	}
	got, err := cluster.ResolveJobInputs(job, fakeLookup{})
	if err != nil {
		t.Fatalf("ResolveJobInputs: %v", err)
	}
	if got != job {
		t.Fatal("no-op resolve should return the same pointer")
	}
}

func TestResolveJobInputs_NilJob(t *testing.T) {
	if _, err := cluster.ResolveJobInputs(nil, fakeLookup{}); err == nil {
		t.Fatal("nil job should error")
	}
}

func TestResolveJobInputs_NonWorkflowJobWithFrom(t *testing.T) {
	// Validators prevent this at submit, but library callers could
	// still construct it. Must fail-closed.
	job := &cpb.Job{
		ID: "orphan",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", From: "upstream.OUT", LocalPath: "in/x"},
		},
	}
	_, err := cluster.ResolveJobInputs(job, fakeLookup{})
	if !errors.Is(err, cluster.ErrResolveFromNotWorkflow) {
		t.Fatalf("expected ErrResolveFromNotWorkflow, got %v", err)
	}
}

// ── happy path ──────────────────────────────────────────────────────────

func TestResolveJobInputs_HappyPath(t *testing.T) {
	upstream := &cpb.Job{
		ID:     "wf/preprocess",
		Status: cpb.JobStatusCompleted,
		ResolvedOutputs: []cpb.ArtifactOutput{
			{Name: "TRAIN", URI: "s3://b/jobs/wf/preprocess/out/train.parquet"},
		},
	}
	downstream := &cpb.Job{
		ID:         "wf/train",
		WorkflowID: "wf",
		Inputs: []cpb.ArtifactBinding{
			{Name: "DATA", From: "preprocess.TRAIN", LocalPath: "in/train.parquet"},
		},
	}
	got, err := cluster.ResolveJobInputs(downstream, fakeLookup{"wf/preprocess": upstream})
	if err != nil {
		t.Fatalf("ResolveJobInputs: %v", err)
	}
	if got.Inputs[0].URI != "s3://b/jobs/wf/preprocess/out/train.parquet" {
		t.Fatalf("URI not resolved: %q", got.Inputs[0].URI)
	}
	if got.Inputs[0].From != "preprocess.TRAIN" {
		t.Fatalf("From should be preserved for lineage: %q", got.Inputs[0].From)
	}
	// The original job must not have been mutated (defensive copy).
	if downstream.Inputs[0].URI != "" {
		t.Fatalf("original mutated: %q", downstream.Inputs[0].URI)
	}
}

// ── failure modes ───────────────────────────────────────────────────────

func TestResolveJobInputs_UpstreamMissing(t *testing.T) {
	job := &cpb.Job{
		ID:         "wf/b",
		WorkflowID: "wf",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", From: "a.OUT", LocalPath: "in/x"},
		},
	}
	_, err := cluster.ResolveJobInputs(job, fakeLookup{})
	if !errors.Is(err, cluster.ErrResolveUpstreamMissing) {
		t.Fatalf("expected ErrResolveUpstreamMissing, got %v", err)
	}
}

func TestResolveJobInputs_UpstreamNotCompleted(t *testing.T) {
	upstream := &cpb.Job{
		ID:     "wf/a",
		Status: cpb.JobStatusRunning,
	}
	job := &cpb.Job{
		ID:         "wf/b",
		WorkflowID: "wf",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", From: "a.OUT", LocalPath: "in/x"},
		},
	}
	_, err := cluster.ResolveJobInputs(job, fakeLookup{"wf/a": upstream})
	if !errors.Is(err, cluster.ErrResolveUpstreamNotCompleted) {
		t.Fatalf("expected ErrResolveUpstreamNotCompleted, got %v", err)
	}
}

func TestResolveJobInputs_OutputMissing(t *testing.T) {
	// Upstream completed but its ResolvedOutputs has a different name.
	// Common scenario: node crashed mid-upload and attestOutputs
	// dropped the declared output.
	upstream := &cpb.Job{
		ID:     "wf/a",
		Status: cpb.JobStatusCompleted,
		ResolvedOutputs: []cpb.ArtifactOutput{
			{Name: "OTHER", URI: "s3://b/jobs/wf/a/out/other"},
		},
	}
	job := &cpb.Job{
		ID:         "wf/b",
		WorkflowID: "wf",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", From: "a.EXPECTED", LocalPath: "in/x"},
		},
	}
	_, err := cluster.ResolveJobInputs(job, fakeLookup{"wf/a": upstream})
	if !errors.Is(err, cluster.ErrResolveOutputMissing) {
		t.Fatalf("expected ErrResolveOutputMissing, got %v", err)
	}
}

func TestResolveJobInputs_MixedURIAndFrom(t *testing.T) {
	// A job can mix plain URI inputs with From references — only the
	// From entries need resolving.
	upstream := &cpb.Job{
		ID:     "wf/a",
		Status: cpb.JobStatusCompleted,
		ResolvedOutputs: []cpb.ArtifactOutput{
			{Name: "OUT", URI: "s3://b/jobs/wf/a/out/o"},
		},
	}
	job := &cpb.Job{
		ID:         "wf/b",
		WorkflowID: "wf",
		Inputs: []cpb.ArtifactBinding{
			{Name: "RESOLVED", From: "a.OUT", LocalPath: "in/r"},
			{Name: "FIXED", URI: "s3://b/ds/fixed", LocalPath: "in/f"},
		},
	}
	got, err := cluster.ResolveJobInputs(job, fakeLookup{"wf/a": upstream})
	if err != nil {
		t.Fatalf("ResolveJobInputs: %v", err)
	}
	if got.Inputs[0].URI != "s3://b/jobs/wf/a/out/o" {
		t.Fatalf("resolved URI wrong: %q", got.Inputs[0].URI)
	}
	if got.Inputs[1].URI != "s3://b/ds/fixed" {
		t.Fatalf("fixed URI clobbered: %q", got.Inputs[1].URI)
	}
}

func TestResolveJobInputs_MultipleFromRefs(t *testing.T) {
	a := &cpb.Job{
		ID:     "wf/a",
		Status: cpb.JobStatusCompleted,
		ResolvedOutputs: []cpb.ArtifactOutput{
			{Name: "OUT", URI: "s3://b/jobs/wf/a/out/o"},
		},
	}
	b := &cpb.Job{
		ID:     "wf/b",
		Status: cpb.JobStatusCompleted,
		ResolvedOutputs: []cpb.ArtifactOutput{
			{Name: "OUT", URI: "s3://b/jobs/wf/b/out/o"},
		},
	}
	job := &cpb.Job{
		ID:         "wf/c",
		WorkflowID: "wf",
		Inputs: []cpb.ArtifactBinding{
			{Name: "FROM_A", From: "a.OUT", LocalPath: "in/a"},
			{Name: "FROM_B", From: "b.OUT", LocalPath: "in/b"},
		},
	}
	got, err := cluster.ResolveJobInputs(job, fakeLookup{"wf/a": a, "wf/b": b})
	if err != nil {
		t.Fatalf("ResolveJobInputs: %v", err)
	}
	if got.Inputs[0].URI != "s3://b/jobs/wf/a/out/o" ||
		got.Inputs[1].URI != "s3://b/jobs/wf/b/out/o" {
		t.Fatalf("resolution mismatch: %+v", got.Inputs)
	}
}
