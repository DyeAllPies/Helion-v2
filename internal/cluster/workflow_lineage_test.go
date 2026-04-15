package cluster_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

// ── Fakes ────────────────────────────────────────────────────────────────────

type fakeWorkflowReader struct {
	wf  *cpb.Workflow
	err error
}

func (f *fakeWorkflowReader) Get(_ string) (*cpb.Workflow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.wf, nil
}

type fakeJobReader struct {
	jobs map[string]*cpb.Job
}

func (f *fakeJobReader) Get(id string) (*cpb.Job, error) {
	j, ok := f.jobs[id]
	if !ok {
		return nil, errors.New("job not found")
	}
	return j, nil
}

type fakeModelReader struct {
	byJob map[string][]*registry.Model
	err   error
}

func (f *fakeModelReader) ListBySourceJob(_ context.Context, src string) ([]*registry.Model, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byJob[src], nil
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestBuildWorkflowLineage_HappyPath(t *testing.T) {
	wf := &cpb.Workflow{
		ID:     "wf-1",
		Name:   "train-and-serve",
		Status: cpb.WorkflowStatusRunning,
		Jobs: []cpb.WorkflowJob{
			{
				Name: "train", Command: "python", JobID: "wf-1/train",
			},
			{
				Name: "serve", Command: "python", JobID: "wf-1/serve",
				DependsOn: []string{"train"},
				Inputs: []cpb.ArtifactBinding{
					{Name: "CHECKPOINT", From: "train.MODEL", LocalPath: "model.pt"},
				},
			},
		},
	}
	jobs := &fakeJobReader{jobs: map[string]*cpb.Job{
		"wf-1/train": {
			ID: "wf-1/train", Status: cpb.JobStatusCompleted,
			ResolvedOutputs: []cpb.ArtifactOutput{
				{Name: "MODEL", URI: "s3://b/model.pt", Size: 1024, SHA256: "deadbeef"},
			},
		},
		"wf-1/serve": {ID: "wf-1/serve", Status: cpb.JobStatusRunning},
	}}
	models := &fakeModelReader{byJob: map[string][]*registry.Model{
		"wf-1/train": {{Name: "resnet", Version: "v1"}},
	}}

	got, err := cluster.BuildWorkflowLineage(
		context.Background(), "wf-1",
		&fakeWorkflowReader{wf: wf}, jobs, models,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.WorkflowID != "wf-1" || got.Name != "train-and-serve" {
		t.Fatalf("header mismatch: %+v", got)
	}
	if len(got.Jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(got.Jobs))
	}

	// First job: completed, outputs + produced model.
	train := got.Jobs[0]
	if train.Status != "completed" || len(train.Outputs) != 1 {
		t.Errorf("train: %+v", train)
	}
	if len(train.ModelsProduced) != 1 || train.ModelsProduced[0].Name != "resnet" {
		t.Errorf("train models: %+v", train.ModelsProduced)
	}

	// Second job: running, no outputs, dep edge.
	serve := got.Jobs[1]
	if serve.Status != "running" {
		t.Errorf("serve status: %q", serve.Status)
	}
	if len(serve.DependsOn) != 1 || serve.DependsOn[0] != "train" {
		t.Errorf("serve depends_on: %v", serve.DependsOn)
	}

	// Artifact edge from train.MODEL → serve.CHECKPOINT.
	if len(got.ArtifactEdges) != 1 {
		t.Fatalf("want 1 artifact edge, got %d", len(got.ArtifactEdges))
	}
	edge := got.ArtifactEdges[0]
	if edge.FromJob != "train" || edge.FromOutput != "MODEL" ||
		edge.ToJob != "serve" || edge.ToInput != "CHECKPOINT" {
		t.Errorf("edge: %+v", edge)
	}
}

func TestBuildWorkflowLineage_NotFound(t *testing.T) {
	_, err := cluster.BuildWorkflowLineage(
		context.Background(), "nope",
		&fakeWorkflowReader{err: errors.New("not found")},
		&fakeJobReader{jobs: map[string]*cpb.Job{}},
		&fakeModelReader{},
	)
	if !errors.Is(err, cluster.ErrWorkflowLineageNotFound) {
		t.Fatalf("want ErrWorkflowLineageNotFound, got %v", err)
	}
}

func TestBuildWorkflowLineage_NilModels_DegradesGracefully(t *testing.T) {
	wf := &cpb.Workflow{
		ID:   "wf-1",
		Jobs: []cpb.WorkflowJob{{Name: "train", JobID: "wf-1/train"}},
	}
	jobs := &fakeJobReader{jobs: map[string]*cpb.Job{
		"wf-1/train": {ID: "wf-1/train", Status: cpb.JobStatusCompleted},
	}}

	got, err := cluster.BuildWorkflowLineage(
		context.Background(), "wf-1",
		&fakeWorkflowReader{wf: wf}, jobs, nil, // ← models nil
	)
	if err != nil {
		t.Fatalf("nil models should still produce lineage: %v", err)
	}
	if len(got.Jobs[0].ModelsProduced) != 0 {
		t.Fatalf("nil models means empty produced list, got %+v", got.Jobs[0].ModelsProduced)
	}
}

func TestBuildWorkflowLineage_UnstartedJobs_PendingStatus(t *testing.T) {
	// Workflow submitted but Start() hasn't run — JobIDs empty.
	wf := &cpb.Workflow{
		ID:   "wf-1",
		Jobs: []cpb.WorkflowJob{{Name: "train"}, {Name: "serve", DependsOn: []string{"train"}}},
	}
	got, err := cluster.BuildWorkflowLineage(
		context.Background(), "wf-1",
		&fakeWorkflowReader{wf: wf},
		&fakeJobReader{jobs: map[string]*cpb.Job{}},
		&fakeModelReader{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, j := range got.Jobs {
		if j.Status != "pending" {
			t.Errorf("job %s: want PENDING, got %q", j.Name, j.Status)
		}
		if len(j.Outputs) != 0 || len(j.ModelsProduced) != 0 {
			t.Errorf("unstarted job should have no outputs/models: %+v", j)
		}
	}
}

func TestBuildWorkflowLineage_MalformedFromRef_Skipped(t *testing.T) {
	wf := &cpb.Workflow{
		ID: "wf-1",
		Jobs: []cpb.WorkflowJob{
			{Name: "a"},
			{
				Name: "b",
				Inputs: []cpb.ArtifactBinding{
					// Empty From should not produce an edge.
					{Name: "X", URI: "s3://b/x"},
				},
			},
		},
	}
	got, _ := cluster.BuildWorkflowLineage(
		context.Background(), "wf-1",
		&fakeWorkflowReader{wf: wf},
		&fakeJobReader{jobs: map[string]*cpb.Job{}},
		&fakeModelReader{},
	)
	if len(got.ArtifactEdges) != 0 {
		t.Errorf("empty From should not produce edge: %+v", got.ArtifactEdges)
	}
}

func TestBuildWorkflowLineage_PropagatesModelStoreError(t *testing.T) {
	wf := &cpb.Workflow{
		ID:   "wf-1",
		Jobs: []cpb.WorkflowJob{{Name: "train", JobID: "wf-1/train"}},
	}
	boom := errors.New("badger down")
	_, err := cluster.BuildWorkflowLineage(
		context.Background(), "wf-1",
		&fakeWorkflowReader{wf: wf},
		&fakeJobReader{jobs: map[string]*cpb.Job{
			"wf-1/train": {ID: "wf-1/train", Status: cpb.JobStatusCompleted},
		}},
		&fakeModelReader{err: boom},
	)
	if !errors.Is(err, boom) {
		t.Fatalf("want model store error propagated, got %v", err)
	}
}
