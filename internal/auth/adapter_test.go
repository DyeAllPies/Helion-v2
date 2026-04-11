// internal/auth/adapter_test.go
//
// Tests for StoreAdapter (wraps persistence.Store as auth.TokenStore) and
// the ML-DSA path of NewNodeBundle.

package auth_test

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// ── StoreAdapter ──────────────────────────────────────────────────────────────

func TestStoreAdapter_GetPutDelete(t *testing.T) {
	inner := newPersistenceStore()
	adapter := auth.NewStoreAdapter(inner)

	if err := adapter.Put(ctx, "k1", []byte("value"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := adapter.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Errorf("want 'value', got %q", got)
	}

	if err := adapter.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := adapter.Get(ctx, "k1"); err == nil {
		t.Error("Get after Delete should return error")
	}
}

func TestStoreAdapter_PutWithTTL_UsesPutWithTTL(t *testing.T) {
	inner := newPersistenceStore()
	adapter := auth.NewStoreAdapter(inner)

	ttl := 15 * time.Minute
	if err := adapter.Put(ctx, "k-ttl", []byte("v"), ttl); err != nil {
		t.Fatalf("Put with TTL: %v", err)
	}

	if inner.ttls["k-ttl"] != ttl {
		t.Errorf("want ttl %v, got %v", ttl, inner.ttls["k-ttl"])
	}
}

// ── NewNodeBundle — ML-DSA path ───────────────────────────────────────────────

func TestNewNodeBundle_WithMLDSAEnabled_UsesMLDSAPath(t *testing.T) {
	b, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	// Enable ML-DSA on the CA so NewNodeBundle takes the IssueNodeCertWithMLDSA path.
	if err := b.CA.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}
	nb, err := auth.NewNodeBundle(b.CA, "node-mldsa")
	if err != nil {
		t.Fatalf("NewNodeBundle with ML-DSA: %v", err)
	}
	if len(nb.CertPEM) == 0 {
		t.Error("expected non-empty CertPEM")
	}
}
