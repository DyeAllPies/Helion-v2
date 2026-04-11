// internal/auth/internal_branch_test.go
//
// Package-internal tests covering branches in jwt.go that require crafting
// tokens with missing fields or alternate signing methods — constructions
// that can't be built from the external _test package because they rely on
// tm.secret and the unexported extractJTIUnchecked helper.

package auth

import (
	"context"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type fakeTokenStore struct {
	data map[string][]byte
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{data: make(map[string][]byte)}
}

func (s *fakeTokenStore) Get(_ context.Context, key string) ([]byte, error) {
	if v, ok := s.data[key]; ok {
		return v, nil
	}
	return nil, jwt.ErrTokenMalformed // any non-nil error
}

func (s *fakeTokenStore) Put(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *fakeTokenStore) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}

// ── ValidateToken — unexpected signing method ────────────────────────────────

// TestValidateToken_RS256Token_RejectsUnexpectedSigningMethod hits the
// `unexpected signing method` branch in the keyfunc passed to ParseWithClaims.
// We forge a token signed with RS256 and expect the validator to refuse it.
func TestValidateToken_RS256Token_RejectsUnexpectedSigningMethod(t *testing.T) {
	ctx := context.Background()
	tm, err := NewTokenManager(ctx, newFakeTokenStore())
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}

	// Generate an RSA key and sign a token with it.
	rsaKey, err := rsa.GenerateKey(testRandReader{}, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	claims := &Claims{
		Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "attacker",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(rsaKey)
	if err != nil {
		t.Fatalf("sign RS256: %v", err)
	}

	if _, err := tm.ValidateToken(ctx, signed); err == nil {
		t.Error("ValidateToken should reject RS256 token (unexpected signing method)")
	}
}

// ── extractJTIUnchecked — error branches ─────────────────────────────────────

func TestExtractJTIUnchecked_GarbageToken_ReturnsError(t *testing.T) {
	_, err := extractJTIUnchecked("not.a.jwt")
	if err == nil {
		t.Error("extractJTIUnchecked: want error for garbage input")
	}
}

func TestExtractJTIUnchecked_EmptyJTI_ReturnsError(t *testing.T) {
	// Sign a token with empty JTI using any 32-byte key — extractJTIUnchecked
	// uses ParseUnverified, so signature validation is skipped.
	claims := &Claims{
		Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "x",
			ID:        "", // deliberately empty
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte("any-secret-of-sufficient-length-00000000"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = extractJTIUnchecked(signed)
	if err == nil || !strings.Contains(err.Error(), "no JTI") {
		t.Errorf("want 'no JTI' error, got %v", err)
	}
}

// ── ExtractJTIFromValidatedToken — validated token with empty JTI ────────────

// TestExtractJTIFromValidatedToken_ValidButEmptyJTI covers the
// "validated token has no JTI" branch. We sign a token directly with tm.secret
// (empty JTI, no GenerateToken call) and pre-store the corresponding JTI key
// so ValidateToken's revocation check passes.
func TestExtractJTIFromValidatedToken_ValidButEmptyJTI(t *testing.T) {
	ctx := context.Background()
	store := newFakeTokenStore()
	tm, err := NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}

	claims := &Claims{
		Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "x",
			ID:        "", // deliberately empty
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(tm.secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Insert the (empty) JTI record so ValidateToken's revocation lookup passes.
	if err := store.Put(ctx, JTIPrefix, []byte{1}, time.Minute); err != nil {
		t.Fatalf("seed JTI: %v", err)
	}

	_, err = ExtractJTIFromValidatedToken(ctx, signed, tm)
	if err == nil || !strings.Contains(err.Error(), "no JTI") {
		t.Errorf("want 'no JTI' error, got %v", err)
	}
}

// ── testRandReader ───────────────────────────────────────────────────────────

// testRandReader is a minimal io.Reader backed by a pseudo-random sequence.
// Using crypto/rand.Reader here is also fine; this keeps the test entirely
// deterministic and fast (rsa.GenerateKey 2048 can be slow on CI).
type testRandReader struct{}

func (testRandReader) Read(p []byte) (int, error) {
	// Simple LCG; quality doesn't matter for a test RSA key.
	var state uint64 = 0xdeadbeefcafebabe
	for i := range p {
		state = state*6364136223846793005 + 1442695040888963407
		p[i] = byte(state >> 56)
	}
	return len(p), nil
}
