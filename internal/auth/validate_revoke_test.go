// internal/auth/validate_revoke_test.go
//
// Tests for ValidateToken, RevokeToken, and ExtractJTIFromValidatedToken.

package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// ── ValidateToken ─────────────────────────────────────────────────────────────

func TestValidateToken_ValidToken_ReturnsClaims(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, _ := tm.GenerateToken(ctx, "alice", "admin", time.Minute)

	claims, err := tm.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Subject != "alice" {
		t.Errorf("want subject alice, got %q", claims.Subject)
	}
	if claims.Role != "admin" {
		t.Errorf("want role admin, got %q", claims.Role)
	}
}

func TestValidateToken_Expired_Rejected(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, _ := tm.GenerateToken(ctx, "user", "node", -time.Second)

	_, err := tm.ValidateToken(ctx, tok)
	if err == nil {
		t.Error("expected error for expired token, got nil")
	}
}

func TestValidateToken_InvalidSignature_Rejected(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, _ := tm.GenerateToken(ctx, "user", "admin", time.Minute)

	parts := strings.SplitN(tok, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3-part JWT, got %d parts", len(parts))
	}
	tampered := parts[0] + "." + parts[1] + ".invalidsignatureXXXXXXXXXXXX"

	_, err := tm.ValidateToken(ctx, tampered)
	if err == nil {
		t.Error("expected error for tampered token")
	}
}

func TestValidateToken_RevokedJTI_Rejected(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)

	tok, _ := tm.GenerateToken(ctx, "user", "admin", time.Minute)
	jti, _ := auth.ExtractJTIFromValidatedToken(ctx, tok, tm)

	if err := tm.RevokeToken(ctx, jti); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	_, err := tm.ValidateToken(ctx, tok)
	if err == nil {
		t.Error("expected error for revoked token, got nil")
	}
}

func TestValidateToken_GarbageString_Rejected(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	_, err := tm.ValidateToken(ctx, "not.a.jwt")
	if err == nil {
		t.Error("expected error for garbage token")
	}
}

func TestValidateToken_TamperedSignature_Rejected(t *testing.T) {
	store := newMockStore()
	tm, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}

	tok, err := tm.GenerateToken(ctx, "u", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token format")
	}
	tampered := parts[0] + "." + parts[1] + ".invalidsignature"

	_, err = tm.ValidateToken(ctx, tampered)
	if err == nil {
		t.Error("expected error for tampered signature, got nil")
	}
}

// ── RevokeToken ───────────────────────────────────────────────────────────────

func TestRevokeToken_DeletesJTI_FromStore(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)

	tok, _ := tm.GenerateToken(ctx, "user", "admin", time.Minute)
	jti, _ := auth.ExtractJTIFromValidatedToken(ctx, tok, tm)
	jtiKey := auth.JTIPrefix + jti

	if _, ok := store.data[jtiKey]; !ok {
		t.Fatal("JTI not found in store before revocation")
	}

	if err := tm.RevokeToken(ctx, jti); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	if _, ok := store.data[jtiKey]; ok {
		t.Error("JTI should be deleted after revocation")
	}
}

func TestRevokeToken_OneToken_DoesNotAffectOthers(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())

	tok1, _ := tm.GenerateToken(ctx, "user", "admin", time.Minute)
	tok2, _ := tm.GenerateToken(ctx, "user", "admin", time.Minute)

	jti1, _ := auth.ExtractJTIFromValidatedToken(ctx, tok1, tm)
	if err := tm.RevokeToken(ctx, jti1); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	if _, err := tm.ValidateToken(ctx, tok2); err != nil {
		t.Errorf("tok2 should still be valid after revoking tok1: %v", err)
	}
}

func TestRevokeToken_DeleteFails_ReturnsError(t *testing.T) {
	base := newMockStore()
	store := &failOnDeleteStore{inner: base, deleteErr: errors.New("delete failed")}
	tm, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	if err := tm.RevokeToken(ctx, "any-jti"); err == nil {
		t.Error("expected error when Delete fails, got nil")
	}
}

// ── ExtractJTIFromValidatedToken ──────────────────────────────────────────────

func TestExtractJTI_ReturnsJTI(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, _ := tm.GenerateToken(ctx, "u", "admin", time.Minute)

	jti, err := auth.ExtractJTIFromValidatedToken(ctx, tok, tm)
	if err != nil {
		t.Fatalf("ExtractJTIFromValidatedToken: %v", err)
	}
	if jti == "" {
		t.Error("expected non-empty JTI")
	}
}

func TestExtractJTI_InvalidToken_ReturnsError(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	_, err := auth.ExtractJTIFromValidatedToken(ctx, "garbage", tm)
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

// TestExtractJTI_ForgedToken_DoesNotBypassValidation verifies that
// ExtractJTIFromValidatedToken rejects forged tokens. Previously the package
// exposed a ParseUnverified-based variant (ExtractJTI); it was renamed to
// the unexported extractJTIUnchecked (AUDIT C3 fix). The exported API now
// validates the signature before returning the JTI.
func TestExtractJTI_ForgedToken_DoesNotBypassValidation(t *testing.T) {
	store := newMockStore()
	tm, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}

	real, err := tm.GenerateToken(ctx, "user", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	parts := strings.Split(real, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT structure")
	}
	forged := parts[0] + "." + parts[1] + ".forgedsignatureXXXXXXXXXXXXXX"

	_, err = auth.ExtractJTIFromValidatedToken(ctx, forged, tm)
	if err == nil {
		t.Error("ExtractJTIFromValidatedToken accepted forged token — signature check is broken")
	}
}

func TestExtractJTI_EmptyJTI_ReturnsError(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	_, err := auth.ExtractJTIFromValidatedToken(ctx, "", tm)
	if err == nil {
		t.Error("expected error for empty token string, got nil")
	}
}
