// internal/pqcrypto/ca_from_pem_test.go
//
// Tests for NewCAFromPEM (read-only CA reconstruction from PEM).

package pqcrypto_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

func TestNewCAFromPEM_Success(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	parsed, err := pqcrypto.NewCAFromPEM(ca.CertPEM)
	if err != nil {
		t.Fatalf("NewCAFromPEM: %v", err)
	}
	if parsed == nil {
		t.Fatal("expected non-nil CA")
	}
	if parsed.Cert == nil {
		t.Error("expected Cert to be set")
	}
	if len(parsed.CertPEM) == 0 {
		t.Error("expected CertPEM to be preserved")
	}
}

func TestNewCAFromPEM_InvalidPEM_ReturnsError(t *testing.T) {
	_, err := pqcrypto.NewCAFromPEM([]byte("not valid pem"))
	if err == nil {
		t.Error("expected error for invalid PEM")
	}
}

func TestNewCAFromPEM_WrongBlockType_ReturnsError(t *testing.T) {
	// Valid PEM encoding but wrong block type
	fakePEM := []byte("-----BEGIN RSA PRIVATE KEY-----\nMIIBogIBAAJBALcpkS==\n-----END RSA PRIVATE KEY-----\n")
	_, err := pqcrypto.NewCAFromPEM(fakePEM)
	if err == nil {
		t.Error("expected error for non-CERTIFICATE PEM block")
	}
}

func TestNewCAFromPEM_EmptyInput_ReturnsError(t *testing.T) {
	_, err := pqcrypto.NewCAFromPEM([]byte{})
	if err == nil {
		t.Error("expected error for empty input")
	}
}
