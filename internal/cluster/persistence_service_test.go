// internal/cluster/persistence_service_test.go
//
// Feature-17 Job.Service round-trip coverage through BadgerDB JSON.
// Sibling file to persistence_labels_test.go; same shape of
// regression the labels + TotalGpus tests guard against: a JSON-tag
// typo or a struct-field rename on ServiceSpec would silently break
// persistence across coordinator restarts, and every pending service
// job would recover as a batch job (no prober, no readiness event)
// after a crash.

package cluster_test

import (
	"context"
	"testing"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// TestBadgerPersister_JobService_Roundtrip pins the Service field's
// JSON persistence. The pre-existing TestBadgerPersister_SaveJob_LoadAllJobs_Roundtrip
// only inspects ID + Command; ServiceSpec was added for feature 17
// but no test verifies it survives Save→LoadAll. A regression
// breaking any of the three fields' JSON tags (port / health_path /
// health_initial_ms) would silently lose state on coordinator
// restart while every pending service job recovered as a batch job.
func TestBadgerPersister_JobService_Roundtrip(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	j := &cpb.Job{
		ID:      "inf-job",
		Command: "python",
		Service: &cpb.ServiceSpec{
			Port:            8080,
			HealthPath:      "/healthz",
			HealthInitialMS: 500,
		},
	}
	if err := p.SaveJob(ctx, j); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	got := jobs[0].Service
	if got == nil {
		t.Fatal("Service field dropped on roundtrip")
	}
	if got.Port != 8080 || got.HealthPath != "/healthz" || got.HealthInitialMS != 500 {
		t.Fatalf("Service roundtrip mismatch: %+v", got)
	}
}

// TestBadgerPersister_JobService_ForwardCompatFromPreServiceJSON
// covers the migration case: a Job row persisted by a coordinator
// version without the Service field (pre-feature-17, or any batch
// job today) must deserialize cleanly with Service=nil. The
// `json:"service,omitempty"` tag makes a zero-value Service
// indistinguishable from a pre-field row in BadgerDB; this test
// pins the invariant so a future refactor (e.g. dropping omitempty
// or switching the field type) has to keep the migration path
// working.
func TestBadgerPersister_JobService_ForwardCompatFromPreServiceJSON(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	// A pre-feature-17 batch job: no Service field set. omitempty
	// means the serialised JSON omits the "service" key, matching
	// what an older persister would have written.
	if err := p.SaveJob(ctx, &cpb.Job{ID: "legacy-batch", Command: "ls"}); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if jobs[0].Service != nil {
		t.Fatalf("missing-field should load as nil Service, got %+v", jobs[0].Service)
	}
	if jobs[0].ID != "legacy-batch" {
		t.Fatalf("wrong job: %+v", jobs[0])
	}
}
