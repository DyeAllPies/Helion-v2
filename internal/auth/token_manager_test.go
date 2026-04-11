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
