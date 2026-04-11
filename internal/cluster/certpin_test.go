// internal/cluster/certpin_test.go
//
// Tests for CertFingerprint and MemCertPinner.

package cluster_test

import (
	"context"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
)

func TestCertFingerprint_ConsistentForSameInput(t *testing.T) {
	der := []byte("fake-der-cert-bytes-for-testing")
	fp1 := cluster.CertFingerprint(der)
	fp2 := cluster.CertFingerprint(der)
	if fp1 != fp2 {
		t.Errorf("fingerprint not deterministic: %q vs %q", fp1, fp2)
	}
}

func TestCertFingerprint_DiffersForDifferentInputs(t *testing.T) {
	fp1 := cluster.CertFingerprint([]byte("cert-a"))
	fp2 := cluster.CertFingerprint([]byte("cert-b"))
	if fp1 == fp2 {
		t.Error("different inputs produced the same fingerprint")
	}
}

func TestCertFingerprint_IsHexString(t *testing.T) {
	fp := cluster.CertFingerprint([]byte("any-cert"))
	// SHA-256 produces 32 bytes → 64 hex chars
	if len(fp) != 64 {
		t.Errorf("expected 64-char hex fingerprint, got %d chars: %s", len(fp), fp)
	}
	for _, c := range fp {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			t.Errorf("non-hex character %q in fingerprint %s", c, fp)
		}
	}
}

func TestMemCertPinner_SetAndGet_Roundtrip(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemCertPinner()

	if err := p.SetPin(ctx, "node-1", "abc123"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	got, err := p.GetPin(ctx, "node-1")
	if err != nil {
		t.Fatalf("GetPin: %v", err)
	}
	if got != "abc123" {
		t.Errorf("want pin 'abc123', got %q", got)
	}
}

func TestMemCertPinner_GetPin_MissingNode_ReturnsError(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemCertPinner()

	_, err := p.GetPin(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for missing node, got nil")
	}
}

func TestMemCertPinner_DeletePin_RemovesEntry(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemCertPinner()

	_ = p.SetPin(ctx, "node-del", "fp")
	if err := p.DeletePin(ctx, "node-del"); err != nil {
		t.Fatalf("DeletePin: %v", err)
	}
	_, err := p.GetPin(ctx, "node-del")
	if err == nil {
		t.Error("expected error after DeletePin, got nil")
	}
}

func TestMemCertPinner_DeletePin_NonexistentKey_NoError(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemCertPinner()
	if err := p.DeletePin(ctx, "ghost"); err != nil {
		t.Errorf("DeletePin non-existent key: %v", err)
	}
}

func TestMemCertPinner_MultipleNodes_Independent(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemCertPinner()

	_ = p.SetPin(ctx, "a", "fp-a")
	_ = p.SetPin(ctx, "b", "fp-b")

	ga, _ := p.GetPin(ctx, "a")
	gb, _ := p.GetPin(ctx, "b")

	if ga != "fp-a" {
		t.Errorf("node-a: want 'fp-a', got %q", ga)
	}
	if gb != "fp-b" {
		t.Errorf("node-b: want 'fp-b', got %q", gb)
	}
}

func TestMemCertPinner_Overwrite_UpdatesPin(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemCertPinner()

	_ = p.SetPin(ctx, "node-x", "old")
	_ = p.SetPin(ctx, "node-x", "new")

	got, _ := p.GetPin(ctx, "node-x")
	if got != "new" {
		t.Errorf("want 'new', got %q", got)
	}
}

// ── NewConfiguredCertPinner (AUDIT M5) ───────────────────────────────────────

func TestNewConfiguredCertPinner_PreConfiguredPinsReadBack(t *testing.T) {
	ctx := context.Background()
	pins := map[string]string{
		"alpha": "fp-alpha",
		"beta":  "fp-beta",
	}
	p := cluster.NewConfiguredCertPinner(pins)

	got, err := p.GetPin(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetPin alpha: %v", err)
	}
	if got != "fp-alpha" {
		t.Errorf("alpha: want 'fp-alpha', got %q", got)
	}

	got, err = p.GetPin(ctx, "beta")
	if err != nil {
		t.Fatalf("GetPin beta: %v", err)
	}
	if got != "fp-beta" {
		t.Errorf("beta: want 'fp-beta', got %q", got)
	}
}

func TestNewConfiguredCertPinner_UnconfiguredNodeFallsBack(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewConfiguredCertPinner(map[string]string{"alpha": "fp-a"})

	// Unknown node should behave like MemCertPinner — GetPin returns error,
	// SetPin records the new fingerprint (first-seen fallback).
	if _, err := p.GetPin(ctx, "ghost"); err == nil {
		t.Error("unconfigured node: GetPin should return error, got nil")
	}

	if err := p.SetPin(ctx, "ghost", "fp-ghost"); err != nil {
		t.Fatalf("SetPin on unconfigured: %v", err)
	}
	got, _ := p.GetPin(ctx, "ghost")
	if got != "fp-ghost" {
		t.Errorf("first-seen fallback: want 'fp-ghost', got %q", got)
	}
}

func TestNewConfiguredCertPinner_NilMap_BehavesLikeEmpty(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewConfiguredCertPinner(nil)
	if _, err := p.GetPin(ctx, "anything"); err == nil {
		t.Error("nil map: GetPin should return error")
	}
}

func TestNewConfiguredCertPinner_ConfiguredPinIsImmutableOnSet(t *testing.T) {
	// An attacker re-registering for a pre-configured node should overwrite
	// the pin via SetPin — but Registry.Register never calls SetPin when
	// GetPin already returned a value, so the pin is effectively immutable
	// through the normal register path. This test documents the underlying
	// MemCertPinner behaviour: SetPin does overwrite, so the protection
	// lives in Registry.Register's flow, not in the pinner itself.
	ctx := context.Background()
	p := cluster.NewConfiguredCertPinner(map[string]string{"alpha": "original"})

	// The pinner permits overwrites at the storage layer.
	_ = p.SetPin(ctx, "alpha", "attacker")
	got, _ := p.GetPin(ctx, "alpha")
	if got != "attacker" {
		t.Errorf("pinner allows SetPin overwrite: want 'attacker', got %q", got)
	}
	// Registry.Register would NOT reach SetPin here because GetPin first
	// returned "original" successfully; the mismatch branch rejects instead.
}
