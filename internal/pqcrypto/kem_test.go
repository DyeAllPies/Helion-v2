// internal/pqcrypto/kem_test.go
//
// Tests for ML-KEM-768 (Kyber) key-encapsulation primitive.

package pqcrypto_test

import (
	"bytes"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

func TestGenerateKEMKeyPair_ReturnsNonNil(t *testing.T) {
	pub, priv, err := pqcrypto.GenerateKEMKeyPair()
	if err != nil {
		t.Fatalf("GenerateKEMKeyPair: %v", err)
	}
	if pub == nil {
		t.Error("expected non-nil KEMPublicKey")
	}
	if priv == nil {
		t.Error("expected non-nil KEMPrivateKey")
	}
}

func TestKEM_EncapsulateDecapsulate_SharedSecretMatches(t *testing.T) {
	pub, priv, err := pqcrypto.GenerateKEMKeyPair()
	if err != nil {
		t.Fatalf("GenerateKEMKeyPair: %v", err)
	}

	ct, ss1, err := pub.Encapsulate()
	if err != nil {
		t.Fatalf("Encapsulate: %v", err)
	}

	ss2, err := priv.Decapsulate(ct)
	if err != nil {
		t.Fatalf("Decapsulate: %v", err)
	}

	if !bytes.Equal(ss1, ss2) {
		t.Error("encapsulate and decapsulate shared secrets do not match")
	}
}

func TestKEM_EncapsulateProducesNonEmptyCiphertext(t *testing.T) {
	pub, _, _ := pqcrypto.GenerateKEMKeyPair()
	ct, ss, err := pub.Encapsulate()
	if err != nil {
		t.Fatalf("Encapsulate: %v", err)
	}
	if len(ct) == 0 {
		t.Error("ciphertext should not be empty")
	}
	if len(ss) == 0 {
		t.Error("shared secret should not be empty")
	}
}
