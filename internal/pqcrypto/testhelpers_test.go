// internal/pqcrypto/testhelpers_test.go
//
// Shared helpers and fixtures for pqcrypto test files.

package pqcrypto_test

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"testing"
)

// oidPQCSignatureExt is 1.3.6.1.4.1.11129.2.1.27 (from mldsa.go — must stay in sync).
var oidPQCSignatureExt = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 1, 27}

// oidMLDSA65 is 1.3.6.1.4.1.2.267.7.8.7 (Dilithium-3 — from mldsa.go).
var oidMLDSA65 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 2, 267, 7, 8, 7}

// badPEM is reused by multiple TLS-config error tests.
var badPEM = []byte("this is not valid PEM data")

// parseCertPEM decodes PEM and parses the first certificate.
func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}
	return cert
}

// makeMLDSAExtension creates a DER-encoded PQC signature extension value.
func makeMLDSAExtension(t *testing.T, algOID asn1.ObjectIdentifier, sig []byte) []byte {
	t.Helper()
	b, err := asn1.Marshal(struct {
		Algorithm asn1.ObjectIdentifier
		Signature []byte
	}{
		Algorithm: algOID,
		Signature: sig,
	})
	if err != nil {
		t.Fatalf("asn1.Marshal extension: %v", err)
	}
	return b
}

// certWithMLDSAExt returns a shallow copy of cert with the given extension appended.
func certWithMLDSAExt(cert *x509.Certificate, extValue []byte) *x509.Certificate {
	c := *cert
	c.Extensions = append(append([]pkix.Extension{}, cert.Extensions...), pkix.Extension{
		Id:    oidPQCSignatureExt,
		Value: extValue,
	})
	return &c
}
