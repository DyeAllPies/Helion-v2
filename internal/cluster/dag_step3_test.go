package cluster_test

import (
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── DAG validation: From references ─────────────────────────────────────

func TestValidateDAG_FromRef_HappyPath(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{
			Name:    "preprocess",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "TRAIN", LocalPath: "out/train.parquet"}},
		},
		{
			Name:      "train",
			Command:   "echo",
			DependsOn: []string{"preprocess"},
			Inputs: []cpb.ArtifactBinding{
				{Name: "DATA", From: "preprocess.TRAIN", LocalPath: "in/train.parquet"},
			},
		},
	}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("expected valid DAG: %v", err)
	}
}

func TestValidateDAG_FromRef_TransitiveAncestor(t *testing.T) {
	// C → B → A; C.Inputs references A.OUT directly. A is a
	// transitive ancestor via B, so the DAG is valid.
	jobs := []cpb.WorkflowJob{
		{
			Name:    "ingest",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "RAW", LocalPath: "out/raw"}},
		},
		{
			Name:      "preprocess",
			Command:   "echo",
			DependsOn: []string{"ingest"},
		},
		{
			Name:      "train",
			Command:   "echo",
			DependsOn: []string{"preprocess"},
			Inputs: []cpb.ArtifactBinding{
				{Name: "DATA", From: "ingest.RAW", LocalPath: "in/raw"},
			},
		},
	}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("transitive ancestor should validate: %v", err)
	}
}

func TestValidateDAG_FromRef_UnknownUpstream(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{
			Name:    "train",
			Command: "echo",
			Inputs: []cpb.ArtifactBinding{
				{Name: "DATA", From: "nonexistent.OUT", LocalPath: "in/d"},
			},
		},
	}
	err := cluster.ValidateDAG(jobs)
	if !errors.Is(err, cluster.ErrDAGUnknownFrom) {
		t.Fatalf("expected ErrDAGUnknownFrom, got %v", err)
	}
}

func TestValidateDAG_FromRef_NonAncestor(t *testing.T) {
	// Two independent jobs; `train` references `preprocess.TRAIN` but
	// does not list it in DependsOn — scheduling order undefined.
	jobs := []cpb.WorkflowJob{
		{
			Name:    "preprocess",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "TRAIN", LocalPath: "out/t"}},
		},
		{
			Name:    "train",
			Command: "echo",
			Inputs: []cpb.ArtifactBinding{
				{Name: "DATA", From: "preprocess.TRAIN", LocalPath: "in/t"},
			},
		},
	}
	err := cluster.ValidateDAG(jobs)
	if !errors.Is(err, cluster.ErrDAGFromNotAncestor) {
		t.Fatalf("expected ErrDAGFromNotAncestor, got %v", err)
	}
}

func TestValidateDAG_FromRef_UnknownOutputName(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{
			Name:    "preprocess",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "TRAIN", LocalPath: "out/t"}},
		},
		{
			Name:      "train",
			Command:   "echo",
			DependsOn: []string{"preprocess"},
			Inputs: []cpb.ArtifactBinding{
				// Upstream exists + is an ancestor, but declares no
				// output named VAL.
				{Name: "DATA", From: "preprocess.VAL", LocalPath: "in/v"},
			},
		},
	}
	err := cluster.ValidateDAG(jobs)
	if !errors.Is(err, cluster.ErrDAGFromUnknownOutput) {
		t.Fatalf("expected ErrDAGFromUnknownOutput, got %v", err)
	}
}

func TestValidateDAG_FromRef_RejectsMalformedShape(t *testing.T) {
	// Library-layer defence: API validator should have caught this,
	// but a bypass must still be rejected at the DAG layer.
	jobs := []cpb.WorkflowJob{
		{Name: "a", Command: "echo"},
		{
			Name:      "b",
			Command:   "echo",
			DependsOn: []string{"a"},
			Inputs: []cpb.ArtifactBinding{
				{Name: "X", From: "no-dot", LocalPath: "in/x"},
			},
		},
	}
	if err := cluster.ValidateDAG(jobs); err == nil {
		t.Fatal("expected malformed-shape rejection")
	}
}

// TestValidateDAG_FromRef_CycleStillDetected asserts the new ancestor
// walk does not mask cycle detection — a reference test that guards
// ordering of the checks inside ValidateDAG.
func TestValidateDAG_FromRef_CycleStillDetected(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "a", Command: "echo", DependsOn: []string{"b"}},
		{Name: "b", Command: "echo", DependsOn: []string{"a"}},
	}
	if err := cluster.ValidateDAG(jobs); !errors.Is(err, cluster.ErrDAGCycle) {
		t.Fatalf("expected ErrDAGCycle, got %v", err)
	}
}
