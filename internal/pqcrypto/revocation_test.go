// internal/pqcrypto/revocation_test.go
//
// Feature 31 — unit tests for the revocation store +
// CreateCRLPEM. BadgerPersistence is faked with a minimal
// in-memory map to keep the tests fast and deterministic;
// the Badger-backed integration is exercised end-to-end by
// the handler tests in internal/api.

package pqcrypto_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// ── memPersistence ────────────────────────────────────────

// memPersistence is a tight in-memory fake for the Badger
// interface the revocation store needs. Threadsafe so the
// concurrent-Revoke test can exercise the dual-checked
// locking in the real store.
type memPersistence struct {
	mu   sync.Mutex
	data map[string][]byte
	err  error // if non-nil, every op returns this
}

func newMemPersistence() *memPersistence {
	return &memPersistence{data: make(map[string][]byte)}
}

func (m *memPersistence) Put(_ context.Context, key string, value []byte) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *memPersistence) Scan(_ context.Context, prefix string, _ int) ([][]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out [][]byte
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, append([]byte(nil), v...))
		}
	}
	return out, nil
}


// ── Round trip ────────────────────────────────────────────

func TestRevocationStore_RevokeThenIsRevoked(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, err := pqcrypto.NewBadgerRevocationStore(ctx, db)
	if err != nil {
		t.Fatalf("NewBadgerRevocationStore: %v", err)
	}

	rec := pqcrypto.RevocationRecord{
		SerialHex:  "ABCDEF",
		CommonName: "alice",
		RevokedBy:  "user:root",
		Reason:     "alice left",
	}
	stored, isNew, err := s.Revoke(ctx, rec)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !isNew {
		t.Errorf("first Revoke: want isNew=true")
	}
	// Serial is normalised (lowercased).
	if stored.SerialHex != "abcdef" {
		t.Errorf("serial not normalised: %q", stored.SerialHex)
	}
	if stored.RevokedAt.IsZero() {
		t.Errorf("RevokedAt unset")
	}

	// Hot-path lookup with mixed-case input: still matches.
	if !s.IsRevoked("AbCdEf") {
		t.Errorf("IsRevoked should match despite case")
	}
	if !s.IsRevoked("0xabcdef") {
		t.Errorf("IsRevoked should match despite 0x prefix")
	}
	// Non-revoked serial: false.
	if s.IsRevoked("deadbeef") {
		t.Errorf("IsRevoked false-positive for unrevoked serial")
	}
}

func TestRevocationStore_RevokeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, _ := pqcrypto.NewBadgerRevocationStore(ctx, db)

	first, isNewA, _ := s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex:  "CAFE",
		CommonName: "alice",
		RevokedBy:  "user:root",
	})
	if !isNewA {
		t.Fatalf("first: isNew=false")
	}
	// Second attempt with different reason text MUST NOT
	// overwrite — the first record wins.
	second, isNewB, _ := s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex:  "cafe",
		CommonName: "alice",
		RevokedBy:  "user:other",
		Reason:     "I was not supposed to see this update",
	})
	if isNewB {
		t.Fatalf("second: want isNew=false")
	}
	if second.RevokedBy != first.RevokedBy {
		t.Errorf("idempotent revoke overwrote RevokedBy: %q", second.RevokedBy)
	}
	if second.Reason != first.Reason {
		t.Errorf("idempotent revoke overwrote Reason: %q", second.Reason)
	}
}

// ── Persistence survives restart ──────────────────────────

func TestRevocationStore_ReloadsFromDisk(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, _ := pqcrypto.NewBadgerRevocationStore(ctx, db)
	_, _, err := s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex:  "abcd",
		CommonName: "alice",
		RevokedBy:  "user:root",
	})
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// "Restart": construct a fresh store backed by the same
	// persistence. Cache is rebuilt from Badger.
	s2, err := pqcrypto.NewBadgerRevocationStore(ctx, db)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !s2.IsRevoked("abcd") {
		t.Errorf("revocation not persisted across restart")
	}
}

// ── Validation guards ────────────────────────────────────

func TestRevocationStore_RejectsBadSerial(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, _ := pqcrypto.NewBadgerRevocationStore(ctx, db)

	cases := []string{
		"",           // empty
		"  ",         // whitespace-only
		"nothex!",    // non-hex
		"0xZZ",       // bad char after prefix
		strings.Repeat("a", 65), // over length cap
	}
	for _, c := range cases {
		_, _, err := s.Revoke(ctx, pqcrypto.RevocationRecord{SerialHex: c})
		if !errors.Is(err, pqcrypto.ErrInvalidSerial) {
			t.Errorf("serial %q: want ErrInvalidSerial, got %v", c, err)
		}
	}
}

func TestRevocationStore_Get_Missing(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, _ := pqcrypto.NewBadgerRevocationStore(ctx, db)
	_, err := s.Get(ctx, "abcd1234")
	if !errors.Is(err, pqcrypto.ErrRevocationNotFound) {
		t.Fatalf("Get missing: want ErrRevocationNotFound, got %v", err)
	}
}

