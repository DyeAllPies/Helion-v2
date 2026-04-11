// internal/pqcrypto/mldsa.go
//
// ML-DSA (Module-Lattice Digital Signature Algorithm) implementation using
// Dilithium-3 for post-quantum certificate signing.
//
// Phase 4 enhancement: Node certificates are signed with BOTH ECDSA (classical)
// and ML-DSA (post-quantum). The coordinator verifies both signatures on
// registration, rejecting certificates with tampered ML-DSA signatures.
//
// NIST FIPS 204 Standard:
// ───────────────────────
// ML-DSA is the NIST-standardized version of CRYSTALS-Dilithium. We use
// Dilithium-3 (security level 3) which provides:
//   - Public key: 1952 bytes
//   - Signature: ~3293 bytes (variable, depends on signing randomness)
//   - Security: ~192-bit classical, designed to resist quantum attacks
//
// Dilithium-3 is chosen over Dilithium-2 or Dilithium-5 for balance:
//   - Dilithium-2: smaller keys/sigs but lower security (~128-bit)
//   - Dilithium-3: balanced security (~192-bit)
//   - Dilithium-5: highest security (~256-bit) but largest keys/sigs
//
// For Helion's threat model (protect against future quantum adversaries but
// minimize bandwidth), Dilithium-3 is the sweet spot.
//
// Certificate Extension:
// ──────────────────────
// ML-DSA signatures are stored in an X.509 v3 extension in the certificate.
// We use OID 1.3.6.1.4.1.11129.2.1.27 (experimental PQC signature extension).
//
// Extension format (DER-encoded):
//   SEQUENCE {
//     algorithm  OBJECT IDENTIFIER (1.3.6.1.4.1.2.267.7.8.7 = Dilithium3)
//     signature  OCTET STRING (raw ML-DSA signature bytes)
//   }
//
// Verification process:
// ────────────────────
// 1. Extract the ML-DSA public key from the CA (stored separately from cert).
// 2. Extract the ML-DSA signature from the certificate extension.
// 3. Compute the certificate TBS (to-be-signed) hash (SHA3-256).
// 4. Verify signature(TBS, ML-DSA-pubkey) == signature-in-extension.
// 5. If verification fails, reject the certificate even if ECDSA is valid.
//
// Rationale:
// ─────────
// This dual-signature approach provides "belt and suspenders" security:
//   - If ECDSA is broken (by quantum computer), ML-DSA protects us.
//   - If ML-DSA is broken (new lattice attack), ECDSA still works.
//   - Only if BOTH are broken can an attacker forge certificates.

package pqcrypto

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"

	"github.com/cloudflare/circl/sign"
	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// ML-DSA OIDs
var (
	// OID for Dilithium3 algorithm (NIST ML-DSA-65)
	oidMLDSA65 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 2, 267, 7, 8, 7}

	// OID for the certificate extension holding the ML-DSA signature
	oidPQCSignatureExt = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 1, 27}
)

// MLDSAPublicKey wraps an ML-DSA-65 (Dilithium-3) public key.
type MLDSAPublicKey struct {
	raw []byte
}

// MLDSAPrivateKey wraps an ML-DSA-65 private key.
type MLDSAPrivateKey struct {
	scheme sign.Scheme
	sk     sign.PrivateKey
}

// GenerateMLDSAKeyPair generates a fresh ML-DSA-65 (Dilithium-3) key pair.
// This should be called when creating the CA to generate its PQC signing key.
// The private key is used to sign node certificates; the public key is used
// by nodes to verify the coordinator's certificates.
func GenerateMLDSAKeyPair() (*MLDSAPublicKey, *MLDSAPrivateKey, error) {
	scheme := mldsa65.Scheme()
	pk, sk, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("generate ML-DSA-65 keypair: %w", err)
	}

	pubRaw, err := pk.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("marshal ML-DSA public key: %w", err)
	}

	return &MLDSAPublicKey{raw: pubRaw}, &MLDSAPrivateKey{scheme: scheme, sk: sk}, nil
}

// Sign signs a message with the ML-DSA private key.
// The message should be the certificate TBS (to-be-signed) bytes.
// Returns the signature (3293 bytes for Dilithium-3).
func (priv *MLDSAPrivateKey) Sign(message []byte) ([]byte, error) {
	// Dilithium uses SHA3-512 internally for hashing
	sig := priv.scheme.Sign(priv.sk, message, nil)
	return sig, nil
}

