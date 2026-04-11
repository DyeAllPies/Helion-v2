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
