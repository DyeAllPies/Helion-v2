// internal/groups/memstore.go
//
// In-memory Store implementation. Used by unit tests that want
// group membership semantics without spinning up BadgerDB and
// by the dev-mode server when no persistence is configured. The
// behaviour matches BadgerStore's contract 1:1 (validation,
// idempotency, ordering, reverse index) — if they diverge, the
// test matrix in groups_test.go catches it because both impls
// run the same table.

package groups

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store. Safe for concurrent use via
// an internal mutex.
type MemStore struct {
	mu       sync.RWMutex
	groups   map[string]Group   // name -> Group
	reverse  map[string]map[string]struct{} // principalID -> set of group names
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		groups:  make(map[string]Group),
		reverse: make(map[string]map[string]struct{}),
	}
}

func (s *MemStore) Create(_ context.Context, g Group) error {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.groups[g.Name]; exists {
		return ErrGroupExists
	}
	// Copy the Members slice so a caller mutating theirs later
	// doesn't silently change our state.
	g.Members = append([]string(nil), g.Members...)
	s.groups[g.Name] = g
	for _, m := range g.Members {
		s.addReverseLocked(m, g.Name)
	}
	return nil
}

func (s *MemStore) Get(_ context.Context, name string) (*Group, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[name]
	if !ok {
		return nil, ErrGroupNotFound
	}
	// Defensive copy on read too.
	g.Members = append([]string(nil), g.Members...)
	return &g, nil
}

func (s *MemStore) List(_ context.Context) ([]Group, error) {
	s.mu.RLock()
	out := make([]Group, 0, len(s.groups))
	for _, g := range s.groups {
		g.Members = append([]string(nil), g.Members...)
		out = append(out, g)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemStore) AddMember(_ context.Context, name, principalID string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := ValidatePrincipalID(principalID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[name]
	if !ok {
		return ErrGroupNotFound
	}
	for _, m := range g.Members {
		if m == principalID {
			return nil
		}
	}
	g.Members = append(g.Members, principalID)
	g.UpdatedAt = time.Now().UTC()
	s.groups[name] = g
	s.addReverseLocked(principalID, name)
	return nil
}

func (s *MemStore) RemoveMember(_ context.Context, name, principalID string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := ValidatePrincipalID(principalID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[name]
	if !ok {
		return ErrGroupNotFound
	}
	found := -1
	for i, m := range g.Members {
		if m == principalID {
			found = i
			break
		}
	}
	if found < 0 {
		return nil
	}
	g.Members = append(g.Members[:found], g.Members[found+1:]...)
	g.UpdatedAt = time.Now().UTC()
	s.groups[name] = g
	s.removeReverseLocked(principalID, name)
	return nil
}

func (s *MemStore) Delete(_ context.Context, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[name]
	if !ok {
		return ErrGroupNotFound
	}
	for _, m := range g.Members {
		s.removeReverseLocked(m, name)
	}
	delete(s.groups, name)
	return nil
}

func (s *MemStore) GroupsFor(_ context.Context, principalID string) ([]string, error) {
	if err := ValidatePrincipalID(principalID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.reverse[principalID]
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out, nil
}

// ── internal helpers ───────────────────────────────────────

func (s *MemStore) addReverseLocked(principalID, groupName string) {
	set := s.reverse[principalID]
	if set == nil {
		set = make(map[string]struct{})
		s.reverse[principalID] = set
	}
	set[groupName] = struct{}{}
}

func (s *MemStore) removeReverseLocked(principalID, groupName string) {
	set, ok := s.reverse[principalID]
	if !ok {
		return
	}
	delete(set, groupName)
	if len(set) == 0 {
		delete(s.reverse, principalID)
	}
}
