// internal/groups/badger.go
//
// BadgerDB-backed Store implementation. Shares the coordinator's
// main BadgerDB instance — group records are tiny and low-traffic
// compared to jobs, so a dedicated DB would be operational
// overhead for no isolation benefit (same call-out as the
// registry package makes).
//
// Key layout
// ──────────
//   groups/{name}                                 → JSON(Group)
//   groups/members/{principal_id}/{group_name}    → empty value
//
// The reverse index keys are populated/removed in lockstep with
// the Group record's Members slice inside a single Badger
// transaction. A half-applied write would leave GroupsFor out
// of sync with the forward record, so every mutation wraps both
// indices in one `db.Update`.

package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

// BadgerStore satisfies Store against a shared *badger.DB.
type BadgerStore struct {
	db *badger.DB
}

// NewBadgerStore returns a Store backed by db. No TTL — group
// records are admin-configured and meant to live until an
// explicit Delete.
func NewBadgerStore(db *badger.DB) *BadgerStore {
	return &BadgerStore{db: db}
}

// Key helpers. Kept package-private so tests exercising raw-key
// invariants can exercise the same layout without stringly
// coupling to literals.
const (
	keyPrefixGroup   = "groups/"
	keyPrefixMember  = "groups/members/"
	memberValue      = ""
)

func groupKey(name string) []byte {
	return []byte(keyPrefixGroup + name)
}
func memberKey(principalID, groupName string) []byte {
	// principalID contains a ':' (e.g. "user:alice"). That's
	// fine — Badger keys are byte strings, no special char.
	// But we want the reverse index key to be one
	// deterministic layout, so we escape the segment separator
	// with a known non-appearing byte. Using '\x1f' (Unit
	// Separator) which our validation forbids in both IDs and
	// group names.
	var b strings.Builder
	b.WriteString(keyPrefixMember)
	b.WriteString(principalID)
	b.WriteByte('\x1f')
	b.WriteString(groupName)
	return []byte(b.String())
}

func parseMemberKey(key []byte) (principalID, groupName string, ok bool) {
	if !strings.HasPrefix(string(key), keyPrefixMember) {
		return "", "", false
	}
	rest := string(key[len(keyPrefixMember):])
	sep := strings.IndexByte(rest, '\x1f')
	if sep < 0 {
		return "", "", false
	}
	return rest[:sep], rest[sep+1:], true
}

// ── Store methods ──────────────────────────────────────────

// Create persists a new Group. Fails closed: ErrGroupExists on
// a duplicate name, ErrInvalidName / ErrInvalidPrincipal on
// malformed input. Members are added in the same transaction so
// a partial Create does not leave the forward record populated
// with an empty reverse index.
func (s *BadgerStore) Create(_ context.Context, g Group) error {
	if err := ValidateName(g.Name); err != nil {
		return err
	}
	for _, m := range g.Members {
		if err := ValidatePrincipalID(m); err != nil {
			return fmt.Errorf("member %q: %w", m, err)
		}
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	g.UpdatedAt = g.CreatedAt
	return s.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(groupKey(g.Name)); err == nil {
			return ErrGroupExists
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("Create: probe: %w", err)
		}
		raw, err := json.Marshal(g)
		if err != nil {
			return fmt.Errorf("Create: marshal: %w", err)
		}
		if err := txn.Set(groupKey(g.Name), raw); err != nil {
			return fmt.Errorf("Create: set group: %w", err)
		}
		for _, m := range g.Members {
			if err := txn.Set(memberKey(m, g.Name), []byte(memberValue)); err != nil {
				return fmt.Errorf("Create: set member: %w", err)
			}
		}
		return nil
	})
}

// Get reads a Group by name.
func (s *BadgerStore) Get(_ context.Context, name string) (*Group, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	var out *Group
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(groupKey(name))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrGroupNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			var g Group
			if err := json.Unmarshal(v, &g); err != nil {
				return fmt.Errorf("Get: unmarshal: %w", err)
			}
			out = &g
			return nil
		})
	})
	return out, err
}

