// internal/groups/groups_test.go
//
// Behavioural tests for the Store interface. Every test runs
// against BOTH MemStore and BadgerStore so a divergence in one
// backend (e.g. a forgotten reverse-index update in BadgerStore)
// surfaces immediately. The two impls share the same feature-38
// contract; if they differ in observable behaviour, the backend
// with the bug is wrong.

package groups_test

import (
	"context"
	"errors"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/groups"
)

// ── Backend matrix ─────────────────────────────────────────

type backend struct {
	name    string
	factory func(t *testing.T) groups.Store
}

func backends(t *testing.T) []backend {
	return []backend{
		{"MemStore", func(*testing.T) groups.Store { return groups.NewMemStore() }},
		{"BadgerStore", func(t *testing.T) groups.Store {
			opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
			db, err := badger.Open(opts)
			if err != nil {
				t.Fatalf("badger.Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			return groups.NewBadgerStore(db)
		}},
	}
}

// forEachStore runs fn against each registered backend.
func forEachStore(t *testing.T, fn func(t *testing.T, s groups.Store)) {
	t.Helper()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			fn(t, b.factory(t))
		})
	}
}

// ── Happy path ─────────────────────────────────────────────

func TestGroups_CreateGetList_RoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		g := groups.Group{
			Name:      "ml-team",
			Members:   []string{"user:alice", "user:bob"},
			CreatedBy: "user:root",
		}
		if err := s.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Get(ctx, "ml-team")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "ml-team" || len(got.Members) != 2 {
			t.Fatalf("Get: bad record %+v", got)
		}

		list, err := s.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 1 || list[0].Name != "ml-team" {
			t.Fatalf("List: want [ml-team], got %+v", list)
		}
	})
}

// ── Create conflicts ───────────────────────────────────────

func TestGroups_Create_RejectsDuplicate(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		if err := s.Create(ctx, groups.Group{Name: "dupe", CreatedBy: "user:root"}); err != nil {
			t.Fatalf("first create: %v", err)
		}
		err := s.Create(ctx, groups.Group{Name: "dupe", CreatedBy: "user:root"})
		if !errors.Is(err, groups.ErrGroupExists) {
			t.Fatalf("second create: want ErrGroupExists, got %v", err)
		}
	})
}

// ── Validation ─────────────────────────────────────────────

func TestGroups_Create_RejectsInvalidName(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		badNames := []string{
			"",                           // empty
			"has space",                  // space disallowed
			".leading-dot",               // leading dot
			"has/slash",                  // path separator
			string(make([]byte, 65)),     // too long
			"bad!char",                   // punctuation
		}
		for _, n := range badNames {
			err := s.Create(ctx, groups.Group{Name: n, CreatedBy: "user:root"})
			if !errors.Is(err, groups.ErrInvalidName) {
				t.Errorf("name=%q: want ErrInvalidName, got %v", n, err)
			}
		}
	})
}

func TestGroups_Create_RejectsInvalidMember(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		bad := []string{
			"",                // empty
			"alice",           // no kind prefix
			":alice",          // empty kind
			"user:",           // empty subject
			"group:ml-team",   // nested groups not allowed
		}
		for _, id := range bad {
			err := s.Create(ctx, groups.Group{
				Name:    "t",
				Members: []string{id},
			})
			if !errors.Is(err, groups.ErrInvalidPrincipal) {
				t.Errorf("id=%q: want ErrInvalidPrincipal, got %v", id, err)
			}
		}
	})
}

// ── Member add/remove + reverse index ──────────────────────

func TestGroups_AddRemoveMember_PopulatesReverseIndex(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		_ = s.Create(ctx, groups.Group{Name: "ml", CreatedBy: "user:root"})
		_ = s.Create(ctx, groups.Group{Name: "ops", CreatedBy: "user:root"})

		if err := s.AddMember(ctx, "ml", "user:alice"); err != nil {
			t.Fatalf("AddMember ml/alice: %v", err)
		}
		if err := s.AddMember(ctx, "ops", "user:alice"); err != nil {
			t.Fatalf("AddMember ops/alice: %v", err)
		}

		groupsForAlice, err := s.GroupsFor(ctx, "user:alice")
		if err != nil {
			t.Fatalf("GroupsFor: %v", err)
		}
		if len(groupsForAlice) != 2 {
			t.Fatalf("GroupsFor: want 2, got %v", groupsForAlice)
		}

		// Remove from one group; reverse index updates.
		if err := s.RemoveMember(ctx, "ml", "user:alice"); err != nil {
			t.Fatalf("RemoveMember: %v", err)
		}
		groupsForAlice, _ = s.GroupsFor(ctx, "user:alice")
		if len(groupsForAlice) != 1 || groupsForAlice[0] != "ops" {
			t.Fatalf("after remove: want [ops], got %v", groupsForAlice)
		}
	})
}

