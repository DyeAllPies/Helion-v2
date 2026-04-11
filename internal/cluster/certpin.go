// internal/cluster/certpin.go
//
// Certificate pinning: bind a node ID to the SHA-256 fingerprint of its
// TLS certificate. Prevents a scenario where a new cert is issued for the
// same node ID (e.g. CA compromise) and used without going through the full
// revoke→re-register cycle.
//
// How it works
// ────────────
//   1. On first Register: compute SHA-256(DER cert), store as the pin.
//   2. On subsequent Register: compare new SHA-256 against stored pin.
//      If they differ, reject with codes.PermissionDenied.
//   3. On RevokeNode: the pin is cleared, allowing a fresh registration with
//      a new cert (the caller is expected to use a new node ID in practice,
//      but clearing the pin is defensive).
//
// Pin storage
// ───────────
// CertPinner is an interface so that tests can inject an in-memory store and
// production can inject a BadgerDB-backed store without coupling cluster to
// the persistence package.

package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// ErrCertFingerprintMismatch is returned by Register when the presented
// certificate's fingerprint differs from the stored pin for the node.
var ErrCertFingerprintMismatch = errors.New("cert pinning: fingerprint mismatch — re-register with a new node ID")

// CertPinner stores and retrieves the pinned certificate fingerprint for a
// node.  Returning an error from GetPin signals "no pin stored" (not a hard
// failure); only ErrCertFingerprintMismatch is a security rejection.
type CertPinner interface {
	// GetPin returns the hex-encoded SHA-256 fingerprint previously pinned for
	// nodeID, or a non-nil error if no pin is stored.
	GetPin(ctx context.Context, nodeID string) (string, error)

	// SetPin stores the hex-encoded SHA-256 fingerprint for nodeID.
	SetPin(ctx context.Context, nodeID, fingerprint string) error

	// DeletePin removes the pin for nodeID (called on revocation).
	DeletePin(ctx context.Context, nodeID string) error
}

// ── CertFingerprint ───────────────────────────────────────────────────────────

// CertFingerprint computes the hex-encoded SHA-256 fingerprint of DER cert bytes.
func CertFingerprint(derBytes []byte) string {
	sum := sha256.Sum256(derBytes)
	return hex.EncodeToString(sum[:])
}

// ── CertVerifier ─────────────────────────────────────────────────────────────

// CertVerifier verifies an out-of-band ML-DSA signature on a node certificate.
// Implemented by *pqcrypto.CA; injected via Registry.SetCertVerifier so that
// the cluster package stays free of a pqcrypto import.
type CertVerifier interface {
	// VerifyNodeCertMLDSA verifies the stored ML-DSA signature for the given
	// DER cert bytes.  Returns nil if ML-DSA is not enabled or sig is valid.
	VerifyNodeCertMLDSA(derBytes []byte) error
}

// ── MemCertPinner ─────────────────────────────────────────────────────────────

// MemCertPinner is an in-memory CertPinner for testing and development.
// All methods are safe for concurrent use.
type MemCertPinner struct {
	mu   sync.RWMutex
	pins map[string]string // nodeID → hex fingerprint
}

// NewMemCertPinner creates an in-memory CertPinner.
func NewMemCertPinner() *MemCertPinner {
	return &MemCertPinner{pins: make(map[string]string)}
}

func (m *MemCertPinner) GetPin(_ context.Context, nodeID string) (string, error) {
	m.mu.RLock()
	fp, ok := m.pins[nodeID]
	m.mu.RUnlock()
	if !ok {
		return "", errors.New("certpin: no pin for node " + nodeID)
	}
	return fp, nil
}

func (m *MemCertPinner) SetPin(_ context.Context, nodeID, fingerprint string) error {
	m.mu.Lock()
	m.pins[nodeID] = fingerprint
	m.mu.Unlock()
	return nil
}

func (m *MemCertPinner) DeletePin(_ context.Context, nodeID string) error {
	m.mu.Lock()
	delete(m.pins, nodeID)
	m.mu.Unlock()
	return nil
}
