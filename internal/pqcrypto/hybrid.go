// internal/pqcrypto/hybrid.go
//
// Hybrid post-quantum TLS implementation using X25519 + ML-KEM-768 (Kyber).
//
// Phase 4 enhancement: This file adds hybrid key exchange to the existing
// classical ECDSA-based TLS in ca.go. The hybrid approach provides forward
// secrecy even if either component (classical or PQC) is broken.
//
// Implementation uses:
//   - Cloudflare circl library for ML-KEM-768 (NIST FIPS 203 standard)
//   - Go 1.23 experimental X25519MLKEM768 curve ID where available
//   - Manual hybrid handshake for compatibility with older TLS libraries
//
// Design rationale:
// ─────────────────
// The TLS 1.3 handshake uses a single key exchange algorithm specified by the
// "supported_groups" extension in ClientHello. To achieve hybrid security,
// we use X25519MLKEM768 (curve ID 0x6399) which combines:
//   - X25519 ECDH (classical, 32 bytes)
//   - ML-KEM-768 encapsulation (PQC, 1088 bytes ciphertext)
//
// The shared secret is derived as:
//   shared_secret = KDF(x25519_secret || mlkem768_secret)
//
// This ensures that an adversary must break BOTH X25519 (discrete log) AND
// ML-KEM-768 (lattice problem) to compromise the session key. If either
// remains secure, forward secrecy is preserved.
//
// Harvest-now-decrypt-later (HNDL) resistance:
// ─────────────────────────────────────────────
// Classical ECDH (including X25519) is vulnerable to HNDL attacks by a future
// quantum computer running Shor's algorithm. ML-KEM-768 is designed to resist
// quantum attacks on the Learning With Errors (LWE) problem. By hybridizing,
// we protect against HNDL even if PQC is standardized slowly.

package pqcrypto

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/cloudflare/circl/kem"
	"github.com/cloudflare/circl/kem/mlkem/mlkem768"
)

// HybridConfig holds configuration for hybrid PQC TLS.
type HybridConfig struct {
	EnableHybridKEM bool   // Enable ML-KEM-768 + X25519 hybrid key exchange
	CurvePreference uint16 // TLS curve ID (0x6399 for X25519MLKEM768)
}

// DefaultHybridConfig returns the recommended hybrid PQC configuration.
// Uses X25519MLKEM768 (curve ID 0x6399) for Go 1.23+.
func DefaultHybridConfig() *HybridConfig {
	return &HybridConfig{
		EnableHybridKEM: true,
		CurvePreference: 0x6399, // X25519MLKEM768 (IETF draft standard)
	}
}

// ApplyHybridKEM configures a tls.Config to use hybrid key exchange.
// This function modifies the CurvePreferences to prefer X25519MLKEM768.
//
// NOTE: Actual hybrid KEM support requires Go 1.23+ with experimental
// post-quantum support enabled. For older Go versions, this falls back
// to classical X25519 only, which is still secure but not HNDL-resistant.
//
// To enable experimental PQ in Go 1.23+:
//   export GODEBUG=tlskyber=1
//
// Verification:
//   Use Wireshark to inspect the TLS handshake and verify that the
//   "supported_groups" extension includes 0x6399 (X25519MLKEM768) and
//   that the ServerHello uses it for key exchange.
func ApplyHybridKEM(cfg *tls.Config, hybridCfg *HybridConfig) *tls.Config {
	if cfg == nil {
		cfg = &tls.Config{}
	}

	if !hybridCfg.EnableHybridKEM {
		return cfg
	}

	// Prefer X25519MLKEM768 (0x6399), fall back to X25519 (0x001d).
	// If the peer doesn't support 0x6399, TLS will negotiate X25519 only.
	cfg.CurvePreferences = []tls.CurveID{
		tls.CurveID(hybridCfg.CurvePreference), // X25519MLKEM768
		tls.X25519,                              // X25519 fallback
	}

	return cfg
}

// ML-KEM-768 (Kyber) encapsulation for out-of-band key agreement.
// This is used for signing node certificates with PQC keys (Phase 4).
// The TLS handshake itself uses X25519MLKEM768 via CurvePreferences above.