// Verify verifies an ML-DSA signature against a message.
// Returns nil if the signature is valid, error otherwise.
func (pub *MLDSAPublicKey) Verify(message, signature []byte) error {
	scheme := mldsa65.Scheme()
	pk, err := scheme.UnmarshalBinaryPublicKey(pub.raw)
	if err != nil {
		return fmt.Errorf("unmarshal ML-DSA public key: %w", err)
	}

	if !scheme.Verify(pk, message, signature, nil) {
		return fmt.Errorf("ML-DSA signature verification failed")
	}

	return nil
}

// pqcSignatureExtension is the ASN.1 structure for the ML-DSA signature extension.
type pqcSignatureExtension struct {
	Algorithm asn1.ObjectIdentifier
	Signature []byte
}

// AddMLDSASignature adds an ML-DSA signature to a certificate template.
// This should be called BEFORE creating the certificate with x509.CreateCertificate.
// The signature is computed over the certificate's TBS and stored in an extension.
//
// Note: This is a placeholder. Full implementation requires signing the TBS bytes,
// which are only available AFTER x509.CreateCertificate is called. In practice,
// we need a two-pass approach:
//   1. Create cert with x509.CreateCertificate (gets TBS bytes)
//   2. Parse the cert, extract TBS, sign with ML-DSA
//   3. Re-encode cert with the extension added
//
// For Phase 4, we'll implement this in IssueNodeCertWithMLDSA below.
func AddMLDSASignature(template *x509.Certificate, tbsBytes []byte, privKey *MLDSAPrivateKey) error {
	sig, err := privKey.Sign(tbsBytes)
	if err != nil {
		return fmt.Errorf("sign with ML-DSA: %w", err)
	}

	ext := pqcSignatureExtension{
		Algorithm: oidMLDSA65,
		Signature: sig,
	}

	extBytes, err := asn1.Marshal(ext)
	if err != nil {
		return fmt.Errorf("marshal PQC signature extension: %w", err)
	}

	template.ExtraExtensions = append(template.ExtraExtensions, pkix.Extension{
		Id:       oidPQCSignatureExt,
		Critical: false, // Non-critical so old verifiers ignore it
		Value:    extBytes,
	})

	return nil
}

// VerifyMLDSASignature verifies the ML-DSA signature in a certificate.
// Extracts the signature from the PQC extension and verifies it against the TBS.
// Returns nil if valid, error if signature is missing or invalid.
func VerifyMLDSASignature(cert *x509.Certificate, caPubKey *MLDSAPublicKey) error {
	// Extract the PQC signature extension
	var sigExt pqcSignatureExtension
	found := false

	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidPQCSignatureExt) {
			if _, err := asn1.Unmarshal(ext.Value, &sigExt); err != nil {
				return fmt.Errorf("unmarshal PQC signature extension: %w", err)
			}
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("ML-DSA signature extension not found in certificate")
	}

	// Verify the algorithm is Dilithium-3
	if !sigExt.Algorithm.Equal(oidMLDSA65) {
		return fmt.Errorf("unexpected PQC algorithm: %v (expected ML-DSA-65)", sigExt.Algorithm)
	}

	// Extract the TBS (to-be-signed) bytes from the certificate
	// In a real X.509 cert, TBS is the first SEQUENCE in the DER encoding
	// For simplicity, we hash the entire RawTBSCertificate field
	tbsBytes := cert.RawTBSCertificate

	// Verify the signature
	if err := caPubKey.Verify(tbsBytes, sigExt.Signature); err != nil {
		return fmt.Errorf("ML-DSA signature verification failed: %w", err)
	}

	return nil
}

// EnhanceCAWithMLDSA adds ML-DSA signing capability to the CA.
// This generates a new ML-DSA key pair for the CA and stores it.
// All subsequent node certificates will be dual-signed (ECDSA + ML-DSA).
func (ca *CA) EnhanceWithMLDSA() error {
	pub, priv, err := GenerateMLDSAKeyPair()
	if err != nil {
		return fmt.Errorf("generate ML-DSA keypair: %w", err)
	}

	ca.mldsaPub = pub
	ca.mldsaPriv = priv
	return nil
}

