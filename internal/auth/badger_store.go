// internal/auth/badger_store.go
//
// BadgerDB adapter for JWT token storage.
//
// This file provides StoreAdapter which bridges the persistence layer
// (which uses context.Context) to the auth.Store interface (which doesn't).

package auth

import (
	"context"
	"time"
)

// StoreAdapter wraps persistence.Store to match auth.TokenStore interface.
// This bridges the gap between the persistence layer and auth layer.
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

func (s *StoreAdapter) Get(key string) ([]byte, error) {
	return s.persister.Get(context.Background(), key)
}

func (s *StoreAdapter) Put(key string, value []byte, ttl time.Duration) error {
	if ttl > 0 {
		return s.persister.PutWithTTL(context.Background(), key, value, ttl)
	}
	return s.persister.Put(context.Background(), key, value)
}

func (s *StoreAdapter) Delete(key string) error {
	return s.persister.Delete(context.Background(), key)
}
