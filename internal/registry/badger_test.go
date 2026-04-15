package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

func newTestStore(t *testing.T) *BadgerStore {
	t.Helper()
	opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewBadgerStore(db)
}

// ── Dataset roundtrip ──────────────────────────────────────────────────

func TestBadgerStore_Dataset_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	d := &Dataset{
		Name:    "iris",
		Version: "v1.0.0",
		URI:     "s3://helion/datasets/iris/v1.0.0.parquet",
		SizeBytes: 1024,
		SHA256:    "deadbeef",
		Tags:      map[string]string{"team": "ml"},
		CreatedAt: time.Now().UTC(),
		CreatedBy: "alice",
	}
	if err := s.RegisterDataset(ctx, d); err != nil {
		t.Fatalf("RegisterDataset: %v", err)
	}
	got, err := s.GetDataset("iris", "v1.0.0")
	if err != nil {
		t.Fatalf("GetDataset: %v", err)
	}
	if got.URI != d.URI || got.CreatedBy != d.CreatedBy || got.Tags["team"] != "ml" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestBadgerStore_Dataset_DuplicateVersion_Rejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	d := &Dataset{Name: "iris", Version: "v1", URI: "s3://b/k", CreatedAt: time.Now()}
	_ = s.RegisterDataset(ctx, d)
	if err := s.RegisterDataset(ctx, d); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestBadgerStore_Dataset_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetDataset("nope", "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	if err := s.DeleteDataset(context.Background(), "nope", "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestBadgerStore_Dataset_ListNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now()
	for i := 0; i < 3; i++ {
		d := &Dataset{
			Name:      "iris",
			Version:   "v" + string(rune('a'+i)),
			URI:       "s3://b/k",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		_ = s.RegisterDataset(ctx, d)
	}
	list, total, err := s.ListDatasets(ctx, 1, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Fatalf("counts: total=%d len=%d", total, len(list))
	}
	// Newest first → "vc", "vb", "va".
	if list[0].Version != "vc" || list[2].Version != "va" {
		t.Fatalf("not sorted newest-first: %+v", list)
	}
}

func TestBadgerStore_Dataset_Pagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now()
	for i := 0; i < 5; i++ {
		_ = s.RegisterDataset(ctx, &Dataset{
			Name: "d", Version: "v" + string(rune('a'+i)),
			URI: "s3://b/k", CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	p1, total, _ := s.ListDatasets(ctx, 1, 2)
	p2, _, _ := s.ListDatasets(ctx, 2, 2)
	p3, _, _ := s.ListDatasets(ctx, 3, 2)
	if total != 5 || len(p1) != 2 || len(p2) != 2 || len(p3) != 1 {
		t.Fatalf("paginate sizes: %d %d %d (total=%d)", len(p1), len(p2), len(p3), total)
	}
	// Page past end returns empty, not error.
	p4, _, err := s.ListDatasets(ctx, 99, 2)
	if err != nil || len(p4) != 0 {
		t.Fatalf("past-end page: %+v %v", p4, err)
	}
}

func TestBadgerStore_Dataset_DeleteIsSpecific(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.RegisterDataset(ctx, &Dataset{Name: "d", Version: "v1", URI: "s3://b/k", CreatedAt: time.Now()})
	_ = s.RegisterDataset(ctx, &Dataset{Name: "d", Version: "v2", URI: "s3://b/k", CreatedAt: time.Now()})
	if err := s.DeleteDataset(ctx, "d", "v1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetDataset("d", "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("v1 should be gone")
	}
	if _, err := s.GetDataset("d", "v2"); err != nil {
		t.Fatalf("v2 should still exist: %v", err)
	}
}

// ── Model roundtrip + latest ────────────────────────────────────────────

func TestBadgerStore_Model_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := &Model{
		Name:    "resnet",
		Version: "v0.1",
		URI:     "s3://b/jobs/train/out/model.pt",
		Framework:     "pytorch",
		SourceJobID:   "train-1",
		SourceDataset: DatasetRef{Name: "imagenet", Version: "v2"},
		Metrics:       map[string]float64{"top1": 0.76},
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "alice",
	}
	if err := s.RegisterModel(ctx, m); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	got, err := s.GetModel("resnet", "v0.1")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if got.SourceDataset.Name != "imagenet" || got.Metrics["top1"] != 0.76 {
		t.Fatalf("lineage/metrics lost: %+v", got)
	}
}

func TestBadgerStore_Model_Latest_ReturnsMostRecentByCreatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Insert v0.2 first with an older timestamp; v0.1 with a newer.
	// Latest must pick v0.1 (chronological, not semantic).
	_ = s.RegisterModel(ctx, &Model{Name: "m", Version: "v0.2", URI: "s3://b/k",
		CreatedAt: time.Now().Add(-1 * time.Hour)})
	_ = s.RegisterModel(ctx, &Model{Name: "m", Version: "v0.1", URI: "s3://b/k",
		CreatedAt: time.Now()})
	latest, err := s.LatestModel("m")
	if err != nil {
		t.Fatalf("LatestModel: %v", err)
	}
	if latest.Version != "v0.1" {
		t.Fatalf("latest: got %q, want v0.1", latest.Version)
	}
}

func TestBadgerStore_Model_Latest_MissingReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.LatestModel("never-registered"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ── ListBySourceJob (feature 18 lineage) ──────────────────────────────

func TestBadgerStore_Model_ListBySourceJob(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.RegisterModel(ctx, &Model{
		Name: "a", Version: "v1", URI: "s3://b/a", SourceJobID: "wf-1/train",
		CreatedAt: time.Now(),
	})
	_ = s.RegisterModel(ctx, &Model{
		Name: "a", Version: "v2", URI: "s3://b/a2", SourceJobID: "wf-1/train",
		CreatedAt: time.Now(),
	})
	_ = s.RegisterModel(ctx, &Model{
		Name: "b", Version: "v1", URI: "s3://b/b", SourceJobID: "other-job",
		CreatedAt: time.Now(),
	})
	_ = s.RegisterModel(ctx, &Model{
		Name: "c", Version: "v1", URI: "s3://b/c", // no source
		CreatedAt: time.Now(),
	})

	got, err := s.ListBySourceJob(ctx, "wf-1/train")
	if err != nil {
		t.Fatalf("ListBySourceJob: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 models for wf-1/train, got %d", len(got))
	}

	// Different job → none.
	if got, _ := s.ListBySourceJob(ctx, "never-happened"); len(got) != 0 {
		t.Fatalf("unknown source job should return empty, got %d", len(got))
	}

	// Empty source_job_id → nil, no error. Keeps callers from
	// accidentally returning "all models with no source" which would
	// be a footgun during lineage joins.
	if got, err := s.ListBySourceJob(ctx, ""); err != nil || got != nil {
		t.Fatalf("empty source returned (%+v, %v); want (nil, nil)", got, err)
	}
}

// ── Cross-type isolation ────────────────────────────────────────────────

// Datasets and models share a BadgerDB — verify the prefix scheme
// prevents a dataset named "x" from colliding with a model named "x".
func TestBadgerStore_CrossTypeIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.RegisterDataset(ctx, &Dataset{Name: "shared", Version: "v1", URI: "s3://b/d", CreatedAt: time.Now()})
	_ = s.RegisterModel(ctx, &Model{Name: "shared", Version: "v1", URI: "s3://b/m", CreatedAt: time.Now()})

	d, err := s.GetDataset("shared", "v1")
	if err != nil || d.URI != "s3://b/d" {
		t.Fatalf("dataset: %+v %v", d, err)
	}
	m, err := s.GetModel("shared", "v1")
	if err != nil || m.URI != "s3://b/m" {
		t.Fatalf("model: %+v %v", m, err)
	}
}
