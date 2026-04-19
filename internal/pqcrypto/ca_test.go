// internal/pqcrypto/ca_test.go
//
// Tests for CA construction, node cert issuance, standard TLS config,
// and CA/node TTL env var overrides.

package pqcrypto_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"
	"time"

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
	if cfg.ClientAuth != tls.RequireAnyClientCert {
		t.Errorf("want RequireAnyClientCert, got %v", cfg.ClientAuth)
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

// ── Feature 27 — IssueOperatorCert ────────────────────────────────────────────

func TestIssueOperatorCert_RejectsEmptyCN(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if _, _, err := ca.IssueOperatorCert("", 0); err == nil {
		t.Error("empty CN: want err")
	}
}

func TestIssueOperatorCert_RejectsNULInCN(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if _, _, err := ca.IssueOperatorCert("alice\x00evil", 0); err == nil {
		t.Error("NUL in CN: want err")
	}
}

func TestIssueOperatorCert_RejectsReadOnlyCA(t *testing.T) {
	// A CA loaded from PEM has no private key and must refuse to sign.
	// Regression guard: IssueOperatorCert on a read-only CA must not
	// panic on a nil private key — it must return a clean error so the
	// operator-cert CLI fails loudly rather than mysteriously.
	fullCA, _ := pqcrypto.NewCA()
	readOnly, err := pqcrypto.NewCAFromPEM(fullCA.CertPEM)
	if err != nil {
		t.Fatalf("NewCAFromPEM: %v", err)
	}
	if _, _, err := readOnly.IssueOperatorCert("alice", 0); err == nil {
		t.Error("read-only CA: want err")
	}
}

func TestIssueOperatorCert_HappyPath_HasClientAuthEKU(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, keyPEM, err := ca.IssueOperatorCert("alice@ops", 0)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("IssueOperatorCert returned empty PEM")
	}
	cert := parseCertPEM(t, certPEM)
	if cert.Subject.CommonName != "alice@ops" {
		t.Errorf("CN: want alice@ops, got %q", cert.Subject.CommonName)
	}
	hasClient, hasServer := false, false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
		if eku == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
	}
	if !hasClient {
		t.Error("operator cert must carry ClientAuth EKU")
	}
	if hasServer {
		t.Error("operator cert must NOT carry ServerAuth EKU (belt-and-braces: a leaked operator cert must not be usable as a server cert)")
	}
}

func TestIssueOperatorCert_SignedByCA(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, _, err := ca.IssueOperatorCert("alice", 0)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	pool := ca.ClientCertPool()
	opts := x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err != nil {
		t.Errorf("operator cert should verify against its issuing CA: %v", err)
	}
}

func TestIssueOperatorCert_DifferentCACannotVerify(t *testing.T) {
	caA, _ := pqcrypto.NewCA()
	caB, _ := pqcrypto.NewCA()
	certPEM, _, err := caA.IssueOperatorCert("alice", 0)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	opts := x509.VerifyOptions{
		Roots:     caB.ClientCertPool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err == nil {
		t.Error("cert signed by CA A should NOT verify against CA B")
	}
}

func TestIssueOperatorCert_TTLRespected(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	ttl := 24 * time.Hour
	certPEM, _, err := ca.IssueOperatorCert("alice", ttl)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	got := cert.NotAfter.Sub(cert.NotBefore)
	// Allow ±5s for generation timing.
	if got < ttl-5*time.Second || got > ttl+5*time.Second {
		t.Errorf("NotAfter-NotBefore = %v, want ≈ %v", got, ttl)
	}
}

func TestIssueOperatorCert_TTLCappedAtCALifetime(t *testing.T) {
	// Regression guard: asking for a TTL longer than the CA itself
	// would leave the operator cert valid past the CA's expiry. The
	// impl clamps TTL to time.Until(ca.NotAfter). We can't easily
	// construct a CA with a short expiry, so assert the return
	// succeeds for a huge TTL and the resulting NotAfter <= CA.NotAfter.
	ca, _ := pqcrypto.NewCA()
	certPEM, _, err := ca.IssueOperatorCert("alice", 100*365*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueOperatorCert huge TTL: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	if cert.NotAfter.After(ca.Cert.NotAfter) {
		t.Errorf("operator NotAfter %v outlives CA NotAfter %v — TTL clamp broken",
			cert.NotAfter, ca.Cert.NotAfter)
	}
}

func TestClientCertPool_Verifies(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, _, err := ca.IssueOperatorCert("alice", 0)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	pool := ca.ClientCertPool()
	opts := x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err != nil {
		t.Errorf("operator cert should verify against ClientCertPool: %v", err)
	}
}