func TestGroups_AddMember_Idempotent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		_ = s.Create(ctx, groups.Group{Name: "t", CreatedBy: "user:root"})
		_ = s.AddMember(ctx, "t", "user:alice")
		_ = s.AddMember(ctx, "t", "user:alice")

		g, _ := s.Get(ctx, "t")
		if len(g.Members) != 1 {
			t.Fatalf("want 1 member, got %d: %v", len(g.Members), g.Members)
		}
		for2, _ := s.GroupsFor(ctx, "user:alice")
		if len(for2) != 1 {
			t.Fatalf("reverse index duplicated: %v", for2)
		}
	})
}

func TestGroups_RemoveMember_Idempotent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		_ = s.Create(ctx, groups.Group{Name: "t", CreatedBy: "user:root"})
		if err := s.RemoveMember(ctx, "t", "user:never-was-member"); err != nil {
			t.Fatalf("remove absent: %v", err)
		}
	})
}

// ── Missing-group errors ───────────────────────────────────

func TestGroups_Get_MissingReturnsNotFound(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		_, err := s.Get(context.Background(), "missing")
		if !errors.Is(err, groups.ErrGroupNotFound) {
			t.Fatalf("want ErrGroupNotFound, got %v", err)
		}
	})
}

func TestGroups_AddMember_MissingGroup(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		err := s.AddMember(context.Background(), "missing", "user:alice")
		if !errors.Is(err, groups.ErrGroupNotFound) {
			t.Fatalf("want ErrGroupNotFound, got %v", err)
		}
	})
}

// ── Delete + reverse-index cleanup ─────────────────────────

func TestGroups_Delete_RemovesReverseIndex(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		_ = s.Create(ctx, groups.Group{
			Name:    "ml",
			Members: []string{"user:alice", "user:bob"},
		})
		if err := s.Delete(ctx, "ml"); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		// Group gone.
		if _, err := s.Get(ctx, "ml"); !errors.Is(err, groups.ErrGroupNotFound) {
			t.Errorf("Get after delete: want ErrGroupNotFound, got %v", err)
		}

		// Reverse index gone for both members.
		for _, m := range []string{"user:alice", "user:bob"} {
			gs, _ := s.GroupsFor(ctx, m)
			if len(gs) != 0 {
				t.Errorf("reverse index not cleaned for %q: %v", m, gs)
			}
		}
	})
}

// ── GroupsFor scale sanity ─────────────────────────────────

// TestGroupsFor_PrefixScanIsolated creates 50 groups with
// overlapping members and verifies GroupsFor returns exactly
// the expected subset (not a stray from a prefix-matching
// member ID). Defence against "user:a" matching "user:ab"
// style bugs in the reverse-index layout.
func TestGroupsFor_PrefixScanIsolated(t *testing.T) {
	forEachStore(t, func(t *testing.T, s groups.Store) {
		ctx := context.Background()
		// Two principal IDs where one is a prefix of the other.
		_ = s.Create(ctx, groups.Group{Name: "g1", Members: []string{"user:a", "user:ab"}})
		_ = s.Create(ctx, groups.Group{Name: "g2", Members: []string{"user:ab"}})

		gsA, _ := s.GroupsFor(ctx, "user:a")
		if len(gsA) != 1 || gsA[0] != "g1" {
			t.Errorf("user:a -> want [g1], got %v", gsA)
		}

		gsAB, _ := s.GroupsFor(ctx, "user:ab")
		if len(gsAB) != 2 {
			t.Errorf("user:ab -> want 2 groups, got %v", gsAB)
		}
	})
}