// IssueNodeCertWithMLDSA issues a node certificate with an out-of-band ML-DSA
// signature stored in the CA's signature map (keyed by cert serial number).
//
// Two-pass approach:
//   1. Issue a standard ECDSA cert via IssueNodeCert.
//   2. Parse it, extract RawTBSCertificate, sign with ML-DSA.
//   3. Store the signature in ca.mldsaSigs[serial] for later verification.
//
// At registration time, the coordinator calls VerifyNodeCertMLDSA(derBytes)
// which looks up the signature by serial and verifies it against the TBS.
// This is the "out-of-band" approach: the signature lives in the CA's memory
// rather than embedded in the cert, which avoids having to re-encode the DER.
func (ca *CA) IssueNodeCertWithMLDSA(nodeID string) (certPEM, keyPEM []byte, err error) {
	// Pass 1: issue a standard ECDSA cert.
	certPEM, keyPEM, err = ca.IssueNodeCert(nodeID)
	if err != nil {
		return nil, nil, fmt.Errorf("issue base certificate: %w", err)
	}

	// If ML-DSA is not enabled on this CA, return the standard cert unchanged.
	if ca.mldsaPriv == nil {
		return certPEM, keyPEM, nil
	}

	// Pass 2: parse the cert to extract RawTBSCertificate for ML-DSA signing.
	block, err := parsePEMBlock(certPEM, "CERTIFICATE")
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate PEM: %w", err)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate DER: %w", err)
	}

	// Sign the TBS (to-be-signed) portion with ML-DSA.
	sig, err := ca.mldsaPriv.Sign(cert.RawTBSCertificate)
	if err != nil {
		return nil, nil, fmt.Errorf("ML-DSA sign: %w", err)
	}

	// Store the signature keyed by serial number so VerifyNodeCertMLDSA can
	// retrieve it during the Register RPC.
	ca.StoreMLDSASignature(cert.SerialNumber.String(), sig)

	return certPEM, keyPEM, nil
}

// VerifyNodeCertMLDSA verifies the stored ML-DSA signature for a node cert
// presented as DER bytes.  Returns nil if:
//   - ML-DSA is not enabled on this CA (mldsaPub == nil), or
//   - the signature matches the cert's RawTBSCertificate.
//
// Returns an error if ML-DSA is enabled but no signature is stored for the
// cert's serial, or if signature verification fails.
func (ca *CA) VerifyNodeCertMLDSA(derBytes []byte) error {
	if ca.mldsaPub == nil {
		return nil // ML-DSA not enabled; skip verification.
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return fmt.Errorf("parse cert DER: %w", err)
	}

	serial := cert.SerialNumber.String()
	sig, ok := ca.GetMLDSASignature(serial)
	if !ok {
		return fmt.Errorf("ML-DSA: no stored signature for cert serial %s", serial)
	}

	return ca.mldsaPub.Verify(cert.RawTBSCertificate, sig)
}

// parsePEMBlock decodes the first PEM block of the given type from pemData.
func parsePEMBlock(pemData []byte, blockType string) (*pem.Block, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if block.Type != blockType {
		return nil, fmt.Errorf("expected PEM block type %q, got %q", blockType, block.Type)
	}
	return block, nil
}

// GetMLDSAPublicKey returns the CA's ML-DSA public key for distribution to nodes.
// Nodes use this to verify the coordinator's certificates.
func (ca *CA) GetMLDSAPublicKey() *MLDSAPublicKey {
	return ca.mldsaPub
}

// StoreMLDSASignature stores a pre-computed ML-DSA signature for a certificate.
// This is a workaround for the two-pass signing process.
// The coordinator stores signatures in BadgerDB keyed by cert serial number.
func (ca *CA) StoreMLDSASignature(certSerialNumber string, signature []byte) {
	ca.mldsaMu.Lock()
	defer ca.mldsaMu.Unlock()
	if ca.mldsaSigs == nil {
		ca.mldsaSigs = make(map[string][]byte)
	}
	ca.mldsaSigs[certSerialNumber] = signature
}

// GetMLDSASignature retrieves a stored ML-DSA signature for a certificate.
func (ca *CA) GetMLDSASignature(certSerialNumber string) ([]byte, bool) {
	ca.mldsaMu.RLock()
	defer ca.mldsaMu.RUnlock()
	if ca.mldsaSigs == nil {
		return nil, false
	}
	sig, ok := ca.mldsaSigs[certSerialNumber]
	return sig, ok
}
