// internal/auth/testhelpers_test.go
//
// Shared mock stores and the package-level ctx used across auth test files.

package auth_test

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ctx = context.Background()

// ── mockTokenStore ────────────────────────────────────────────────────────────

type mockTokenStore struct {
	mu   sync.Mutex
	data map[string][]byte
	err  error // if set, all operations return this error
}

func newMockStore() *mockTokenStore {
	return &mockTokenStore{data: make(map[string][]byte)}
}

func (s *mockTokenStore) Get(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("key not found")
	}
	return append([]byte{}, v...), nil
}

func (s *mockTokenStore) Put(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte{}, value...)
	return nil
}

func (s *mockTokenStore) Delete(_ context.Context, key string) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// ── mockPersistenceStore (for StoreAdapter tests) ─────────────────────────────

type mockPersistenceStore struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration
}

func newPersistenceStore() *mockPersistenceStore {
	return &mockPersistenceStore{
		data: make(map[string][]byte),
		ttls: make(map[string]time.Duration),
	}
}

func (m *mockPersistenceStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}

func (m *mockPersistenceStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte{}, value...)
	return nil
}

func (m *mockPersistenceStore) PutWithTTL(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte{}, value...)
	m.ttls[key] = ttl
	return nil
}

func (m *mockPersistenceStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// ── Error-injection stores (used by error-path tests) ────────────────────────

type failOnPutStore struct {
	putErr error
}

func (s *failOnPutStore) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("key not found")
}
func (s *failOnPutStore) Put(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return s.putErr
}
func (s *failOnPutStore) Delete(_ context.Context, _ string) error { return nil }

// failOnJTIPutStore lets the first Put (JWT-secret storage) succeed but fails
// any subsequent Put (JTI storage during GenerateToken).
type failOnJTIPutStore struct {
	inner    *mockTokenStore
	putCalls int
}

func (s *failOnJTIPutStore) Get(c context.Context, key string) ([]byte, error) {
	return s.inner.Get(c, key)
}
func (s *failOnJTIPutStore) Delete(c context.Context, key string) error {
	return s.inner.Delete(c, key)
}
func (s *failOnJTIPutStore) Put(c context.Context, key string, value []byte, ttl time.Duration) error {
	s.putCalls++
	if s.putCalls > 1 {
		return errors.New("JTI store failure")
	}
	return s.inner.Put(c, key, value, ttl)
}

type failOnDeleteStore struct {
	inner     *mockTokenStore
	deleteErr error
}

func (s *failOnDeleteStore) Get(c context.Context, key string) ([]byte, error) {
	return s.inner.Get(c, key)
}
func (s *failOnDeleteStore) Put(c context.Context, key string, value []byte, ttl time.Duration) error {
	return s.inner.Put(c, key, value, ttl)
}
func (s *failOnDeleteStore) Delete(_ context.Context, _ string) error { return s.deleteErr }

// failOnNthPutStore succeeds the first N-1 Put calls, then fails.
type failOnNthPutStore struct {
	inner    *mockTokenStore
	putCalls int
	failOn   int
}

func (s *failOnNthPutStore) Get(c context.Context, key string) ([]byte, error) {
	return s.inner.Get(c, key)
}
func (s *failOnNthPutStore) Delete(c context.Context, key string) error {
	return s.inner.Delete(c, key)
}
func (s *failOnNthPutStore) Put(c context.Context, key string, value []byte, ttl time.Duration) error {
	s.putCalls++
	if s.putCalls == s.failOn {
		return errors.New("simulated disk full")
	}
	return s.inner.Put(c, key, value, ttl)
}
