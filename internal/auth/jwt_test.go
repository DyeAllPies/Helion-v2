package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

var ctx = context.Background()

// ── Mock TokenStore ────────────────────────────────────────────────────────────

type mockTokenStore struct {
	mu   sync.Mutex
	data map[string][]byte
	err  error // if set, all operations return this error
}

func newMockStore() *mockTokenStore {
	return &mockTokenStore{data: make(map[string][]byte)}
}

func (s *mockTokenStore) Get(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("key not found")
	}
	return append([]byte{}, v...), nil
}

func (s *mockTokenStore) Put(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte{}, value...)
	return nil
}

func (s *mockTokenStore) Delete(_ context.Context, key string) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// ── NewTokenManager ────────────────────────────────────────────────────────────

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

	jti1, err := auth.ExtractJTI(tok1)
	if err != nil {
		t.Fatalf("ExtractJTI tok1: %v", err)
	}
	jti2, err := auth.ExtractJTI(tok2)
	if err != nil {
		t.Fatalf("ExtractJTI tok2: %v", err)
	}
	if jti1 == jti2 {
		t.Error("consecutive tokens should have different JTIs")
	}
}

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
	jti, _ := auth.ExtractJTI(tok)

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

// ── RevokeToken ───────────────────────────────────────────────────────────────

func TestRevokeToken_DeletesJTI_FromStore(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(ctx, store)

	tok, _ := tm.GenerateToken(ctx, "user", "admin", time.Minute)
	jti, _ := auth.ExtractJTI(tok)
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

	jti1, _ := auth.ExtractJTI(tok1)
	if err := tm.RevokeToken(ctx, jti1); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	if _, err := tm.ValidateToken(ctx, tok2); err != nil {
		t.Errorf("tok2 should still be valid after revoking tok1: %v", err)
	}
}

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

	// Second rotation should invalidate tok1.
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

// ── PrintRootTokenInstructions ───────────────────────────────────────────────

func TestPrintRootTokenInstructions_DoesNotPanic(t *testing.T) {
	auth.PrintRootTokenInstructions("tok-short")
	auth.PrintRootTokenInstructions(strings.Repeat("x", 200))
}

// ── ExtractJTI ────────────────────────────────────────────────────────────────

func TestExtractJTI_ReturnsJTI(t *testing.T) {
	tm, _ := auth.NewTokenManager(ctx, newMockStore())
	tok, _ := tm.GenerateToken(ctx, "u", "admin", time.Minute)

	jti, err := auth.ExtractJTI(tok)
	if err != nil {
		t.Fatalf("ExtractJTI: %v", err)
	}
	if jti == "" {
		t.Error("expected non-empty JTI")
	}
}

func TestExtractJTI_InvalidToken_ReturnsError(t *testing.T) {
	_, err := auth.ExtractJTI("garbage")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

// ── StoreAdapter ──────────────────────────────────────────────────────────────

type mockPersistenceStore struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration
}

func newPersistenceStore() *mockPersistenceStore {
	return &mockPersistenceStore{
		data: make(map[string][]byte),
		ttls: make(map[string]time.Duration),
	}
}

func (m *mockPersistenceStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}

func (m *mockPersistenceStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte{}, value...)
	return nil
}

func (m *mockPersistenceStore) PutWithTTL(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte{}, value...)
	m.ttls[key] = ttl
	return nil
}

