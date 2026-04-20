// internal/pqcrypto/revocation.go
//
// Feature 31 — operator certificate revocation.
//
// Role
// ────
// Feature 27 ships TTL-based operator certs (default 90 days).
// Without revocation, a leaked PKCS#12 file is usable until
// expiry — untenable when an operator leaves mid-quarter, a
// laptop is stolen, or a cert is issued with a wrong CN.
//
// This file adds a persisted revoked-serial set. The admin
// endpoint `POST /admin/operator-certs/{serial}/revoke` inserts
// into it; `clientCertMiddleware` consults `IsRevoked(serial)`
// on every request that presents a client cert and rejects the
// match as if the cert were never presented.
//
// Safety properties
// ─────────────────
//
//  1. **Append-only revocations.** Once a record is in the
//     store it stays — an "unrevoke" action is a NEW issuance,
//     not a deletion. The store exposes no Delete primitive;
//     accidental writes that remove a revocation require direct
//     Badger surgery and leave audit evidence.
//
//  2. **O(1) hot-path lookup.** `IsRevoked` is called on every
//     authenticated request in `on`/`warn` tiers. The in-
//     memory cache guarantees RWMutex-protected map access
//     with no Badger read on the request path. The cache is
//     loaded at construction and updated on every Revoke.
//
//  3. **Persistent truth.** Every Revoke writes through the
//     Badger store first; the in-memory cache is only
//     consulted for reads. A coordinator restart rebuilds the
//     cache from Badger — no revocation is lost to RAM-only
//     state.
//
//  4. **Idempotent revoke.** Revoking the same serial twice is
//     a no-op on the second call — returns the existing
//     record. Handlers treat this as 200 OK + the prior
//     record, so a panicked operator hitting the button five
//     times doesn't produce five audit lines.
//
//  5. **Defensive serial normalisation.** Serial hex strings
//     are trimmed + lowercased before both storage and
//     lookup. A caller passing `"0xABCD"` and `"abcd"`
//     addresses the same record.

package pqcrypto

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// ── Errors ─────────────────────────────────────────────────

// ErrInvalidSerial is returned when a serial hex string is
// empty or contains non-hex characters after normalisation.
var ErrInvalidSerial = errors.New("pqcrypto: invalid serial hex")

// ErrRevocationNotFound is returned by Get / CRL when the
// serial does not appear in the revocation store.
var ErrRevocationNotFound = errors.New("pqcrypto: revocation not found")

// ── Record ─────────────────────────────────────────────────

// RevocationRecord is the on-disk form of a single revoked
// operator cert.
type RevocationRecord struct {
	// SerialHex is the lowercase-normalised hex encoding of
	// the certificate serial number. Matches the form emitted
	// by the `operator_cert_issued` audit event.
	SerialHex string `json:"serial_hex"`

	// CommonName captures the CN of the cert AT THE TIME of
	// revocation. Recording it here spares an operator from
	// cross-referencing issuance audit records to answer
	// "whose cert did we revoke?"
	CommonName string `json:"common_name"`

	// RevokedAt is the timestamp the revocation was recorded.
	// Appears on the CRL as `RevocationTime`.
	RevokedAt time.Time `json:"revoked_at"`

	// RevokedBy is the Principal ID of the admin that revoked
	// the cert. Audit-only; not consulted for any decision.
	RevokedBy string `json:"revoked_by"`

	// Reason is the free-form justification the admin supplied.
	// Trimmed + length-capped at write time so a runaway
	// paste doesn't bloat Badger.
	Reason string `json:"reason,omitempty"`
}

// ── Store interface ────────────────────────────────────────

// RevocationStore persists the revoked-serial set and exposes
// an O(1) IsRevoked query for the TLS verification hot path.
type RevocationStore interface {
	// Revoke inserts or retrieves a revocation record for
	// the given serial. Idempotent: if a prior record exists,
	// it is returned with isNew=false and nothing is
	// mutated. Otherwise the new record is persisted with
	// isNew=true.
	Revoke(ctx context.Context, rec RevocationRecord) (stored *RevocationRecord, isNew bool, err error)

	// IsRevoked returns true iff the serial is in the
	// revocation set. O(1). Hot path — must not touch Badger.
	IsRevoked(serialHex string) bool

	// Get returns the full revocation record or
	// ErrRevocationNotFound.
	Get(ctx context.Context, serialHex string) (*RevocationRecord, error)

	// List returns every revocation record, ordered by
	// RevokedAt descending (newest first). Used by the CRL
	// export endpoint.
	List(ctx context.Context) ([]RevocationRecord, error)
}

// ── Badger-backed persister ────────────────────────────────

