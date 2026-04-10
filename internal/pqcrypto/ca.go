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
	"math/big"
	"net"
	"time"
)

// CA holds the root certificate and private key for the cluster.
// Phase 4 enhancement: Added ML-DSA signing keys and hybrid KEM config.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *ecdsa.PrivateKey
	
	// Phase 4: Post-quantum cryptography fields
	mldsaPub   *MLDSAPublicKey    // ML-DSA-65 public key for signature verification
	mldsaPriv  *MLDSAPrivateKey   // ML-DSA-65 private key for signing certificates
	mldsaSigs  map[string][]byte  // Pre-computed ML-DSA signatures (serial -> sig)
	hybridCfg  *HybridConfig      // Hybrid KEM configuration (X25519+ML-KEM-768)
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
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
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

// IssueNodeCert signs a new ECDSA P-256 certificate for a node.
// The nodeID is used as both the CommonName and a DNS SAN.
// 127.0.0.1 and ::1 are added as IP SANs for local development.
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
		DNSNames: []string{nodeID, "localhost"},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(24 * time.Hour),
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
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
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
