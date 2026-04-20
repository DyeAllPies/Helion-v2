// internal/registry/badger_extras_test.go
//
// Coverage-focused tests for the lesser-exercised BadgerStore
// surface: Count*, ListModels pagination, DeleteModel, and the
// feature-38 Update*Shares paths. The base round-trip is covered
// in badger_test.go.

package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
)

// ── Count* ───────────────────────────────────────────────────

func TestBadgerStore_CountDatasets_AndModels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Empty store → 0.
	if n, err := s.CountDatasets(ctx); err != nil || n != 0 {
		t.Errorf("empty CountDatasets: n=%d err=%v, want (0, nil)", n, err)
	}
	if n, err := s.CountModels(ctx); err != nil || n != 0 {
		t.Errorf("empty CountModels: n=%d err=%v, want (0, nil)", n, err)
	}

	// Seed three datasets + two models.
	for i, ver := range []string{"v1", "v2", "v3"} {
		_ = s.RegisterDataset(ctx, &Dataset{
			Name: "ds", Version: ver, URI: "s3://d",
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}
	for i, ver := range []string{"v1", "v2"} {
		_ = s.RegisterModel(ctx, &Model{
			Name: "m", Version: ver, URI: "s3://m",
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}

	if n, err := s.CountDatasets(ctx); err != nil || n != 3 {
		t.Errorf("CountDatasets: n=%d err=%v, want (3, nil)", n, err)
	}
	if n, err := s.CountModels(ctx); err != nil || n != 2 {
		t.Errorf("CountModels: n=%d err=%v, want (2, nil)", n, err)
	}
}

// ── ListModels pagination ────────────────────────────────────

func TestBadgerStore_ListModels_PaginatesNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seed 5 versions with ascending CreatedAt.
	base := time.Now().UTC()
	for i, ver := range []string{"v1", "v2", "v3", "v4", "v5"} {
		_ = s.RegisterModel(ctx, &Model{
			Name: "m", Version: ver, URI: "s3://m",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}

	// Page 1, size 2 → newest two (v5, v4).
	got, total, err := s.ListModels(ctx, 1, 2)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if total != 5 {
		t.Errorf("total: got %d, want 5", total)
	}
	if len(got) != 2 {
		t.Fatalf("page len: got %d, want 2", len(got))
	}
	if got[0].Version != "v5" || got[1].Version != "v4" {
		t.Errorf("ordering: got [%s,%s], want [v5,v4]", got[0].Version, got[1].Version)
	}

	// Page 3, size 2 → only v1 remains.
	got, _, err = s.ListModels(ctx, 3, 2)
	if err != nil {
		t.Fatalf("ListModels page 3: %v", err)
	}
	if len(got) != 1 || got[0].Version != "v1" {
		t.Errorf("last page: got %+v, want [v1]", got)
	}

	// Page past end → empty slice (not error).
	got, _, err = s.ListModels(ctx, 99, 10)
	if err != nil {
		t.Fatalf("past-end: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("past-end page: got len %d, want 0", len(got))
	}
}

// ── DeleteModel ──────────────────────────────────────────────

func TestBadgerStore_DeleteModel_RemovesEntry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.RegisterModel(ctx, &Model{Name: "m", Version: "v1", URI: "s3://m"})

	if err := s.DeleteModel(ctx, "m", "v1"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	// Second delete must produce ErrNotFound — delete is NOT
	// idempotent in registry (different from webauthn's store).
	err := s.DeleteModel(ctx, "m", "v1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
}

func TestBadgerStore_DeleteModel_Missing_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteModel(context.Background(), "nope", "v1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("delete-missing: want ErrNotFound, got %v", err)
	}
}

// ── UpdateDatasetShares ──────────────────────────────────────

func TestBadgerStore_UpdateDatasetShares_PersistsAndOverwrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_ = s.RegisterDataset(ctx, &Dataset{Name: "ds", Version: "v1", URI: "s3://d"})

	shares := []authz.Share{
		{Grantee: "user:bob", Actions: []authz.Action{authz.ActionRead}},
	}
	if err := s.UpdateDatasetShares(ctx, "ds", "v1", shares); err != nil {
		t.Fatalf("UpdateDatasetShares: %v", err)
	}

	got, err := s.GetDataset("ds", "v1")
	if err != nil {
		t.Fatalf("GetDataset: %v", err)
	}
	if len(got.Shares) != 1 || got.Shares[0].Grantee != "user:bob" {
		t.Errorf("shares not persisted: %+v", got.Shares)
	}

	// Replace with an empty set (revoke-all) — must overwrite.
	if err := s.UpdateDatasetShares(ctx, "ds", "v1", nil); err != nil {
		t.Fatalf("UpdateDatasetShares revoke-all: %v", err)
	}
	got, _ = s.GetDataset("ds", "v1")
	if len(got.Shares) != 0 {
		t.Errorf("shares not cleared: %+v", got.Shares)
	}
}

func TestBadgerStore_UpdateDatasetShares_Missing_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateDatasetShares(context.Background(), "nope", "v1", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("update-missing dataset: want ErrNotFound, got %v", err)
	}
}

// ── UpdateModelShares ────────────────────────────────────────

func TestBadgerStore_UpdateModelShares_PersistsAndOverwrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_ = s.RegisterModel(ctx, &Model{Name: "m", Version: "v1", URI: "s3://m"})

	shares := []authz.Share{
		{Grantee: "group:ml-team", Actions: []authz.Action{authz.ActionRead, authz.ActionWrite}},
	}
	if err := s.UpdateModelShares(ctx, "m", "v1", shares); err != nil {
		t.Fatalf("UpdateModelShares: %v", err)
	}
	got, err := s.GetModel("m", "v1")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if len(got.Shares) != 1 || got.Shares[0].Grantee != "group:ml-team" {
		t.Errorf("shares not persisted: %+v", got.Shares)
	}
}

func TestBadgerStore_UpdateModelShares_Missing_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateModelShares(context.Background(), "nope", "v1", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("update-missing model: want ErrNotFound, got %v", err)
	}
}