// ── List ordering ────────────────────────────────────────

func TestRevocationStore_List_NewestFirst(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, _ := pqcrypto.NewBadgerRevocationStore(ctx, db)

	now := time.Now().UTC()
	_, _, _ = s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex: "0001", RevokedAt: now.Add(-3 * time.Hour),
	})
	_, _, _ = s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex: "0002", RevokedAt: now.Add(-1 * time.Hour),
	})
	_, _, _ = s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex: "0003", RevokedAt: now.Add(-2 * time.Hour),
	})

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].SerialHex != "0002" || list[1].SerialHex != "0003" || list[2].SerialHex != "0001" {
		t.Errorf("ordering wrong: %+v", list)
	}
}

// ── NormalizeSerialHex ──────────────────────────────────

func TestNormalizeSerialHex(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"AbCd", "abcd", false},
		{"0xABCD", "abcd", false},
		{"0XaBcD", "abcd", false},
		{"  deadbeef\n", "deadbeef", false},
		{"", "", true},
		{"not-hex", "", true},
		{"0x", "", true}, // empty after prefix
		{strings.Repeat("a", 65), "", true},
	}
	for _, c := range cases {
		got, err := pqcrypto.NormalizeSerialHex(c.in)
		if c.err {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

// ── Reason trimming ─────────────────────────────────────

func TestRevocationStore_TrimsReason(t *testing.T) {
	ctx := context.Background()
	db := newMemPersistence()
	s, _ := pqcrypto.NewBadgerRevocationStore(ctx, db)

	huge := strings.Repeat("x", 2048)
	rec, _, _ := s.Revoke(ctx, pqcrypto.RevocationRecord{
		SerialHex: "feed",
		Reason:    "  " + huge + "  ",
	})
	if len(rec.Reason) != 512 {
		t.Errorf("reason not capped at 512: %d", len(rec.Reason))
	}
}

// ── CRL export ──────────────────────────────────────────

func TestCreateCRLPEM_VerifiesAgainstCA(t *testing.T) {
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	recs := []pqcrypto.RevocationRecord{
		{SerialHex: "abcd", CommonName: "alice", RevokedAt: time.Now().UTC()},
		{SerialHex: "beef", CommonName: "bob", RevokedAt: time.Now().UTC()},
	}
	crlPEM, err := ca.CreateCRLPEM(recs, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("CreateCRLPEM: %v", err)
	}

	block, _ := pem.Decode(crlPEM)
	if block == nil {
		t.Fatalf("no PEM block")
	}
	if block.Type != "X509 CRL" {
		t.Errorf("PEM block type: %q", block.Type)
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatalf("ParseRevocationList: %v", err)
	}
	if err := crl.CheckSignatureFrom(ca.Cert); err != nil {
		t.Fatalf("CRL signature does not verify against CA: %v", err)
	}

	// Both serials must be present.
	seen := map[string]bool{}
	entries := crl.RevokedCertificateEntries
	if len(entries) == 0 {
		// Older Go emits via RevokedCertificates; check that
		// path too so the test works across versions.
		for _, e := range crl.RevokedCertificates { //nolint:staticcheck
			seen[e.SerialNumber.Text(16)] = true
		}
	} else {
		for _, e := range entries {
			seen[e.SerialNumber.Text(16)] = true
		}
	}
	if !seen["abcd"] || !seen["beef"] {
		t.Errorf("expected both serials in CRL, got %v", seen)
	}
}

func TestCreateCRLPEM_RejectsBackwardsNextUpdate(t *testing.T) {
	ca, _ := pqcrypto.NewCA()
	_, err := ca.CreateCRLPEM(nil, time.Now().Add(-time.Hour))
	if err == nil {
		t.Fatal("want error for nextUpdate in the past")
	}
}

func TestCreateCRLPEM_EmptyList_StillValid(t *testing.T) {
	// An empty CRL (no revocations yet) is still a valid
	// signed artefact — consumers fetching the endpoint
	// before any revocation has happened must not error out.
	ca, _ := pqcrypto.NewCA()
	crlPEM, err := ca.CreateCRLPEM(nil, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateCRLPEM empty: %v", err)
	}
	block, _ := pem.Decode(crlPEM)
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatalf("ParseRevocationList empty: %v", err)
	}
	if err := crl.CheckSignatureFrom(ca.Cert); err != nil {
		t.Fatalf("empty CRL signature: %v", err)
	}
}

// ── SerialHexFromBigInt ─────────────────────────────────

func TestSerialHexFromBigInt_RoundTrip(t *testing.T) {
	sn, _ := new(big.Int).SetString("abcd", 16)
	got := pqcrypto.SerialHexFromBigInt(sn)
	if got != "abcd" {
		t.Errorf("hex: %q", got)
	}
	norm, _ := pqcrypto.NormalizeSerialHex(got)
	if norm != "abcd" {
		t.Errorf("round-trip: %q", norm)
	}
}
