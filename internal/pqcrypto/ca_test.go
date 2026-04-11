// internal/pqcrypto/ca_test.go
//
// Tests for CA construction, node cert issuance, standard TLS config,
// and CA/node TTL env var overrides.

package pqcrypto_test

import (
	"bytes"
	"crypto/tls"
	"os"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// ── CA ────────────────────────────────────────────────────────────────────────

func TestNewCA_ReturnsNonNil(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if ca == nil {
		t.Fatal("expected non-nil CA")
	}
}

func TestNewCA_HasCertAndPEM(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if ca.Cert == nil {
		t.Error("CA.Cert should not be nil")
	}
	if len(ca.CertPEM) == 0 {
		t.Error("CA.CertPEM should not be empty")
	}
}

func TestNewCA_IsCA(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if !ca.Cert.IsCA {
		t.Error("CA certificate should have IsCA=true")
	}
}

func TestIssueNodeCert_ReturnsPEMPair(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, keyPEM, err := ca.IssueNodeCert("node-1")
	if err != nil {
		t.Fatalf("IssueNodeCert: %v", err)
	}
	if len(certPEM) == 0 {
		t.Error("certPEM should not be empty")
	}
	if len(keyPEM) == 0 {
		t.Error("keyPEM should not be empty")
	}
}

func TestIssueNodeCert_CertIsValidForTLS(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, keyPEM, err := ca.IssueNodeCert("node-tls-test")
	if err != nil {
		t.Fatalf("IssueNodeCert: %v", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Errorf("issued cert/key pair is not a valid TLS key pair: %v", err)
	}
}

func TestIssueNodeCert_DifferentNodesDifferentCerts(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	cert1, _, _ := ca.IssueNodeCert("node-a")
	cert2, _, _ := ca.IssueNodeCert("node-b")
	if bytes.Equal(cert1, cert2) {
		t.Error("different nodes should produce different certificates")
	}
}

func TestTLSConfig_ReturnsConfig(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, keyPEM, _ := ca.IssueNodeCert("coord")
	cfg, err := ca.TLSConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("want MinVersion TLS 1.3, got %d", cfg.MinVersion)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("want RequireAndVerifyClientCert, got %v", cfg.ClientAuth)
	}
}

func TestNodeTLSConfig_ReturnsConfig(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, keyPEM, _ := ca.IssueNodeCert("node-1")
	cfg, err := ca.NodeTLSConfig(certPEM, keyPEM, "coordinator")
	if err != nil {
		t.Fatalf("NodeTLSConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if cfg.ServerName != "coordinator" {
		t.Errorf("want ServerName=coordinator, got %q", cfg.ServerName)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("want MinVersion TLS 1.3, got %d", cfg.MinVersion)
	}
}

func TestTLSConfig_InvalidKey_ReturnsError(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, _, _ := ca.IssueNodeCert("node-1")
	_, err := ca.TLSConfig(certPEM, []byte("not a valid key"))
	if err == nil {
		t.Error("expected error for mismatched cert/key")
	}
}

func TestNodeTLSConfig_InvalidPEM_ReturnsError(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, err = ca.NodeTLSConfig(badPEM, badPEM, "coordinator")
	if err == nil {
		t.Error("expected error for invalid cert/key PEM, got nil")
	}
}

// ── envDuration (indirect coverage via NewCA / IssueNodeCert) ────────────────

func TestNewCA_WithValidCATTLEnv_UsesEnvValue(t *testing.T) {
	t.Setenv("HELION_CA_CERT_TTL_DAYS", "1")
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA with env TTL: %v", err)
	}
	if ca == nil {
		t.Fatal("expected non-nil CA")
	}
}

func TestNewCA_WithInvalidCATTLEnv_UsesDefault(t *testing.T) {
	t.Setenv("HELION_CA_CERT_TTL_DAYS", "notanumber")
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA with invalid env TTL: %v", err)
	}
	if ca == nil {
		t.Fatal("expected non-nil CA")
	}
}

func TestNewCA_WithZeroCATTLEnv_UsesDefault(t *testing.T) {
	t.Setenv("HELION_CA_CERT_TTL_DAYS", "0")
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA with zero env TTL: %v", err)
	}
	if ca == nil {
		t.Fatal("expected non-nil CA")
	}
}

func TestIssueNodeCert_WithValidTTLEnv_UsesEnvValue(t *testing.T) {
	t.Setenv("HELION_NODE_CERT_TTL_HOURS", "48")
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	certPEM, keyPEM, err := ca.IssueNodeCert("node-ttl-env")
	if err != nil {
		t.Fatalf("IssueNodeCert with env TTL: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Error("expected non-empty cert and key PEM")
	}
}

func TestIssueNodeCert_WithInvalidTTLEnv_UsesDefault(t *testing.T) {
	t.Setenv("HELION_NODE_CERT_TTL_HOURS", "bad")
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, _, err = ca.IssueNodeCert("node-bad-ttl")
	if err != nil {
		t.Fatalf("IssueNodeCert with invalid env TTL: %v", err)
	}
}

func TestEnvDuration_UnsetVar_UsesDefault(t *testing.T) {
	os.Unsetenv("HELION_CA_CERT_TTL_DAYS")
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA with unset env var: %v", err)
	}
	if ca == nil {
		t.Fatal("expected non-nil CA")
	}
}