// List returns every group, newest first.
func (s *BadgerStore) List(_ context.Context) ([]Group, error) {
	var out []Group
	prefix := []byte(keyPrefixGroup)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			key := it.Item().Key()
			// Skip reverse-index entries (keyPrefixMember extends
			// keyPrefixGroup so a naive prefix iteration would
			// pick them up).
			if strings.HasPrefix(string(key), keyPrefixMember) {
				continue
			}
			var g Group
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &g)
			}); err != nil {
				return fmt.Errorf("List: unmarshal %q: %w", key, err)
			}
			out = append(out, g)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, err
}

// AddMember is idempotent — re-adding an existing member is a
// no-op. Updates both indices atomically.
func (s *BadgerStore) AddMember(_ context.Context, name, principalID string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := ValidatePrincipalID(principalID); err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(groupKey(name))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrGroupNotFound
		}
		if err != nil {
			return fmt.Errorf("AddMember: probe: %w", err)
		}
		var g Group
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &g)
		}); err != nil {
			return fmt.Errorf("AddMember: unmarshal: %w", err)
		}
		for _, existing := range g.Members {
			if existing == principalID {
				return nil // idempotent
			}
		}
		g.Members = append(g.Members, principalID)
		g.UpdatedAt = time.Now().UTC()
		raw, err := json.Marshal(g)
		if err != nil {
			return fmt.Errorf("AddMember: marshal: %w", err)
		}
		if err := txn.Set(groupKey(name), raw); err != nil {
			return fmt.Errorf("AddMember: set group: %w", err)
		}
		if err := txn.Set(memberKey(principalID, name), []byte(memberValue)); err != nil {
			return fmt.Errorf("AddMember: set reverse index: %w", err)
		}
		return nil
	})
}

// RemoveMember is idempotent.
func (s *BadgerStore) RemoveMember(_ context.Context, name, principalID string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := ValidatePrincipalID(principalID); err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(groupKey(name))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrGroupNotFound
		}
		if err != nil {
			return fmt.Errorf("RemoveMember: probe: %w", err)
		}
		var g Group
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &g)
		}); err != nil {
			return fmt.Errorf("RemoveMember: unmarshal: %w", err)
		}
		found := -1
		for i, existing := range g.Members {
			if existing == principalID {
				found = i
				break
			}
		}
		if found < 0 {
			return nil // idempotent
		}
		g.Members = append(g.Members[:found], g.Members[found+1:]...)
		g.UpdatedAt = time.Now().UTC()
		raw, err := json.Marshal(g)
		if err != nil {
			return fmt.Errorf("RemoveMember: marshal: %w", err)
		}
		if err := txn.Set(groupKey(name), raw); err != nil {
			return fmt.Errorf("RemoveMember: set group: %w", err)
		}
		if err := txn.Delete(memberKey(principalID, name)); err != nil {
			return fmt.Errorf("RemoveMember: delete reverse index: %w", err)
		}
		return nil
	})
}

// Delete removes the group and every reverse-index entry. Share
// cleanup (removing `group:{name}` grantees from resource share
// lists) is the HTTP handler's responsibility — see
// internal/api/handlers_groups.go.
func (s *BadgerStore) Delete(_ context.Context, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(groupKey(name))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrGroupNotFound
		}
		if err != nil {
			return fmt.Errorf("Delete: probe: %w", err)
		}
		var g Group
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &g)
		}); err != nil {
			return fmt.Errorf("Delete: unmarshal: %w", err)
		}
		for _, m := range g.Members {
			if err := txn.Delete(memberKey(m, name)); err != nil {
				return fmt.Errorf("Delete: drop reverse index %q: %w", m, err)
			}
		}
		return txn.Delete(groupKey(name))
	})
}

// GroupsFor returns every group the principal is a member of.
// O(n_groups_for_principal) via the reverse-index prefix scan.
// Unknown principal ID returns an empty slice + nil error.
func (s *BadgerStore) GroupsFor(_ context.Context, principalID string) ([]string, error) {
	if err := ValidatePrincipalID(principalID); err != nil {
		return nil, err
	}
	// Prefix is "groups/members/<principalID>\x1f".
	var b strings.Builder
	b.WriteString(keyPrefixMember)
	b.WriteString(principalID)
	b.WriteByte('\x1f')
	prefix := []byte(b.String())

	var out []string
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			_, g, ok := parseMemberKey(it.Item().KeyCopy(nil))
			if !ok {
				continue
			}
			out = append(out, g)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