// BadgerPersistence is the narrow interface the revocation
// store needs from the coordinator's Badger handle. Mirrors
// the audit-store pattern: takes the shared DB and runs its
// own key-prefix subsystem. Reads go through the in-memory
// cache populated at NewBadgerRevocationStore time; no Get
// primitive is needed.
type BadgerPersistence interface {
	Put(ctx context.Context, key string, value []byte) error
	Scan(ctx context.Context, prefix string, limit int) ([][]byte, error)
}

// BadgerRevocationStore implements RevocationStore against a
// BadgerPersistence. Thread-safe via an internal RWMutex; the
// in-memory cache mirrors the on-disk set and is loaded at
// construction.
type BadgerRevocationStore struct {
	db BadgerPersistence

	mu     sync.RWMutex
	cache  map[string]RevocationRecord // serialHex → record
}

// NewBadgerRevocationStore loads the persisted revocation set
// into an in-memory cache and returns a ready-to-use store.
// Returns an error if the initial Scan fails — a coordinator
// that can't enumerate its revoked certs must not start.
func NewBadgerRevocationStore(ctx context.Context, db BadgerPersistence) (*BadgerRevocationStore, error) {
	s := &BadgerRevocationStore{
		db:    db,
		cache: make(map[string]RevocationRecord),
	}
	raw, err := db.Scan(ctx, revocationKeyPrefix, 0)
	if err != nil {
		return nil, fmt.Errorf("BadgerRevocationStore: initial scan: %w", err)
	}
	for _, body := range raw {
		var rec RevocationRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			// Corrupt entries are skipped rather than
			// blocking the coordinator — a single malformed
			// record must not prevent other revocations from
			// being enforced.
			continue
		}
		if rec.SerialHex == "" {
			continue
		}
		s.cache[rec.SerialHex] = rec
	}
	return s, nil
}

const (
	// revocationKeyPrefix is the Badger key prefix under
	// which revocation records live.
	revocationKeyPrefix = "crypto/revoked/"

	// maxReasonBytes caps the free-form reason string. Long
	// enough for every reasonable operator justification; short
	// enough that a runaway paste cannot bloat a single audit
	// line.
	maxReasonBytes = 512
)

func revocationKey(serialHex string) string {
	return revocationKeyPrefix + serialHex
}

// Revoke persists a record. Idempotent — the second call with
// the same serial returns the first record.
func (s *BadgerRevocationStore) Revoke(ctx context.Context, rec RevocationRecord) (*RevocationRecord, bool, error) {
	norm, err := NormalizeSerialHex(rec.SerialHex)
	if err != nil {
		return nil, false, err
	}
	rec.SerialHex = norm
	rec.Reason = trimReason(rec.Reason)
	if rec.RevokedAt.IsZero() {
		rec.RevokedAt = time.Now().UTC()
	}

	// Idempotent check under a read lock first (the hot path
	// if a panicked operator double-clicks revoke).
	s.mu.RLock()
	existing, ok := s.cache[norm]
	s.mu.RUnlock()
	if ok {
		// Return a defensive copy so the caller can't mutate
		// the cache by reference.
		copy := existing
		return &copy, false, nil
	}

	// Persist to Badger first so a crash between "wrote the
	// cache" and "wrote the disk" can never leave the
	// coordinator claiming a cert is revoked when a
	// restart-recovery wouldn't.
	body, err := json.Marshal(&rec)
	if err != nil {
		return nil, false, fmt.Errorf("Revoke: marshal: %w", err)
	}
	if err := s.db.Put(ctx, revocationKey(norm), body); err != nil {
		return nil, false, fmt.Errorf("Revoke: persist: %w", err)
	}

	s.mu.Lock()
	// Re-check under the write lock to handle the race where
	// two concurrent Revoke calls raced through the RLock.
	if prior, ok := s.cache[norm]; ok {
		s.mu.Unlock()
		copy := prior
		return &copy, false, nil
	}
	s.cache[norm] = rec
	s.mu.Unlock()

	copy := rec
	return &copy, true, nil
}

// IsRevoked is the hot-path query. O(1) map lookup under a
// RWMutex read lock.
func (s *BadgerRevocationStore) IsRevoked(serialHex string) bool {
	norm, err := NormalizeSerialHex(serialHex)
	if err != nil {
		return false
	}
	s.mu.RLock()
	_, ok := s.cache[norm]
	s.mu.RUnlock()
	return ok
}

// Get returns the full record or ErrRevocationNotFound.
func (s *BadgerRevocationStore) Get(_ context.Context, serialHex string) (*RevocationRecord, error) {
	norm, err := NormalizeSerialHex(serialHex)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	rec, ok := s.cache[norm]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrRevocationNotFound
	}
	copy := rec
	return &copy, nil
}

