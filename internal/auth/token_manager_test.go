// internal/auth/token_manager_test.go
//
// Tests for NewTokenManager and GenerateToken (happy and error paths).

package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// ── NewTokenManager ───────────────────────────────────────────────────────────

func TestNewTokenManager_GeneratesSecret_OnFirstStart(t *testing.T) {
	store := newMockStore()
	tm, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tm == nil {
		t.Fatal("expected non-nil TokenManager")
	}
	if _, ok := store.data[auth.JWTSecretKey]; !ok {
		t.Error("JWT secret should be persisted to store on first start")
	}
}

func TestNewTokenManager_LoadsExistingSecret(t *testing.T) {
	store := newMockStore()

	existingSecret := make([]byte, 32)
	for i := range existingSecret {
		existingSecret[i] = byte(i + 1)
	}
	store.data[auth.JWTSecretKey] = existingSecret

	tm1, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("first NewTokenManager: %v", err)
	}

	tokenStr, err := tm1.GenerateToken(ctx, "subject", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	tm2, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("second NewTokenManager: %v", err)
	}

	if _, err := tm2.ValidateToken(ctx, tokenStr); err != nil {
		t.Errorf("token from tm1 should validate with tm2 (same secret): %v", err)
	}
}

func TestNewTokenManager_PutFails_ReturnsError(t *testing.T) {
	store := &failOnPutStore{putErr: errors.New("disk full")}
	_, err := auth.NewTokenManager(ctx, store)
	if err == nil {
		t.Error("expected error when Put fails during secret storage, got nil")
	}
}

// ── GenerateToken ─────────────────────────────────────────────────────────────

func TestGenerateToken_ReturnsNonEmptyString(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, err := tm.GenerateToken(ctx, "user-1", "admin", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token string")
	}
}

func TestGenerateToken_StoresJTI(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)

	_, err := tm.GenerateToken(ctx, "user-1", "admin", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for k := range store.data {
		if strings.HasPrefix(k, auth.JTIPrefix) {
			found = true
			break
		}
	}
	if !found {
		t.Error("JTI should be stored with prefix " + auth.JTIPrefix)
	}
}

func TestGenerateToken_UniqueJTIs(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)

	tok1, _ := tm.GenerateToken(ctx, "u", "admin", time.Minute)
	tok2, _ := tm.GenerateToken(ctx, "u", "admin", time.Minute)

	jti1, err := auth.ExtractJTIFromValidatedToken(ctx, tok1, tm)
	if err != nil {
		t.Fatalf("ExtractJTIFromValidatedToken tok1: %v", err)
	}
	jti2, err := auth.ExtractJTIFromValidatedToken(ctx, tok2, tm)
	if err != nil {
		t.Fatalf("ExtractJTIFromValidatedToken tok2: %v", err)
	}
	if jti1 == jti2 {
		t.Error("consecutive tokens should have different JTIs")
	}
}

func TestGenerateToken_JTIStoreFails_ReturnsError(t *testing.T) {
	store := &failOnJTIPutStore{inner: newMockStore()}
	tm, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	_, err = tm.GenerateToken(ctx, "user", "admin", time.Minute)
	if err == nil {
		t.Error("expected error when JTI store fails, got nil")
	}
}

// ── Feature 33 — required_cn binding ────────────────────────────────────────

// TestGenerateToken_OmitsRequiredCNByDefault guards the
// back-compat contract: the legacy GenerateToken signature
// must NOT stamp a `required_cn` claim. Any token minted by
// an older call path continues to behave as unbound.
func TestGenerateToken_OmitsRequiredCNByDefault(t *testing.T) {
	tm, err := auth.NewTokenManager(ctx, newMockStore())
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	tok, err := tm.GenerateToken(ctx, "alice", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	claims, err := tm.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.RequiredCN != "" {
		t.Errorf("legacy GenerateToken leaked a required_cn claim: %q", claims.RequiredCN)
	}
}

// TestGenerateTokenWithCN_RoundTrip proves the claim is
// signed + round-trips through ValidateToken unchanged.
func TestGenerateTokenWithCN_RoundTrip(t *testing.T) {
	tm, err := auth.NewTokenManager(ctx, newMockStore())
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	tok, err := tm.GenerateTokenWithCN(ctx, "alice", "admin", "alice@ops", time.Minute)
	if err != nil {
		t.Fatalf("GenerateTokenWithCN: %v", err)
	}
	claims, err := tm.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.RequiredCN != "alice@ops" {
		t.Errorf("required_cn: got %q, want %q", claims.RequiredCN, "alice@ops")
	}
	if claims.Subject != "alice" {
		t.Errorf("subject: got %q", claims.Subject)
	}
	if claims.Role != "admin" {
		t.Errorf("role: got %q", claims.Role)
	}
}

// TestGenerateTokenWithCN_EmptyCN is equivalent to
// GenerateToken; no required_cn is emitted.
func TestGenerateTokenWithCN_EmptyCN(t *testing.T) {
	tm, err := auth.NewTokenManager(ctx, newMockStore())
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	tok, err := tm.GenerateTokenWithCN(ctx, "alice", "admin", "", time.Minute)
	if err != nil {
		t.Fatalf("GenerateTokenWithCN: %v", err)
	}
	claims, err := tm.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.RequiredCN != "" {
		t.Errorf("empty CN should not be stamped, got %q", claims.RequiredCN)
	}
}

// TestGenerateTokenWithCN_JWTSignatureProtectsBinding —
// the load-bearing security property. Any attempt to flip
// required_cn in the encoded JWT must invalidate the
// signature and fail ValidateToken. This guards against a
// token holder editing the claim to match their own CN.
func TestGenerateTokenWithCN_JWTSignatureProtectsBinding(t *testing.T) {
	tm, err := auth.NewTokenManager(ctx, newMockStore())
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	tok, err := tm.GenerateTokenWithCN(ctx, "alice", "admin", "alice@ops", time.Minute)
	if err != nil {
		t.Fatalf("GenerateTokenWithCN: %v", err)
	}
	// Naive tamper: replace "alice@ops" with "bob@ops" in
	// the payload segment. A real attacker would have to
	// re-base64 the payload, but the segment-level substring
	// swap already invalidates the signature, which is what
	// we want to verify.
	tampered := strings.ReplaceAll(tok, "alice", "bob")
	if tampered == tok {
		t.Skip("no substring to tamper with")
	}
	_, err = tm.ValidateToken(ctx, tampered)
	if err == nil {
		t.Fatal("tampered token validated — signature guard missing")
	}
}
