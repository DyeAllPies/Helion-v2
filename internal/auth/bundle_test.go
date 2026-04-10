package auth_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

func TestNewCoordinatorBundle_ReturnsBundle(t *testing.T) {
	b, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil bundle")
	}
	if b.CA == nil {
		t.Error("bundle.CA should not be nil")
	}
	if len(b.CertPEM) == 0 {
		t.Error("bundle.CertPEM should not be empty")
	}
	if len(b.KeyPEM) == 0 {
		t.Error("bundle.KeyPEM should not be empty")
	}
}

func TestNewNodeBundle_ReturnsBundle(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	b, err := auth.NewNodeBundle(ca, "node-1")
	if err != nil {
		t.Fatalf("NewNodeBundle: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil bundle")
	}
	if len(b.CertPEM) == 0 {
		t.Error("bundle.CertPEM should not be empty")
	}
}

func TestServerCredentials_ReturnsCredentials(t *testing.T) {
	b, _ := auth.NewCoordinatorBundle()
	creds, err := b.ServerCredentials()
	if err != nil {
		t.Fatalf("ServerCredentials: %v", err)
	}
	if creds == nil {
		t.Fatal("expected non-nil credentials")
	}
}

func TestClientCredentials_ReturnsCredentials(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	b, _ := auth.NewNodeBundle(ca, "node-1")
	creds, err := b.ClientCredentials("helion-coordinator")
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if creds == nil {
		t.Fatal("expected non-nil credentials")
	}
}

func TestRawTLSConfig_ReturnsConfig(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	b, _ := auth.NewNodeBundle(ca, "node-1")
	cfg, err := b.RawTLSConfig("helion-coordinator")
	if err != nil {
		t.Fatalf("RawTLSConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if cfg.ServerName != "helion-coordinator" {
		t.Errorf("want ServerName=helion-coordinator, got %q", cfg.ServerName)
	}
}
