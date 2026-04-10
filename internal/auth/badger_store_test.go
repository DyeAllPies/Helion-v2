// internal/auth/badger_store_test.go
//
// Tests for StoreAdapter — the bridge from persistence.Store to auth.TokenStore.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// ── in-process fake persistence store ────────────────────────────────────────

type fakePersister struct {
	data map[string][]byte
	ttls map[string]time.Duration
	err  error // if non-nil, all ops fail with this error
}

func newFakePersister() *fakePersister {
	return &fakePersister{
		data: make(map[string][]byte),
		ttls: make(map[string]time.Duration),
	}
}

func (f *fakePersister) Get(_ context.Context, key string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.data[key]
	if !ok {
		return nil, errors.New("key not found")
	}
	return append([]byte{}, v...), nil
}

func (f *fakePersister) Put(_ context.Context, key string, value []byte) error {
	if f.err != nil {
		return f.err
	}
	f.data[key] = append([]byte{}, value...)
	return nil
}

func (f *fakePersister) PutWithTTL(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.data[key] = append([]byte{}, value...)
	f.ttls[key] = ttl
	return nil
}

func (f *fakePersister) Delete(_ context.Context, key string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.data, key)
	return nil
}

// ── StoreAdapter tests ────────────────────────────────────────────────────────

func TestStoreAdapter_Put_Get_Roundtrip(t *testing.T) {
	adapter := auth.NewStoreAdapter(newFakePersister())

	if err := adapter.Put("mykey", []byte("myvalue"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := adapter.Get("mykey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "myvalue" {
		t.Errorf("want 'myvalue', got %q", got)
	}
}

func TestStoreAdapter_Get_MissingKey_ReturnsError(t *testing.T) {
	adapter := auth.NewStoreAdapter(newFakePersister())
	_, err := adapter.Get("nonexistent")
	if err == nil {
		t.Error("expected error for missing key, got nil")
	}
}

func TestStoreAdapter_Put_ZeroTTL_UsesPut(t *testing.T) {
	fp := newFakePersister()
	adapter := auth.NewStoreAdapter(fp)

	_ = adapter.Put("zerokey", []byte("v"), 0)

	// No TTL should be recorded.
	if _, hasTTL := fp.ttls["zerokey"]; hasTTL {
		t.Error("zero TTL should not call PutWithTTL")
	}
	if _, ok := fp.data["zerokey"]; !ok {
		t.Error("key should be stored")
	}
}

func TestStoreAdapter_Put_WithTTL_UsesPutWithTTL(t *testing.T) {
	fp := newFakePersister()
	adapter := auth.NewStoreAdapter(fp)

	ttl := 5 * time.Minute
	_ = adapter.Put("ttlkey", []byte("v"), ttl)

	if fp.ttls["ttlkey"] != ttl {
		t.Errorf("want ttl %v, got %v", ttl, fp.ttls["ttlkey"])
	}
}

func TestStoreAdapter_Delete_RemovesKey(t *testing.T) {
	fp := newFakePersister()
	adapter := auth.NewStoreAdapter(fp)

	_ = adapter.Put("delkey", []byte("v"), 0)
	if err := adapter.Delete("delkey"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := adapter.Get("delkey"); err == nil {
		t.Error("Get after Delete should return error")
	}
}

func TestStoreAdapter_Delete_NonexistentKey_NoError(t *testing.T) {
	adapter := auth.NewStoreAdapter(newFakePersister())
	if err := adapter.Delete("ghost"); err != nil {
		t.Errorf("Delete non-existent: %v", err)
	}
}

func TestStoreAdapter_Get_PersisterError_Propagates(t *testing.T) {
	fp := newFakePersister()
	fp.err = errors.New("db failure")
	adapter := auth.NewStoreAdapter(fp)

	_, err := adapter.Get("key")
	if err == nil {
		t.Error("expected error from persister, got nil")
	}
}

func TestStoreAdapter_Put_PersisterError_Propagates(t *testing.T) {
	fp := newFakePersister()
	fp.err = errors.New("db failure")
	adapter := auth.NewStoreAdapter(fp)

	if err := adapter.Put("key", []byte("v"), 0); err == nil {
		t.Error("expected error from persister, got nil")
	}
}

func TestStoreAdapter_PutWithTTL_PersisterError_Propagates(t *testing.T) {
	fp := newFakePersister()
	fp.err = errors.New("db failure")
	adapter := auth.NewStoreAdapter(fp)

	if err := adapter.Put("key", []byte("v"), time.Minute); err == nil {
		t.Error("expected error from persister PutWithTTL, got nil")
	}
}

func TestStoreAdapter_Delete_PersisterError_Propagates(t *testing.T) {
	fp := newFakePersister()
	fp.err = errors.New("db failure")
	adapter := auth.NewStoreAdapter(fp)

	if err := adapter.Delete("key"); err == nil {
		t.Error("expected error from persister Delete, got nil")
	}
}

func TestStoreAdapter_MultipleKeysIndependent(t *testing.T) {
	adapter := auth.NewStoreAdapter(newFakePersister())

	_ = adapter.Put("a", []byte("1"), 0)
	_ = adapter.Put("b", []byte("2"), 0)

	va, _ := adapter.Get("a")
	vb, _ := adapter.Get("b")

	if string(va) != "1" {
		t.Errorf("key a: want '1', got %q", va)
	}
	if string(vb) != "2" {
		t.Errorf("key b: want '2', got %q", vb)
	}
}