// List returns every record, newest first.
func (s *BadgerRevocationStore) List(_ context.Context) ([]RevocationRecord, error) {
	s.mu.RLock()
	out := make([]RevocationRecord, 0, len(s.cache))
	for _, rec := range s.cache {
		out = append(out, rec)
	}
	s.mu.RUnlock()
	// Newest-first ordering.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].RevokedAt.After(out[j-1].RevokedAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// ── Helpers ────────────────────────────────────────────────

// NormalizeSerialHex trims, lowercases, and strips an
// optional `0x` prefix from a serial hex string. Returns
// ErrInvalidSerial on empty, non-hex, or obviously malformed
// input. Exported so HTTP handlers can normalise path values
// with the same rules before storing / looking up.
func NormalizeSerialHex(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidSerial)
	}
	// Length cap — serial numbers can be up to 20 octets
	// (40 hex chars) per RFC 5280. We allow up to 64 hex
	// chars to accommodate the coordinator's time.UnixNano
	// serials (which fit comfortably under that).
	if len(s) > 64 {
		return "", fmt.Errorf("%w: serial too long (%d chars)", ErrInvalidSerial, len(s))
	}
	// Lowercase + hex-digit validation.
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			out[i] = c
		case c >= 'a' && c <= 'f':
			out[i] = c
		case c >= 'A' && c <= 'F':
			out[i] = c + ('a' - 'A')
		default:
			return "", fmt.Errorf("%w: non-hex char %q at %d", ErrInvalidSerial, c, i)
		}
	}
	return string(out), nil
}

// SerialHexFromBigInt converts a certificate SerialNumber
// (big.Int) to the normalised hex form used throughout the
// revocation system. Guaranteed round-trip with
// NormalizeSerialHex.
func SerialHexFromBigInt(sn *big.Int) string {
	if sn == nil {
		return ""
	}
	return strings.ToLower(sn.Text(16))
}

func trimReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxReasonBytes {
		s = s[:maxReasonBytes]
	}
	return s
}

// ── CRL export ─────────────────────────────────────────────

// CreateCRLPEM emits a PEM-encoded X.509 Certificate
// Revocation List signed by the CA's private key. Each record
// in `recs` becomes a `pkix.RevokedCertificate` entry.
//
// `nextUpdate` is the advertised validity window for the CRL;
// callers typically pass `time.Now().Add(24*time.Hour)` —
// consumers (Nginx via `ssl_crl`, direct verifiers) reload
// the CRL when stale.
//
// Returns the raw PEM bytes ready to hand to the HTTP
// response body.
func (c *CA) CreateCRLPEM(recs []RevocationRecord, nextUpdate time.Time) ([]byte, error) {
	if c == nil || c.Cert == nil || c.key == nil {
		return nil, fmt.Errorf("CreateCRLPEM: CA not fully initialised")
	}
	now := time.Now().UTC()
	if nextUpdate.Before(now) {
		return nil, fmt.Errorf("CreateCRLPEM: nextUpdate %s is in the past", nextUpdate)
	}

	revoked := make([]pkix.RevokedCertificate, 0, len(recs))
	for _, rec := range recs {
		sn, ok := new(big.Int).SetString(rec.SerialHex, 16)
		if !ok {
			return nil, fmt.Errorf("CreateCRLPEM: bad serial %q", rec.SerialHex)
		}
		revoked = append(revoked, pkix.RevokedCertificate{
			SerialNumber:   sn,
			RevocationTime: rec.RevokedAt,
		})
	}

	// CRL serial number: monotonic via UnixNano. Readers
	// use this to detect updates; our exporter is stateless
	// (recomputes on every call), so any fresh fetch comes
	// with a fresh serial.
	template := &x509.RevocationList{
		RevokedCertificateEntries: toRevokedCertificateEntries(revoked),
		Number:     big.NewInt(now.UnixNano()),
		ThisUpdate: now,
		NextUpdate: nextUpdate,
	}

	der, err := x509.CreateRevocationList(rand.Reader, template, c.Cert, c.key)
	if err != nil {
		return nil, fmt.Errorf("CreateCRLPEM: sign: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), nil
}

// toRevokedCertificateEntries converts the deprecated-but-
// still-common `pkix.RevokedCertificate` shape to the newer
// `x509.RevocationListEntry` shape that
// `x509.CreateRevocationList` prefers. Go 1.21+ reads both;
// we populate the new field explicitly so future stdlib
// changes don't surprise us.
func toRevokedCertificateEntries(in []pkix.RevokedCertificate) []x509.RevocationListEntry {
	out := make([]x509.RevocationListEntry, len(in))
	for i, entry := range in {
		out[i] = x509.RevocationListEntry{
			SerialNumber:   entry.SerialNumber,
			RevocationTime: entry.RevocationTime,
		}
	}
	return out
}

// Avoid an ecdsa import-only compile break while we split the
// file with auxiliary functions; the CA.key reference in
// CreateCRLPEM keeps the dependency real.
var _ ecdsa.PrivateKey
