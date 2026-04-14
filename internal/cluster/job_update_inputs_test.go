package cluster_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestUpdateResolvedInputs_PersistsRewrittenURI(t *testing.T) {
	p := cluster.NewMemJobPersister()
	s := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	original := &cpb.Job{
		ID: "job-ur",
		Inputs: []cpb.ArtifactBinding{
			{Name: "DATA", From: "upstream.OUT", LocalPath: "in/d"},
		},
	}
	if err := s.Submit(ctx, original); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Simulate the resolver: same entries, URI filled in, From kept
	// so the persisted Job shows both sides of the lineage.
	rewritten := []cpb.ArtifactBinding{
		{Name: "DATA", From: "upstream.OUT", URI: "s3://b/jobs/wf/upstream/out/o", LocalPath: "in/d"},
	}
	if err := s.UpdateResolvedInputs(ctx, "job-ur", rewritten); err != nil {
		t.Fatalf("UpdateResolvedInputs: %v", err)
	}

	got, err := s.Get("job-ur")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Inputs) != 1 {
		t.Fatalf("inputs: %+v", got.Inputs)
	}
	if got.Inputs[0].URI != "s3://b/jobs/wf/upstream/out/o" {
		t.Fatalf("URI not persisted: %q", got.Inputs[0].URI)
	}
	if got.Inputs[0].From != "upstream.OUT" {
		t.Fatalf("From lost on update: %q", got.Inputs[0].From)
	}
}

func TestUpdateResolvedInputs_JobNotFound(t *testing.T) {
	s := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	err := s.UpdateResolvedInputs(context.Background(), "does-not-exist", nil)
	if !errors.Is(err, cluster.ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound, got %v", err)
	}
}

func TestUpdateResolvedInputs_DefensiveCopy(t *testing.T) {
	s := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ctx := context.Background()
	_ = s.Submit(ctx, &cpb.Job{
		ID: "job-copy",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", From: "a.OUT", LocalPath: "in/x"},
		},
	})

	inputs := []cpb.ArtifactBinding{
		{Name: "X", URI: "s3://b/x", From: "a.OUT", LocalPath: "in/x"},
	}
	if err := s.UpdateResolvedInputs(ctx, "job-copy", inputs); err != nil {
		t.Fatalf("UpdateResolvedInputs: %v", err)
	}
	// Caller mutation after the call must not reach the persisted Job.
	inputs[0].URI = "s3://attacker/override"

	got, _ := s.Get("job-copy")
	if got.Inputs[0].URI != "s3://b/x" {
		t.Fatalf("defensive copy violated: %q", got.Inputs[0].URI)
	}
}

// failingPersister wraps MemJobPersister and fails the next SaveJob so
// we can exercise the rollback branch without touching BadgerDB.
type failingPersister struct {
	inner *cluster.MemJobPersister
	fail  bool
}

func (f *failingPersister) SaveJob(ctx context.Context, j *cpb.Job) error {
	if f.fail {
		return errors.New("disk full")
	}
	return f.inner.SaveJob(ctx, j)
}
func (f *failingPersister) LoadAllJobs(ctx context.Context) ([]*cpb.Job, error) {
	return f.inner.LoadAllJobs(ctx)
}
func (f *failingPersister) AppendAudit(ctx context.Context, eventType, actor, target, detail string) error {
	return f.inner.AppendAudit(ctx, eventType, actor, target, detail)
}

func TestUpdateResolvedInputs_PersistFailure_RollsBack(t *testing.T) {
	fp := &failingPersister{inner: cluster.NewMemJobPersister()}
	s := cluster.NewJobStore(fp, nil)
	ctx := context.Background()

	orig := []cpb.ArtifactBinding{
		{Name: "DATA", From: "a.OUT", LocalPath: "in/d"},
	}
	_ = s.Submit(ctx, &cpb.Job{ID: "job-rb", Inputs: orig})

	// Flip the persister into failure mode only for the update call.
	fp.fail = true
	err := s.UpdateResolvedInputs(ctx, "job-rb", []cpb.ArtifactBinding{
		{Name: "DATA", From: "a.OUT", URI: "s3://b/x", LocalPath: "in/d"},
	})
	if err == nil {
		t.Fatal("expected persist failure to propagate")
	}

	// In-memory state must have rolled back: URI still empty.
	got, _ := s.Get("job-rb")
	if got.Inputs[0].URI != "" {
		t.Fatalf("rollback violated: URI=%q", got.Inputs[0].URI)
	}
	if got.Inputs[0].From != "a.OUT" {
		t.Fatalf("From corrupted on rollback: %q", got.Inputs[0].From)
	}
}
