package cluster_test

import (
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Step-2 rule: the stager only uploads outputs on successful runs.
// A downstream job that uses `from: X.OUT` but runs under on_failure
// or on_complete can therefore never resolve — the upstream's outputs
// never landed in ResolvedOutputs. Reject at submit.

func TestValidateDAG_FromRef_RejectedUnderOnFailure(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{
			Name:    "train",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "MODEL", LocalPath: "out/m"}},
		},
		{
			Name:      "cleanup",
			Command:   "echo",
			DependsOn: []string{"train"},
			Condition: cpb.DependencyOnFailure,
			Inputs: []cpb.ArtifactBinding{
				{Name: "M", From: "train.MODEL", LocalPath: "in/m"},
			},
		},
	}
	err := cluster.ValidateDAG(jobs)
	if !errors.Is(err, cluster.ErrDAGFromConditionUnreachable) {
		t.Fatalf("expected ErrDAGFromConditionUnreachable, got %v", err)
	}
}

func TestValidateDAG_FromRef_RejectedUnderOnComplete(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{
			Name:    "train",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "MODEL", LocalPath: "out/m"}},
		},
		{
			Name:      "audit",
			Command:   "echo",
			DependsOn: []string{"train"},
			Condition: cpb.DependencyOnComplete,
			Inputs: []cpb.ArtifactBinding{
				{Name: "M", From: "train.MODEL", LocalPath: "in/m"},
			},
		},
	}
	err := cluster.ValidateDAG(jobs)
	if !errors.Is(err, cluster.ErrDAGFromConditionUnreachable) {
		t.Fatalf("expected ErrDAGFromConditionUnreachable, got %v", err)
	}
}

func TestValidateDAG_FromRef_OnSuccessStillAllowed(t *testing.T) {
	// Default condition is on_success; the happy-path case must
	// continue to work after the condition gate was added.
	jobs := []cpb.WorkflowJob{
		{
			Name:    "train",
			Command: "echo",
			Outputs: []cpb.ArtifactBinding{{Name: "MODEL", LocalPath: "out/m"}},
		},
		{
			Name:      "eval",
			Command:   "echo",
			DependsOn: []string{"train"},
			// Condition left zero == DependencyOnSuccess.
			Inputs: []cpb.ArtifactBinding{
				{Name: "M", From: "train.MODEL", LocalPath: "in/m"},
			},
		},
	}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("on_success + from should validate: %v", err)
	}
}

// A downstream with Condition on_failure but no `from:` refs is fine —
// it just doesn't use any upstream output.
func TestValidateDAG_OnFailure_WithoutFromRef_Allowed(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "train", Command: "echo"},
		{
			Name:      "cleanup",
			Command:   "echo",
			DependsOn: []string{"train"},
			Condition: cpb.DependencyOnFailure,
		},
	}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
