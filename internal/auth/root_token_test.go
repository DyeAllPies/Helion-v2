// internal/auth/root_token_test.go
//
// Tests for RotateRootToken, GetRootToken, and WriteRootToken.

package auth_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// ── Root token ────────────────────────────────────────────────────────────────

func TestRotateRootToken_ProducesToken(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, err := tm.RotateRootToken(ctx)
	if err != nil {
		t.Fatalf("RotateRootToken: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty root token")
	}
}

func TestRotateRootToken_AlwaysGeneratesNewToken(t *testing.T) {
	// Each call to RotateRootToken must produce a different token so that a
	// leaked token from a previous run is invalid after the next startup.
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok1, _ := tm.RotateRootToken(ctx)
	tok2, _ := tm.RotateRootToken(ctx)

	if tok1 == tok2 {
		t.Error("RotateRootToken should produce a different token on each call")
	}
}

func TestRotateRootToken_RevokesOldToken(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok1, _ := tm.RotateRootToken(ctx)

	_, _ = tm.RotateRootToken(ctx)

	_, err := tm.ValidateToken(ctx, tok1)
	if err == nil {
		t.Error("old root token should be invalid after rotation")
	}
}

func TestRotateRootToken_PersistedToStore(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)
	tok, _ := tm.RotateRootToken(ctx)

	stored, err := store.Get(ctx, auth.RootTokenKey)
	if err != nil {
		t.Fatalf("root token not in store: %v", err)
	}
	if string(stored) != tok {
		t.Error("stored root token does not match returned token")
	}
}

// TestRotateRootToken_StoreFails_ReturnsError covers the "store root token"
// error path in RotateRootToken.
// Call sequence (no pre-existing token):
//  1. NewTokenManager → Get(JWTSecretKey) fails → Put(JWTSecretKey)  [Put #1]
//  2. RotateRootToken → Get(RootTokenKey) fails → GenerateToken → Put(JTI)  [Put #2]
//  3. RotateRootToken → Put(RootTokenKey) fails  [Put #3 = failOn]
func TestRotateRootToken_StoreFails_ReturnsError(t *testing.T) {
	store := &failOnNthPutStore{inner: newMockStore(), failOn: 3}
	tm, err := auth.NewTokenManager(ctx, store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	_, err = tm.RotateRootToken(ctx)
	if err == nil {
		t.Error("expected error when storing root token fails, got nil")
	}
	if !strings.Contains(err.Error(), "store root token") {
		t.Errorf("want 'store root token' in error, got: %v", err)
	}
}

func TestGetRootToken_ReturnsStoredToken(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)

	generated, _ := tm.RotateRootToken(ctx)
	retrieved, err := tm.GetRootToken(ctx)
	if err != nil {
		t.Fatalf("GetRootToken: %v", err)
	}
	if retrieved != generated {
		t.Errorf("GetRootToken mismatch: want %q, got %q", generated, retrieved)
	}
}

func TestGetRootToken_NoToken_ReturnsError(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	_, err := tm.GetRootToken(ctx)
	if err == nil {
		t.Error("GetRootToken should return error when no root token exists")
	}
}

// ── WriteRootToken ────────────────────────────────────────────────────────────

func TestWriteRootToken_WritesWithSecurePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "root-token")

	token := "tok-" + strings.Repeat("x", 200)
	if err := auth.WriteRootToken(token, path); err != nil {
		t.Fatalf("WriteRootToken: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != token {
		t.Errorf("token mismatch: got %q, want %q", got, token)
	}

	// Permission check is meaningful only on Unix-like systems — Windows
	// reports 0666 for files regardless of the mode passed to WriteFile.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("file mode: got %o, want 0600", mode)
		}
	}
}

func TestWriteRootToken_EmptyPath_ReturnsError(t *testing.T) {
	if err := auth.WriteRootToken("tok", ""); err == nil {
		t.Error("expected error for empty path, got nil")
	}
}

func TestWriteRootToken_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c", "root-token")

	if err := auth.WriteRootToken("tok", nested); err != nil {
		t.Fatalf("WriteRootToken with nested path: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

// TestWriteRootToken_ParentIsFile_ReturnsError covers the MkdirAll error
// branch: if the parent-of-parent path is an existing regular file, MkdirAll
// returns a "not a directory" error.
func TestWriteRootToken_ParentIsFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Create a plain file, then ask WriteRootToken to use a path underneath
	// it — MkdirAll should fail because a file cannot be a parent directory.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup blocker file: %v", err)
	}
	bad := filepath.Join(blocker, "child", "root-token")
	if err := auth.WriteRootToken("tok", bad); err == nil {
		t.Error("expected error when parent path is a regular file")
	}
}
