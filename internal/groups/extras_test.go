// internal/groups/extras_test.go
//
// Coverage for the BadgerStore error branches + idempotent
// paths that the main groups_test.go doesn't hit: Create
// with pre-populated members, AddMember-when-already-present,
// RemoveMember-when-absent, validation errors surfacing
// through each method.

package groups_test

import (
	"context"
	"errors"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/groups"
)

func newBadgerStore(t *testing.T) *groups.BadgerStore {
	t.Helper()
	opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return groups.NewBadgerStore(db)
}

// ── Create with initial members ─────────────────────────────

func TestBadgerStore_Create_WithMembers_AllAdded(t *testing.T) {
	s := newBadgerStore(t)
	ctx := context.Background()
	g := groups.Group{
		Name:      "ml-team",
		Members:   []string{"user:alice", "user:bob"},
		CreatedBy: "user:root",
	}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create with members: %v", err)
	}
	got, err := s.Get(ctx, "ml-team")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Members) != 2 {
		t.Errorf("members: got %d, want 2", len(got.Members))
	}
}

func TestBadgerStore_Create_InvalidMember_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	g := groups.Group{
		Name:    "g1",
		Members: []string{"bogus-no-prefix"},
	}
	err := s.Create(context.Background(), g)
	if err == nil {
		t.Error("invalid member: want error, got nil")
	}
}

func TestBadgerStore_Create_InvalidName_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	err := s.Create(context.Background(), groups.Group{Name: ""})
	if err == nil {
		t.Error("invalid name: want error, got nil")
	}
}

// ── Get validation errors ───────────────────────────────────

func TestBadgerStore_Get_InvalidName_ValidationError(t *testing.T) {
	s := newBadgerStore(t)
	// Invalid name (contains reserved character) surfaces
	// ValidateName's error before the Badger lookup.
	_, err := s.Get(context.Background(), string([]byte{0x1f}))
	if err == nil {
		t.Error("invalid name: want error")
	}
}

// ── AddMember idempotency + validation ─────────────────────

func TestBadgerStore_AddMember_AlreadyPresent_Idempotent(t *testing.T) {
	s := newBadgerStore(t)
	ctx := context.Background()
	_ = s.Create(ctx, groups.Group{Name: "g1"})
	if err := s.AddMember(ctx, "g1", "user:alice"); err != nil {
		t.Fatalf("first AddMember: %v", err)
	}
	// Second add of same member → no-op, no error.
	if err := s.AddMember(ctx, "g1", "user:alice"); err != nil {
		t.Fatalf("idempotent AddMember: %v", err)
	}
	got, _ := s.Get(ctx, "g1")
	if len(got.Members) != 1 {
		t.Errorf("members: got %d, want 1", len(got.Members))
	}
}

func TestBadgerStore_AddMember_InvalidPrincipal_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	_ = s.Create(context.Background(), groups.Group{Name: "g1"})
	err := s.AddMember(context.Background(), "g1", "bogus")
	if err == nil {
		t.Error("invalid principal: want error")
	}
}

func TestBadgerStore_AddMember_InvalidGroupName_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	err := s.AddMember(context.Background(), "", "user:alice")
	if err == nil {
		t.Error("invalid group name: want error")
	}
}

// ── RemoveMember idempotency + validation ──────────────────

func TestBadgerStore_RemoveMember_NotAMember_Idempotent(t *testing.T) {
	s := newBadgerStore(t)
	ctx := context.Background()
	_ = s.Create(ctx, groups.Group{Name: "g1"})
	// Remove a non-member — must succeed silently.
	if err := s.RemoveMember(ctx, "g1", "user:not-there"); err != nil {
		t.Errorf("remove non-member: %v", err)
	}
}

func TestBadgerStore_RemoveMember_InvalidGroupName_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	err := s.RemoveMember(context.Background(), "", "user:alice")
	if err == nil {
		t.Error("invalid group name: want error")
	}
}

func TestBadgerStore_RemoveMember_InvalidPrincipal_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	_ = s.Create(context.Background(), groups.Group{Name: "g1"})
	err := s.RemoveMember(context.Background(), "g1", "bogus")
	if err == nil {
		t.Error("invalid principal: want error")
	}
}

// ── Delete validation errors ───────────────────────────────

func TestBadgerStore_Delete_InvalidName_Rejected(t *testing.T) {
	s := newBadgerStore(t)
	err := s.Delete(context.Background(), "")
	if err == nil {
		t.Error("invalid name: want error")
	}
}

func TestBadgerStore_Delete_MissingGroup_ErrGroupNotFound(t *testing.T) {
	s := newBadgerStore(t)
	err := s.Delete(context.Background(), "not-there")
	if !errors.Is(err, groups.ErrGroupNotFound) {
		t.Errorf("missing: want ErrGroupNotFound, got %v", err)
	}
}
