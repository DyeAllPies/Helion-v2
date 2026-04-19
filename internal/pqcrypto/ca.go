// internal/pqcrypto/ca.go
package pqcrypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// envDuration reads an integer env var (in the given unit) and returns the
// corresponding duration. Logs a WARN and uses def if the value is missing or
// unparseable.
func envDuration(key string, def int, unit time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(def) * unit
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		slog.Warn("invalid cert duration env var, using default",
			slog.String("key", key), slog.String("value", v), slog.Int("default", def))
		return time.Duration(def) * unit
	}
	return time.Duration(n) * unit
}

// CA holds the root certificate and private key for the cluster.
// Phase 4 enhancement: Added ML-DSA signing keys and hybrid KEM config.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *ecdsa.PrivateKey

	// Phase 4: Post-quantum cryptography fields
	mldsaPub  *MLDSAPublicKey  // ML-DSA-65 public key for signature verification
	mldsaPriv *MLDSAPrivateKey // ML-DSA-65 private key for signing certificates
	hybridCfg *HybridConfig    // Hybrid KEM configuration (X25519+ML-KEM-768)

	// mldsaMu guards mldsaSigs against concurrent reads and writes.
	mldsaMu   sync.RWMutex
	mldsaSigs map[string][]byte // Pre-computed ML-DSA signatures (serial -> sig)
}

// NewCA generates a self-signed ECDSA P-256 root CA.
// PQC (ML-DSA/Dilithium) signing is added in Phase 4.
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Helion Cluster"},
			CommonName:   "Helion Root CA",
		},
		DNSNames:              []string{"helion-ca"},
		NotBefore:             time.Now(),
		// AUDIT L1 (fixed): CA TTL was previously 3650 days (10 years). Reduced
		// to 730 days (2 years) and made configurable via HELION_CA_CERT_TTL_DAYS.
		NotAfter:              time.Now().Add(envDuration("HELION_CA_CERT_TTL_DAYS", 730, 24*time.Hour)),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	return &CA{Cert: cert, CertPEM: certPEM, key: key}, nil
}

