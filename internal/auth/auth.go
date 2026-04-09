// internal/auth/auth.go
package auth

import (
	"crypto/tls"
	"fmt"

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
func NewNodeBundle(ca *pqcrypto.CA, nodeID string) (*Bundle, error) {
	certPEM, keyPEM, err := ca.IssueNodeCert(nodeID)
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
