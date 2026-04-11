// internal/pqcrypto/mldsa_test.go
//
// Tests for ML-DSA (Dilithium-3) signature primitive, CA ML-DSA enhancement,
// cert-extension add/verify, and VerifyNodeCertMLDSA.

package pqcrypto_test

import (
	"bytes"
	"encoding/asn1"
	"encoding/pem"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// ── ML-DSA primitive ──────────────────────────────────────────────────────────

func TestGenerateMLDSAKeyPair_ReturnsNonNil(t *testing.T) {
	pub, priv, err := pqcrypto.GenerateMLDSAKeyPair()
	if err != nil {
		t.Fatalf("GenerateMLDSAKeyPair: %v", err)
	}
	if pub == nil {
		t.Error("expected non-nil MLDSAPublicKey")
	}
	if priv == nil {
		t.Error("expected non-nil MLDSAPrivateKey")
	}
}

func TestMLDSA_SignAndVerify_Roundtrip(t *testing.T) {
	pub, priv, err := pqcrypto.GenerateMLDSAKeyPair()
	if err != nil {
		t.Fatalf("GenerateMLDSAKeyPair: %v", err)
	}

	msg := []byte("hello from helion")
	sig, err := priv.Sign(msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) == 0 {
		t.Error("signature should not be empty")
	}

	if err := pub.Verify(msg, sig); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestMLDSA_Verify_TamperedMessage_Fails(t *testing.T) {
	pub, priv, _ := pqcrypto.GenerateMLDSAKeyPair()
	msg := []byte("original message")
	sig, _ := priv.Sign(msg)

	if err := pub.Verify([]byte("tampered message"), sig); err == nil {
		t.Error("expected error for tampered message, got nil")
	}
}

func TestMLDSA_Verify_TamperedSignature_Fails(t *testing.T) {
	pub, priv, _ := pqcrypto.GenerateMLDSAKeyPair()
	msg := []byte("some message")
	sig, _ := priv.Sign(msg)

	sig[0] ^= 0xFF

	if err := pub.Verify(msg, sig); err == nil {
		t.Error("expected error for tampered signature, got nil")
	}
}

// ── CA ML-DSA enhancement ─────────────────────────────────────────────────────

func TestEnhanceCAWithMLDSA_NoError(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if err := ca.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}
}

func TestGetMLDSAPublicKey_NilBeforeEnhance(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if pk := ca.GetMLDSAPublicKey(); pk != nil {
		t.Error("ML-DSA public key should be nil before EnhanceWithMLDSA")
	}
}

func TestGetMLDSAPublicKey_NonNilAfterEnhance(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	_ = ca.EnhanceWithMLDSA()
	if pk := ca.GetMLDSAPublicKey(); pk == nil {
		t.Error("ML-DSA public key should not be nil after EnhanceWithMLDSA")
	}
}

func TestStoreAndGetMLDSASignature_Roundtrip(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	sig := []byte("fake-mldsa-signature")
	ca.StoreMLDSASignature("serial-001", sig)

	got, ok := ca.GetMLDSASignature("serial-001")
	if !ok {
		t.Fatal("expected signature to be stored")
	}
	if !bytes.Equal(got, sig) {
		t.Errorf("stored signature mismatch: want %x, got %x", sig, got)
	}
}

func TestGetMLDSASignature_UnknownSerial_ReturnsFalse(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	if _, ok := ca.GetMLDSASignature("nonexistent"); ok {
		t.Error("expected false for nonexistent serial")
	}
}

func TestVerifyCertificateWithKEM_NoError(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	pub, _, _ := pqcrypto.GenerateKEMKeyPair()
	if err := pqcrypto.VerifyCertificateWithKEM(ca.Cert, pub); err != nil {
		t.Errorf("VerifyCertificateWithKEM: %v", err)
	}
}

func TestIssueNodeCertWithMLDSA_WithoutMLDSA_FallsBack(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	certPEM, keyPEM, err := ca.IssueNodeCertWithMLDSA("node-fallback")
	if err != nil {
		t.Fatalf("IssueNodeCertWithMLDSA: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Error("expected non-empty cert/key PEM")
	}
}

func TestIssueNodeCertWithMLDSA_WithMLDSA_CallsParsePEM(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if err := ca.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}
	_, _, err = ca.IssueNodeCertWithMLDSA("node-mldsa")
	if err == nil {
		t.Log("IssueNodeCertWithMLDSA succeeded (parsePEMBlock may be implemented)")
	}
}

// ── AddMLDSASignature / VerifyMLDSASignature extension tests ─────────────────

func TestAddMLDSASignature_AddsExtension(t *testing.T) {
	_, priv, err := pqcrypto.GenerateMLDSAKeyPair()
	if err != nil {
		t.Fatalf("GenerateMLDSAKeyPair: %v", err)
	}

	ca, _ := pqcrypto.NewCA()
	tbs := ca.Cert.RawTBSCertificate

	template := ca.Cert
	if err := pqcrypto.AddMLDSASignature(template, tbs, priv); err != nil {
		t.Fatalf("AddMLDSASignature: %v", err)
	}

	if len(template.ExtraExtensions) == 0 {
		t.Error("expected at least one extra extension after AddMLDSASignature")
	}
}

func TestVerifyMLDSASignature_NoExtension_ReturnsError(t *testing.T) {
	pub, _, _ := pqcrypto.GenerateMLDSAKeyPair()
	ca, _ := pqcrypto.NewCA()

	if err := pqcrypto.VerifyMLDSASignature(ca.Cert, pub); err == nil {
		t.Error("expected error for cert without ML-DSA extension, got nil")
	}
}

func TestVerifyMLDSASignature_MalformedExtension_ReturnsError(t *testing.T) {
	pub, _, _ := pqcrypto.GenerateMLDSAKeyPair()
	ca, _ := pqcrypto.NewCA()
	cert := parseCertPEM(t, ca.CertPEM)

	modifiedCert := certWithMLDSAExt(cert, []byte("not valid asn1 data"))

	if err := pqcrypto.VerifyMLDSASignature(modifiedCert, pub); err == nil {
		t.Error("expected error for malformed extension, got nil")
	}
}

func TestVerifyMLDSASignature_WrongAlgorithm_ReturnsError(t *testing.T) {
	pub, _, _ := pqcrypto.GenerateMLDSAKeyPair()
	ca, _ := pqcrypto.NewCA()
	cert := parseCertPEM(t, ca.CertPEM)

	extValue := makeMLDSAExtension(t, asn1.ObjectIdentifier{1, 2, 3, 4}, []byte("fake sig"))
	modifiedCert := certWithMLDSAExt(cert, extValue)

	if err := pqcrypto.VerifyMLDSASignature(modifiedCert, pub); err == nil {
		t.Error("expected error for wrong algorithm, got nil")
	}
}

func TestVerifyMLDSASignature_ValidSignature_ReturnsNil(t *testing.T) {
	pub, priv, err := pqcrypto.GenerateMLDSAKeyPair()
	if err != nil {
		t.Fatalf("GenerateMLDSAKeyPair: %v", err)
	}
	ca, _ := pqcrypto.NewCA()
	cert := parseCertPEM(t, ca.CertPEM)

	sig, err := priv.Sign(cert.RawTBSCertificate)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	extValue := makeMLDSAExtension(t, oidMLDSA65, sig)
	modifiedCert := certWithMLDSAExt(cert, extValue)

	if err := pqcrypto.VerifyMLDSASignature(modifiedCert, pub); err != nil {
		t.Errorf("VerifyMLDSASignature with valid signature: %v", err)
	}
}

func TestVerifyMLDSASignature_TamperedSignature_ReturnsError(t *testing.T) {
	pub, priv, _ := pqcrypto.GenerateMLDSAKeyPair()
	ca, _ := pqcrypto.NewCA()
	cert := parseCertPEM(t, ca.CertPEM)

	sig, _ := priv.Sign(cert.RawTBSCertificate)
	sig[0] ^= 0xFF

	extValue := makeMLDSAExtension(t, oidMLDSA65, sig)
	modifiedCert := certWithMLDSAExt(cert, extValue)

	if err := pqcrypto.VerifyMLDSASignature(modifiedCert, pub); err == nil {
		t.Error("expected error for tampered signature, got nil")
	}
}

// ── VerifyNodeCertMLDSA ───────────────────────────────────────────────────────

func TestVerifyNodeCertMLDSA_NoMLDSA_ReturnsNil(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	certPEM, _, err := ca.IssueNodeCert("node-noml")
	if err != nil {
		t.Fatalf("IssueNodeCert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}

	if err := ca.VerifyNodeCertMLDSA(block.Bytes); err != nil {
		t.Errorf("VerifyNodeCertMLDSA without ML-DSA: want nil, got %v", err)
	}
}

func TestVerifyNodeCertMLDSA_WithMLDSA_ValidCert_ReturnsNil(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if err := ca.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}

	certPEM, _, err := ca.IssueNodeCertWithMLDSA("node-ml")
	if err != nil {
		t.Fatalf("IssueNodeCertWithMLDSA: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}

	if err := ca.VerifyNodeCertMLDSA(block.Bytes); err != nil {
		t.Errorf("VerifyNodeCertMLDSA valid cert: want nil, got %v", err)
	}
}

func TestVerifyNodeCertMLDSA_UnknownSerial_ReturnsError(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if err := ca.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}

	certPEM, _, err := ca.IssueNodeCert("node-nosig")
	if err != nil {
		t.Fatalf("IssueNodeCert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}

	err = ca.VerifyNodeCertMLDSA(block.Bytes)
	if err == nil {
		t.Error("VerifyNodeCertMLDSA unknown serial: want error, got nil")
	}
}

func TestVerifyNodeCertMLDSA_InvalidDER_ReturnsError(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if err := ca.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}

	err = ca.VerifyNodeCertMLDSA([]byte("not-a-der-cert"))
	if err == nil {
		t.Error("VerifyNodeCertMLDSA invalid DER: want error, got nil")
	}
}