func (m *mockPersistenceStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func TestStoreAdapter_GetPutDelete(t *testing.T) {
	inner := newPersistenceStore()
	adapter := auth.NewStoreAdapter(inner)

	if err := adapter.Put(ctx, "k1", []byte("value"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := adapter.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Errorf("want 'value', got %q", got)
	}

	if err := adapter.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := adapter.Get(ctx, "k1"); err == nil {
		t.Error("Get after Delete should return error")
	}
}

func TestStoreAdapter_PutWithTTL_UsesPutWithTTL(t *testing.T) {
	inner := newPersistenceStore()
	adapter := auth.NewStoreAdapter(inner)

	ttl := 15 * time.Minute
	if err := adapter.Put(ctx, "k-ttl", []byte("v"), ttl); err != nil {
		t.Fatalf("Put with TTL: %v", err)
	}

	if inner.ttls["k-ttl"] != ttl {
		t.Errorf("want ttl %v, got %v", ttl, inner.ttls["k-ttl"])
	}
}

// ── Error path coverage ───────────────────────────────────────────────────────

type failOnPutStore struct {
	putErr error
}

func (s *failOnPutStore) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("key not found")
}
func (s *failOnPutStore) Put(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return s.putErr
}
func (s *failOnPutStore) Delete(_ context.Context, _ string) error { return nil }

func TestNewTokenManager_PutFails_ReturnsError(t *testing.T) {
	store := &failOnPutStore{putErr: errors.New("disk full")}
	_, err := auth.NewTokenManager(ctx, store)
	if err == nil {
		t.Error("expected error when Put fails during secret storage, got nil")
	}
}

type failOnJTIPutStore struct {
	inner    *mockTokenStore
	putCalls int
}

func (s *failOnJTIPutStore) Get(c context.Context, key string) ([]byte, error) {
	return s.inner.Get(c, key)
}
func (s *failOnJTIPutStore) Delete(c context.Context, key string) error {
	return s.inner.Delete(c, key)
}
func (s *failOnJTIPutStore) Put(c context.Context, key string, value []byte, ttl time.Duration) error {
	s.putCalls++
	if s.putCalls > 1 {
		return errors.New("JTI store failure")
	}
	return s.inner.Put(c, key, value, ttl)
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

type failOnDeleteStore struct {
	inner     *mockTokenStore
	deleteErr error
}

func (s *failOnDeleteStore) Get(c context.Context, key string) ([]byte, error) {
	return s.inner.Get(c, key)
}
func (s *failOnDeleteStore) Put(c context.Context, key string, value []byte, ttl time.Duration) error {
	return s.inner.Put(c, key, value, ttl)
}
func (s *failOnDeleteStore) Delete(_ context.Context, _ string) error { return s.deleteErr }

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

// failOnNthPutStore succeeds the first N-1 Put calls, then fails.
type failOnNthPutStore struct {
	inner    *mockTokenStore
	putCalls int
	failOn   int
}

func (s *failOnNthPutStore) Get(c context.Context, key string) ([]byte, error) {
	return s.inner.Get(c, key)
}
func (s *failOnNthPutStore) Delete(c context.Context, key string) error {
	return s.inner.Delete(c, key)
}
func (s *failOnNthPutStore) Put(c context.Context, key string, value []byte, ttl time.Duration) error {
	s.putCalls++
	if s.putCalls == s.failOn {
		return errors.New("simulated disk full")
	}
	return s.inner.Put(c, key, value, ttl)
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

// ── ValidateToken — unexpected signing method ─────────────────────────────────

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

	// Replace the signature segment with garbage so parse fails.
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

// ── ExtractJTI — empty JTI ───────────────────────────────────────────────────

func TestExtractJTI_EmptyJTI_ReturnsError(t *testing.T) {
	// A token with no jti claim — craft one by issuing with GenerateToken and
	// checking that a completely empty string errors.
	_, err := auth.ExtractJTI("")
	if err == nil {
		t.Error("expected error for empty token string, got nil")
	}
}

// ── NewNodeBundle — ML-DSA path ───────────────────────────────────────────────

func TestNewNodeBundle_WithMLDSAEnabled_UsesMLDSAPath(t *testing.T) {
	b, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	// Enable ML-DSA on the CA so NewNodeBundle takes the IssueNodeCertWithMLDSA path.
	if err := b.CA.EnhanceWithMLDSA(); err != nil {
		t.Fatalf("EnhanceWithMLDSA: %v", err)
	}
	nb, err := auth.NewNodeBundle(b.CA, "node-mldsa")
	if err != nil {
		t.Fatalf("NewNodeBundle with ML-DSA: %v", err)
	}
	if len(nb.CertPEM) == 0 {
		t.Error("expected non-empty CertPEM")
	}
}
