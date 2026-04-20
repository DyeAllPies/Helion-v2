// internal/webauthn/memstore.go
//
// In-memory CredentialStore. Tests use it without touching
// Badger; dev-mode coordinator binaries that didn't wire
// persistence fall through to it so registration still works
// (at the cost of losing credentials on restart).

package webauthn

import (
	"bytes"
	"context"
	"fmt"
	"sync"
)

// MemStore is a map-backed CredentialStore protected by an
// RWMutex. Safe for concurrent use.
type MemStore struct {
	mu   sync.RWMutex
	data map[string]*CredentialRecord // key: hex(credential_id)
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{data: map[string]*CredentialRecord{}}
}

func (s *MemStore) key(id []byte) string { return EncodeCredentialID(id) }

// Create inserts a new record. Returns an error on
// duplicate credential ID so accidental double-registration
// is visible.
func (s *MemStore) Create(_ context.Context, rec *CredentialRecord) error {
	if rec == nil {
		return fmt.Errorf("MemStore.Create: nil record")
	}
	if len(rec.Credential.ID) == 0 {
		return fmt.Errorf("MemStore.Create: empty credential ID")
	}
	k := s.key(rec.Credential.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[k]; exists {
		return fmt.Errorf("MemStore.Create: credential %s already exists", k)
	}
	// Defensive copy so the caller cannot mutate the stored
	// record by reference.
	copyRec := *rec
	s.data[k] = &copyRec
	return nil
}

// Get returns a defensive copy of the stored record.
func (s *MemStore) Get(_ context.Context, credentialID []byte) (*CredentialRecord, error) {
	k := s.key(credentialID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.data[k]
	if !ok {
		return nil, ErrCredentialNotFound
	}
	copyRec := *rec
	return &copyRec, nil
}

// ListByOperator returns every credential for the given
// user handle, newest-first.
func (s *MemStore) ListByOperator(_ context.Context, userHandle []byte) ([]*CredentialRecord, error) {
	s.mu.RLock()
	out := make([]*CredentialRecord, 0)
	for _, rec := range s.data {
		if bytes.Equal(rec.UserHandle, userHandle) {
			copyRec := *rec
			out = append(out, &copyRec)
		}
	}
	s.mu.RUnlock()
	SortByRegistered(out)
	return out, nil
}

// List returns every stored credential, newest-first.
func (s *MemStore) List(_ context.Context) ([]*CredentialRecord, error) {
	s.mu.RLock()
	out := make([]*CredentialRecord, 0, len(s.data))
	for _, rec := range s.data {
		copyRec := *rec
		out = append(out, &copyRec)
	}
	s.mu.RUnlock()
	SortByRegistered(out)
	return out, nil
}

// Delete is idempotent.
func (s *MemStore) Delete(_ context.Context, credentialID []byte) error {
	k := s.key(credentialID)
	s.mu.Lock()
	delete(s.data, k)
	s.mu.Unlock()
	return nil
}

// UpdateSignCount writes the new counter after a successful
// assertion. verifyNotReplay centralises the
// "strictly-increasing OR stuck-at-zero" rule.
func (s *MemStore) UpdateSignCount(_ context.Context, credentialID []byte, signCount uint32) error {
	k := s.key(credentialID)
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[k]
	if !ok {
		return ErrCredentialNotFound
	}
	if err := verifyNotReplay(rec.Credential.Authenticator.SignCount, signCount); err != nil {
		return err
	}
	rec.Credential.Authenticator.SignCount = signCount
	return nil
}