// NewCAFromPEM reconstructs a read-only CA from a PEM-encoded certificate.
// The returned CA has no private key and cannot sign new certificates, but it
// can be used as a trust anchor (RootCAs) for TLS verification. This is how
// node agents running in separate containers verify the coordinator's cert.
func NewCAFromPEM(certPEM []byte) (*CA, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("failed to decode CA PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}
	return &CA{Cert: cert, CertPEM: certPEM}, nil
}

// IssueNodeCert signs a new ECDSA P-256 certificate for a node.
// The nodeID is used as both the CommonName and a DNS SAN.
// 127.0.0.1 and ::1 are added as IP SANs for local development.
// Additional DNS SANs can be injected via HELION_EXTRA_SANS (comma-separated).
func (ca *CA) IssueNodeCert(nodeID string) (certPEM, keyPEM []byte, err error) {
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"Helion Cluster"},
			CommonName:   nodeID,
		},
		DNSNames: appendExtraSANs([]string{nodeID, "localhost"}),
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
		NotBefore: time.Now(),
		// AUDIT L2 (fixed): node cert TTL is configurable via HELION_NODE_CERT_TTL_HOURS
		// (default 24 h). Short-lived node certs limit the blast radius of a
		// compromised node key by requiring regular re-issuance.
		NotAfter:  time.Now().Add(envDuration("HELION_NODE_CERT_TTL_HOURS", 24, time.Hour)),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &nodeKey.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(nodeKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// TLSConfig returns a tls.Config for the coordinator (server side).
// It requires client certificates signed by this CA.
func (ca *CA) TLSConfig(certPEM, keyPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		// RequireAnyClientCert demands a client certificate but does NOT verify
		// it against the CA at the TLS layer.  This allows nodes to connect with
		// a self-signed bootstrap cert for initial registration.  Application-level
		// verification (cert pinning, ML-DSA signature check) happens in the
		// Register RPC handler after the handshake completes.
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// IssueOperatorCert signs a new ECDSA P-256 client certificate for a
// dashboard operator (feature 27 — browser mTLS).
//
// Separate from IssueNodeCert because:
//
//   - ExtKeyUsage is ClientAuth ONLY (no ServerAuth). An operator
//     cert must never accidentally be usable to stand up a fake
//     server. IssueNodeCert carries both because node gRPC is mTLS
//     and nodes can act as clients OR servers; operators can only
//     ever be clients of the coordinator.
//   - No DNS/IP SANs. An operator cert is not bound to a host; the
//     Subject CN is the only operator identity the coordinator
//     reads. Adding DNS SANs would suggest a host binding that
//     doesn't exist.
//   - TTL is longer (default 90 days) because operators can't
//     trivially re-issue the way a node can on every restart.
//     Configurable via HELION_OPERATOR_CERT_TTL_DAYS.
//
// cn is the operator's identity string — used verbatim as the cert
// Subject CN and later as the `operator_cn` audit detail. Must be
// non-empty and NUL-free; caller is expected to pass a stable
// operator identifier (e.g. "alice@ops").
//
// Returns PEM-encoded cert + PEM-encoded EC private key. The CLI
// at cmd/helion-issue-op-cert/ bundles these into a PKCS#12 file
// for browser import.
func (ca *CA) IssueOperatorCert(cn string, ttl time.Duration) (certPEM, keyPEM []byte, err error) {
	if ca.key == nil {
		return nil, nil, errors.New("IssueOperatorCert: CA is read-only (no private key)")
	}
	if cn == "" {
		return nil, nil, errors.New("IssueOperatorCert: CommonName is required")
	}
	if strings.ContainsRune(cn, '\x00') {
		return nil, nil, errors.New("IssueOperatorCert: CommonName must not contain NUL")
	}
	if ttl <= 0 {
		ttl = envDuration("HELION_OPERATOR_CERT_TTL_DAYS", 90, 24*time.Hour)
	}
	// Sanity cap on TTL: refuse certs that outlive the CA itself.
	// Operators rotate; forever-valid certs are an unreviewable
	// access grant.
	if caRemaining := time.Until(ca.Cert.NotAfter); ttl > caRemaining {
		ttl = caRemaining
	}
	if ttl <= 0 {
		return nil, nil, errors.New("IssueOperatorCert: CA cert is expired; re-issue CA before minting operator certs")
	}

	opKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"Helion Cluster"},
			OrganizationalUnit: []string{"Operators"},
			CommonName:   cn,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &opKey.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("create operator cert: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(opKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// ClientCertPool returns an x509.CertPool containing the CA cert,
// suitable for use as tls.Config.ClientCAs when the coordinator
// wants to verify operator client certificates on the REST listener
// (feature 27). Node certs and operator certs share the same CA;
// the distinction is made by ExtKeyUsage and the `operator_cn`
// extraction in clientCertMiddleware.
//
// Returns a fresh pool on every call so a caller mutating the pool
// (e.g. adding additional operator CAs for federation) cannot
// poison the CA instance.
func (ca *CA) ClientCertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	return pool
}

// NodeTLSConfig returns a tls.Config for a node agent (client side).
// It presents its own certificate and verifies the coordinator against the CA.
func (ca *CA) NodeTLSConfig(certPEM, keyPEM []byte, serverName string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// appendExtraSANs appends any DNS names from the HELION_EXTRA_SANS env var
// (comma-separated) to the base list.  This allows Docker Compose service
// names like "coordinator" to be included in the certificate without
// hardcoding them.
func appendExtraSANs(base []string) []string {
	v := os.Getenv("HELION_EXTRA_SANS")
	if v == "" {
		return base
	}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			base = append(base, s)
		}
	}
	return base
}