// KEMPublicKey wraps an ML-KEM-768 public key.
type KEMPublicKey struct {
	raw []byte
}

// KEMPrivateKey wraps an ML-KEM-768 private key.
type KEMPrivateKey struct {
	scheme kem.Scheme
	sk     kem.PrivateKey
}

// GenerateKEMKeyPair generates a fresh ML-KEM-768 key pair.
// This is used for generating PQC keys for certificate signing (ML-DSA below).
// Returns the public key (for embedding in certificates) and private key.
func GenerateKEMKeyPair() (*KEMPublicKey, *KEMPrivateKey, error) {
	scheme := mlkem768.Scheme()
	pk, sk, err := scheme.GenerateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate ML-KEM-768 keypair: %w", err)
	}

	pubRaw, err := pk.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("marshal ML-KEM public key: %w", err)
	}

	return &KEMPublicKey{raw: pubRaw}, &KEMPrivateKey{scheme: scheme, sk: sk}, nil
}

// Encapsulate performs ML-KEM-768 encapsulation against the public key.
// Returns the ciphertext (to send to the peer) and the shared secret (to derive keys).
// The peer can decapsulate the ciphertext with their private key to recover the same shared secret.
func (pub *KEMPublicKey) Encapsulate() (ciphertext, sharedSecret []byte, err error) {
	scheme := mlkem768.Scheme()
	pk, err := scheme.UnmarshalBinaryPublicKey(pub.raw)
	if err != nil {
		return nil, nil, fmt.Errorf("unmarshal ML-KEM public key: %w", err)
	}

	ct, ss, err := scheme.Encapsulate(pk)
	if err != nil {
		return nil, nil, fmt.Errorf("ML-KEM encapsulate: %w", err)
	}

	return ct, ss, nil
}

// Decapsulate performs ML-KEM-768 decapsulation using the private key.
// Takes the ciphertext from the peer and returns the shared secret.
// The shared secret will match what the peer got from Encapsulate (if not tampered).
func (priv *KEMPrivateKey) Decapsulate(ciphertext []byte) (sharedSecret []byte, err error) {
	ss, err := priv.scheme.Decapsulate(priv.sk, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("ML-KEM decapsulate: %w", err)
	}
	return ss, nil
}

// EnhanceCAWithHybridKEM updates a CA's TLS config to use hybrid key exchange.
// This should be called after creating the CA but before issuing certificates.
func (ca *CA) EnhanceWithHybridKEM() {
	ca.hybridCfg = DefaultHybridConfig()
}

// EnhancedTLSConfig returns a TLS config with hybrid KEM support.
// This wraps the existing TLSConfig method and adds hybrid configuration.
func (ca *CA) EnhancedTLSConfig(certPEM, keyPEM []byte) (*tls.Config, error) {
	cfg, err := ca.TLSConfig(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	if ca.hybridCfg != nil {
		cfg = ApplyHybridKEM(cfg, ca.hybridCfg)
	}

	return cfg, nil
}

// EnhancedNodeTLSConfig returns a node TLS config with hybrid KEM support.
func (ca *CA) EnhancedNodeTLSConfig(certPEM, keyPEM []byte, serverName string) (*tls.Config, error) {
	cfg, err := ca.NodeTLSConfig(certPEM, keyPEM, serverName)
	if err != nil {
		return nil, err
	}

	if ca.hybridCfg != nil {
		cfg = ApplyHybridKEM(cfg, ca.hybridCfg)
	}

	return cfg, nil
}

// VerifyCertificateWithKEM verifies a certificate's signature using ML-KEM-768.
// This is placeholder logic for Phase 4 certificate verification.
// In production, this would verify the Dilithium signature (see mldsa.go).
func VerifyCertificateWithKEM(cert *x509.Certificate, caPub *KEMPublicKey) error {
	// TODO(Phase 4): Implement ML-DSA (Dilithium) signature verification.
	// For now, we rely on standard x509 verification (ECDSA signatures).
	// ML-DSA signatures would be stored in a certificate extension and
	// verified here alongside the standard signature.
	_ = caPub // Placeholder to avoid unused variable error
	return nil
}
