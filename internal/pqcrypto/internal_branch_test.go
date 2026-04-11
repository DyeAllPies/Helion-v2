// internal/pqcrypto/internal_branch_test.go
//
// Package-internal tests that reach into unexported helpers to exercise
// branches unreachable from the external pqcrypto_test package.

package pqcrypto

import (
	"testing"
)

// ── parsePEMBlock ─────────────────────────────────────────────────────────────

func TestParsePEMBlock_NoBlock_ReturnsError(t *testing.T) {
	_, err := parsePEMBlock([]byte("not valid PEM"), "CERTIFICATE")
	if err == nil {
		t.Error("expected error for input without a PEM block")
	}
}

func TestParsePEMBlock_WrongType_ReturnsError(t *testing.T) {
	// A valid PEM block but the wrong type label.
	keyPEM := []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n")
	_, err := parsePEMBlock(keyPEM, "CERTIFICATE")
	if err == nil {
		t.Error("expected error for wrong PEM block type")
	}
}

// ── KEM error paths ──────────────────────────────────────────────────────────

// TestKEMPublicKey_Encapsulate_CorruptedRaw covers the
// UnmarshalBinaryPublicKey error branch in (*KEMPublicKey).Encapsulate.
// We construct a key with a bogus raw slice directly since the field is
// unexported and unreachable from the external test package.
func TestKEMPublicKey_Encapsulate_CorruptedRaw(t *testing.T) {
	pub := &KEMPublicKey{raw: []byte("not a valid ML-KEM public key")}
	_, _, err := pub.Encapsulate()
	if err == nil {
		t.Error("expected error from Encapsulate on corrupted raw key")
	}
}

// TestKEMPrivateKey_Decapsulate_MalformedCiphertext covers the error branch
// in (*KEMPrivateKey).Decapsulate.
func TestKEMPrivateKey_Decapsulate_MalformedCiphertext(t *testing.T) {
	_, priv, err := GenerateKEMKeyPair()
	if err != nil {
		t.Fatalf("GenerateKEMKeyPair: %v", err)
	}
	_, err = priv.Decapsulate([]byte("too-short-ciphertext"))
	if err == nil {
		t.Error("expected error from Decapsulate on malformed ciphertext")
	}
}

// ── ML-DSA error paths ──────────────────────────────────────────────────────

// TestMLDSAPublicKey_Verify_CorruptedRaw covers the UnmarshalBinaryPublicKey
// error branch in (*MLDSAPublicKey).Verify.
func TestMLDSAPublicKey_Verify_CorruptedRaw(t *testing.T) {
	pub := &MLDSAPublicKey{raw: []byte("not a valid ML-DSA public key")}
	err := pub.Verify([]byte("msg"), []byte("sig"))
	if err == nil {
		t.Error("expected error from Verify on corrupted raw key")
	}
}
