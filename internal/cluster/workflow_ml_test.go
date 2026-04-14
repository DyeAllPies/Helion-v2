package cluster_test

import (
	"context"
	"testing"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// TestWorkflowStart_CopiesMLFieldsToMaterializedJob verifies that the
// step-2 fields (working_dir, inputs, outputs, node_selector) flow
// verbatim from each WorkflowJob template onto the materialised Job
// record. Step 3 builds on this by rewriting Inputs[i].URI before
// calling Submit.
func TestWorkflowStart_CopiesMLFieldsToMaterializedJob(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	jobs := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID:   "wf-ml",
		Name: "ml pipeline",
		Jobs: []cpb.WorkflowJob{
			{
				Name:         "preprocess",
				Command:      "pythn", // placeholder; validators live in API layer
				WorkingDir:   "work-pre",
				Outputs:      []cpb.ArtifactBinding{{Name: "TRAIN", LocalPath: "out/train.parquet"}},
				NodeSelector: map[string]string{"role": "cpu"},
			},
			{
				Name:      "train",
				Command:   "pythn",
				DependsOn: []string{"preprocess"},
				Inputs: []cpb.ArtifactBinding{
					{Name: "TRAIN_DATA", URI: "s3://b/placeholder", LocalPath: "in/train.parquet"},
				},
				Outputs:      []cpb.ArtifactBinding{{Name: "MODEL", LocalPath: "out/model.pt"}},
				NodeSelector: map[string]string{"gpu": "a100"},
			},
		},
	}
	if err := ws.Submit(ctx, wf); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := ws.Start(ctx, "wf-ml", jobs); err != nil {
		t.Fatalf("Start: %v", err)
	}

	pre, err := jobs.Get("wf-ml/preprocess")
	if err != nil {
		t.Fatalf("Get preprocess: %v", err)
	}
	if pre.WorkingDir != "work-pre" {
		t.Errorf("preprocess WorkingDir: %q", pre.WorkingDir)
	}
	if len(pre.Outputs) != 1 || pre.Outputs[0].Name != "TRAIN" {
		t.Errorf("preprocess outputs: %+v", pre.Outputs)
	}
	if pre.NodeSelector["role"] != "cpu" {
		t.Errorf("preprocess NodeSelector: %+v", pre.NodeSelector)
	}
	if len(pre.Inputs) != 0 {
		t.Errorf("preprocess should have no inputs: %+v", pre.Inputs)
	}

	train, err := jobs.Get("wf-ml/train")
	if err != nil {
		t.Fatalf("Get train: %v", err)
	}
	if len(train.Inputs) != 1 || train.Inputs[0].Name != "TRAIN_DATA" {
		t.Errorf("train inputs: %+v", train.Inputs)
	}
	if train.Inputs[0].URI != "s3://b/placeholder" {
		t.Errorf("train input URI: %q", train.Inputs[0].URI)
	}
	if train.NodeSelector["gpu"] != "a100" {
		t.Errorf("train NodeSelector: %+v", train.NodeSelector)
	}
}

// TestWorkflowStart_NoMLFields_Unchanged asserts workflows without
// any step-2 fields produce Jobs with empty ML slices (backward
// compatibility with pre-step-2 workflow definitions).
func TestWorkflowStart_NoMLFields_Unchanged(t *testing.T) {
	ws, _ := newTestWorkflowStore()
	jobs := newTestJobStore()
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID:   "wf-legacy",
		Name: "legacy",
		Jobs: []cpb.WorkflowJob{
			{Name: "a", Command: "true"},
			{Name: "b", Command: "true", DependsOn: []string{"a"}},
		},
	}
	if err := ws.Submit(ctx, wf); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := ws.Start(ctx, "wf-legacy", jobs); err != nil {
		t.Fatalf("Start: %v", err)
	}
	a, _ := jobs.Get("wf-legacy/a")
	if a.WorkingDir != "" || len(a.Inputs) != 0 || len(a.Outputs) != 0 || len(a.NodeSelector) != 0 {
		t.Fatalf("legacy job should have empty ML fields: %+v", a)
	}
}
