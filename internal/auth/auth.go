// internal/auth/auth.go
package auth

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
	"google.golang.org/grpc/credentials"
)

// Bundle holds everything a coordinator or node needs to establish mTLS.
type Bundle struct {
	CA      *pqcrypto.CA
	CertPEM []byte
	KeyPEM  []byte
}

// NewCoordinatorBundle creates a CA and issues a coordinator certificate.
func NewCoordinatorBundle() (*Bundle, error) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		return nil, fmt.Errorf("create CA: %w", err)
	}

	certPEM, keyPEM, err := ca.IssueNodeCert("helion-coordinator")
	if err != nil {
		return nil, fmt.Errorf("issue coordinator cert: %w", err)
	}

	return &Bundle{CA: ca, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// NewNodeBundle issues a node certificate from an existing CA.
// If the CA has ML-DSA enabled, IssueNodeCertWithMLDSA is used so that the
// coordinator can verify the out-of-band ML-DSA signature at Register time.
func NewNodeBundle(ca *pqcrypto.CA, nodeID string) (*Bundle, error) {
	var certPEM, keyPEM []byte
	var err error
	if ca.GetMLDSAPublicKey() != nil {
		certPEM, keyPEM, err = ca.IssueNodeCertWithMLDSA(nodeID)
	} else {
		certPEM, keyPEM, err = ca.IssueNodeCert(nodeID)
	}
	if err != nil {
		return nil, fmt.Errorf("issue node cert for %s: %w", nodeID, err)
	}
	return &Bundle{CA: ca, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// ServerCredentials returns gRPC transport credentials for the coordinator.
func (b *Bundle) ServerCredentials() (credentials.TransportCredentials, error) {
	cfg, err := b.CA.TLSConfig(b.CertPEM, b.KeyPEM)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// ClientCredentials returns gRPC transport credentials for a node agent.
func (b *Bundle) ClientCredentials(serverName string) (credentials.TransportCredentials, error) {
	cfg, err := b.CA.NodeTLSConfig(b.CertPEM, b.KeyPEM, serverName)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// RawTLSConfig returns the underlying *tls.Config for non-gRPC use.
func (b *Bundle) RawTLSConfig(serverName string) (*tls.Config, error) {
	return b.CA.NodeTLSConfig(b.CertPEM, b.KeyPEM, serverName)
}

// WriteCAFile writes the CA certificate PEM to path (mode 0644) so that
// node agents running in separate containers can import it.
func WriteCAFile(caPEM []byte, path string) error {
	if path == "" {
		return nil // no-op when path is empty
	}
	if dir := filepath.Dir(path); dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("WriteCAFile: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, caPEM, 0o644); err != nil {
		return fmt.Errorf("WriteCAFile: write %s: %w", path, err)
	}
	return nil
}

// NewNodeBundleFromCAFile creates a self-signed bootstrap bundle whose trust
// pool includes the coordinator's CA certificate read from caPath. This allows
// a node agent in a separate container to verify the coordinator's TLS cert
// while presenting its own self-signed client cert for the initial handshake.
func NewNodeBundleFromCAFile(caPath string) (*Bundle, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	// Create the node's own self-signed CA + cert (for client auth)
	bundle, err := NewCoordinatorBundle()
	if err != nil {
		return nil, fmt.Errorf("create bootstrap bundle: %w", err)
	}

	// Replace the trust anchor: the node must trust the coordinator's CA,
	// not its own self-signed CA, for server certificate verification.
	coordCA, err := pqcrypto.NewCAFromPEM(caPEM)
	if err != nil {
		return nil, fmt.Errorf("parse coordinator CA: %w", err)
	}
	bundle.CA = coordCA

	return bundle, nil
}
