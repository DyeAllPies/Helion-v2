// internal/auth/badger_store.go
//
// BadgerDB adapter for JWT token storage.
//
// StoreAdapter bridges the persistence layer (context-aware) to the
// auth.TokenStore interface, passing the caller's context through so
// that request cancellation and deadlines are respected.

package auth

import (
	"context"
	"time"
)

// StoreAdapter wraps a persistence.Store to implement auth.TokenStore.
type StoreAdapter struct {
	persister interface {
		Get(ctx context.Context, key string) ([]byte, error)
		Put(ctx context.Context, key string, value []byte) error
		PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error
		Delete(ctx context.Context, key string) error
	}
}

// NewStoreAdapter creates a TokenStore adapter from a persistence.Store.
func NewStoreAdapter(persister interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}) TokenStore {
	return &StoreAdapter{persister: persister}
}

func (s *StoreAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	return s.persister.Get(ctx, key)
}

func (s *StoreAdapter) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl > 0 {
		return s.persister.PutWithTTL(ctx, key, value, ttl)
	}
	return s.persister.Put(ctx, key, value)
}

func (s *StoreAdapter) Delete(ctx context.Context, key string) error {
	return s.persister.Delete(ctx, key)
}
