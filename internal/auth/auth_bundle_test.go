// internal/auth/auth_bundle_test.go
//
// Tests for auth.Bundle credential methods — specifically the error paths
// that are triggered by malformed PEM data.
package auth_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// badPEM is clearly invalid PEM — tls.X509KeyPair will reject it.
var badPEM = []byte("this is not valid PEM data")

// newBundleWithBadPEM builds a real CA but replaces the cert/key with garbage
// so that any TLS config construction will fail.
func newBundleWithBadPEM(t *testing.T) *auth.Bundle {
	t.Helper()
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return &auth.Bundle{CA: ca, CertPEM: badPEM, KeyPEM: badPEM}
}

// ── ServerCredentials error path ──────────────────────────────────────────────

func TestServerCredentials_InvalidPEM_ReturnsError(t *testing.T) {
	b := newBundleWithBadPEM(t)
	_, err := b.ServerCredentials()
	if err == nil {
		t.Error("expected error for invalid cert/key PEM, got nil")
	}
}

// ── ClientCredentials error path ──────────────────────────────────────────────

func TestClientCredentials_InvalidPEM_ReturnsError(t *testing.T) {
	b := newBundleWithBadPEM(t)
	_, err := b.ClientCredentials("helion-coordinator")
	if err == nil {
		t.Error("expected error for invalid cert/key PEM, got nil")
	}
}

// ── NewNodeBundle ─────────────────────────────────────────────────────────────

// TestNewNodeBundle_Success exercises the happy path (not just error path).
func TestNewNodeBundle_Success(t *testing.T) {
	cb, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	nb, err := auth.NewNodeBundle(cb.CA, "test-node")
	if err != nil {
		t.Fatalf("NewNodeBundle: %v", err)
	}
	if nb == nil {
		t.Fatal("expected non-nil Bundle")
	}
}
