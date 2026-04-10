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

// ── Mock TokenStore ────────────────────────────────────────────────────────────

type mockTokenStore struct {
	mu   sync.Mutex
	data map[string][]byte
	err  error // if set, all operations return this error
}

func newMockStore() *mockTokenStore {
	return &mockTokenStore{data: make(map[string][]byte)}
}

func (s *mockTokenStore) Get(key string) ([]byte, error) {
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

func (s *mockTokenStore) Put(key string, value []byte, ttl time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte{}, value...)
	return nil
}

func (s *mockTokenStore) Delete(key string) error {
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
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tm == nil {
		t.Fatal("expected non-nil TokenManager")
	}
	// Secret should now be stored in the mock store.
	if _, ok := store.data[auth.JWTSecretKey]; !ok {
		t.Error("JWT secret should be persisted to store on first start")
	}
}

func TestNewTokenManager_LoadsExistingSecret(t *testing.T) {
	store := newMockStore()

	// Simulate a pre-existing secret from a previous run.
	existingSecret := make([]byte, 32)
	for i := range existingSecret {
		existingSecret[i] = byte(i + 1)
	}
	store.data[auth.JWTSecretKey] = existingSecret

	tm1, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("first NewTokenManager: %v", err)
	}

	// Token produced by first manager should validate with second (same secret).
	tokenStr, err := tm1.GenerateToken("subject", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	tm2, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("second NewTokenManager: %v", err)
	}

	if _, err := tm2.ValidateToken(tokenStr); err != nil {
		t.Errorf("token from tm1 should validate with tm2 (same secret): %v", err)
	}
}

// ── GenerateToken ─────────────────────────────────────────────────────────────

func TestGenerateToken_ReturnsNonEmptyString(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	tok, err := tm.GenerateToken("user-1", "admin", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token string")
	}
}

func TestGenerateToken_StoresJTI(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(store)

	_, err := tm.GenerateToken("user-1", "admin", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// At least one key should have the JTI prefix.
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
	tm, _ := auth.NewTokenManager(store)

	tok1, _ := tm.GenerateToken("u", "admin", time.Minute)
	tok2, _ := tm.GenerateToken("u", "admin", time.Minute)

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
	tm, _ := auth.NewTokenManager(newMockStore())
	tok, _ := tm.GenerateToken("alice", "admin", time.Minute)

	claims, err := tm.ValidateToken(tok)
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
	tm, _ := auth.NewTokenManager(newMockStore())
	// Generate token that expires immediately.
	tok, _ := tm.GenerateToken("user", "node", -time.Second)

	_, err := tm.ValidateToken(tok)
	if err == nil {
		t.Error("expected error for expired token, got nil")
	}
}

func TestValidateToken_InvalidSignature_Rejected(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	tok, _ := tm.GenerateToken("user", "admin", time.Minute)

	// Replace the signature segment (third part) with a bogus value.
	parts := strings.SplitN(tok, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3-part JWT, got %d parts", len(parts))
	}
	tampered := parts[0] + "." + parts[1] + ".invalidsignatureXXXXXXXXXXXX"

	_, err := tm.ValidateToken(tampered)
	if err == nil {
		t.Error("expected error for tampered token")
	}
}

func TestValidateToken_RevokedJTI_Rejected(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(store)

	tok, _ := tm.GenerateToken("user", "admin", time.Minute)
	jti, _ := auth.ExtractJTI(tok)

	if err := tm.RevokeToken(jti); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	_, err := tm.ValidateToken(tok)
	if err == nil {
		t.Error("expected error for revoked token, got nil")
	}
}

func TestValidateToken_GarbageString_Rejected(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	_, err := tm.ValidateToken("not.a.jwt")
	if err == nil {
		t.Error("expected error for garbage token")
	}
}

// ── RevokeToken ───────────────────────────────────────────────────────────────

func TestRevokeToken_DeletesJTI_FromStore(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(store)

	tok, _ := tm.GenerateToken("user", "admin", time.Minute)
	jti, _ := auth.ExtractJTI(tok)
	jtiKey := auth.JTIPrefix + jti

	// JTI should be present before revocation.
	if _, ok := store.data[jtiKey]; !ok {
		t.Fatal("JTI not found in store before revocation")
	}

	if err := tm.RevokeToken(jti); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	// JTI should be gone after revocation.
	if _, ok := store.data[jtiKey]; ok {
		t.Error("JTI should be deleted after revocation")
	}
}

func TestRevokeToken_OneToken_DoesNotAffectOthers(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())

	tok1, _ := tm.GenerateToken("user", "admin", time.Minute)
	tok2, _ := tm.GenerateToken("user", "admin", time.Minute)

	jti1, _ := auth.ExtractJTI(tok1)
	if err := tm.RevokeToken(jti1); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	// tok2 should still be valid.
	if _, err := tm.ValidateToken(tok2); err != nil {
		t.Errorf("tok2 should still be valid after revoking tok1: %v", err)
	}
}

// ── Root token ────────────────────────────────────────────────────────────────

func TestGenerateRootToken_ProducesToken(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	tok, err := tm.GenerateRootToken()
	if err != nil {
		t.Fatalf("GenerateRootToken: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty root token")
	}
}

func TestGenerateRootToken_IdempotentOnSecondCall(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	tok1, _ := tm.GenerateRootToken()
	tok2, _ := tm.GenerateRootToken()

	if tok1 != tok2 {
		t.Error("GenerateRootToken should return the same token on second call")
	}
}

func TestGenerateRootToken_PersistedToStore(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(store)
	tok, _ := tm.GenerateRootToken()

	stored, err := store.Get(auth.RootTokenKey)
	if err != nil {
		t.Fatalf("root token not in store: %v", err)
	}
	if string(stored) != tok {
		t.Error("stored root token does not match returned token")
	}
}

func TestGetRootToken_ReturnsStoredToken(t *testing.T) {
	store := newMockStore()
	tm, _ := auth.NewTokenManager(store)

	generated, _ := tm.GenerateRootToken()
	retrieved, err := tm.GetRootToken()
	if err != nil {
		t.Fatalf("GetRootToken: %v", err)
	}
	if retrieved != generated {
		t.Errorf("GetRootToken mismatch: want %q, got %q", generated, retrieved)
	}
}

func TestGetRootToken_NoToken_ReturnsError(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	_, err := tm.GetRootToken()
	if err == nil {
		t.Error("GetRootToken should return error when no root token exists")
	}
}

// ── PrintRootTokenInstructions / truncate ────────────────────────────────────

func TestPrintRootTokenInstructions_DoesNotPanic(t *testing.T) {
	// Smoke-test: just ensure it doesn't panic.
	auth.PrintRootTokenInstructions("tok-short")
	auth.PrintRootTokenInstructions(strings.Repeat("x", 200)) // long token
}

// ── ExtractJTI ────────────────────────────────────────────────────────────────

func TestExtractJTI_ReturnsJTI(t *testing.T) {
	tm, _ := auth.NewTokenManager(newMockStore())
	tok, _ := tm.GenerateToken("u", "admin", time.Minute)

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

// ── TokenStoreAdapter ─────────────────────────────────────────────────────────

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

func TestTokenStoreAdapter_GetPutDelete(t *testing.T) {
	inner := newPersistenceStore()
	adapter := auth.NewTokenStoreAdapter(inner)

	// Put then Get.
	if err := adapter.Put("k1", []byte("value"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := adapter.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Errorf("want 'value', got %q", got)
	}

	// Delete then Get should fail.
	if err := adapter.Delete("k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := adapter.Get("k1"); err == nil {
		t.Error("Get after Delete should return error")
	}
}

func TestTokenStoreAdapter_PutWithTTL_UsesPutWithTTL(t *testing.T) {
	inner := newPersistenceStore()
	adapter := auth.NewTokenStoreAdapter(inner)

	ttl := 15 * time.Minute
	if err := adapter.Put("k-ttl", []byte("v"), ttl); err != nil {
		t.Fatalf("Put with TTL: %v", err)
	}

	// The inner store should have recorded the TTL.
	if inner.ttls["k-ttl"] != ttl {
		t.Errorf("want ttl %v, got %v", ttl, inner.ttls["k-ttl"])
	}
}

// ── Error path coverage ───────────────────────────────────────────────────────

// failOnPutStore succeeds Get (returns key-not-found) and fails Put.
type failOnPutStore struct {
	putErr error
}

func (s *failOnPutStore) Get(_ string) ([]byte, error) {
	return nil, errors.New("key not found")
}
func (s *failOnPutStore) Put(_ string, _ []byte, _ time.Duration) error { return s.putErr }
func (s *failOnPutStore) Delete(_ string) error                         { return nil }

func TestNewTokenManager_PutFails_ReturnsError(t *testing.T) {
	store := &failOnPutStore{putErr: errors.New("disk full")}
	_, err := auth.NewTokenManager(store)
	if err == nil {
		t.Error("expected error when Put fails during secret storage, got nil")
	}
}

// failOnJTIPutStore wraps mockTokenStore and fails on the second Put call.
type failOnJTIPutStore struct {
	inner    *mockTokenStore
	putCalls int
}

func (s *failOnJTIPutStore) Get(key string) ([]byte, error) { return s.inner.Get(key) }
func (s *failOnJTIPutStore) Delete(key string) error        { return s.inner.Delete(key) }
func (s *failOnJTIPutStore) Put(key string, value []byte, ttl time.Duration) error {
	s.putCalls++
	if s.putCalls > 1 {
		return errors.New("JTI store failure")
	}
	return s.inner.Put(key, value, ttl)
}

func TestGenerateToken_JTIStoreFails_ReturnsError(t *testing.T) {
	store := &failOnJTIPutStore{inner: newMockStore()}
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	_, err = tm.GenerateToken("user", "admin", time.Minute)
	if err == nil {
		t.Error("expected error when JTI store fails, got nil")
	}
}

// failOnDeleteStore wraps mockTokenStore and fails all Delete calls.
type failOnDeleteStore struct {
	inner     *mockTokenStore
	deleteErr error
}

func (s *failOnDeleteStore) Get(key string) ([]byte, error) { return s.inner.Get(key) }
func (s *failOnDeleteStore) Put(key string, value []byte, ttl time.Duration) error {
	return s.inner.Put(key, value, ttl)
}
func (s *failOnDeleteStore) Delete(_ string) error { return s.deleteErr }

func TestRevokeToken_DeleteFails_ReturnsError(t *testing.T) {
	base := newMockStore()
	store := &failOnDeleteStore{inner: base, deleteErr: errors.New("delete failed")}
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	if err := tm.RevokeToken("any-jti"); err == nil {
		t.Error("expected error when Delete fails, got nil")
	}
}

// failOnNthPutStore succeeds the first N-1 Put calls, then fails.
type failOnNthPutStore struct {
	inner    *mockTokenStore
	putCalls int
	failOn   int // fail when putCalls == failOn
}

func (s *failOnNthPutStore) Get(key string) ([]byte, error) { return s.inner.Get(key) }
func (s *failOnNthPutStore) Delete(key string) error        { return s.inner.Delete(key) }
func (s *failOnNthPutStore) Put(key string, value []byte, ttl time.Duration) error {
	s.putCalls++
	if s.putCalls == s.failOn {
		return errors.New("simulated disk full")
	}
	return s.inner.Put(key, value, ttl)
}

// TestGenerateRootToken_StoreFails_ReturnsError covers the
// "store root token: …" error path in GenerateRootToken.
// Call sequence:
//  1. NewTokenManager → Get(JWTSecretKey) fails → Put(JWTSecretKey) succeeds  [Put #1]
//  2. GenerateRootToken → Get(RootTokenKey) fails → GenerateToken → Put(JTI) succeeds  [Put #2]
//  3. GenerateRootToken → Put(RootTokenKey) fails  [Put #3 = failOn]
func TestGenerateRootToken_StoreFails_ReturnsError(t *testing.T) {
	store := &failOnNthPutStore{inner: newMockStore(), failOn: 3}
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	_, err = tm.GenerateRootToken()
	if err == nil {
		t.Error("expected error when storing root token fails, got nil")
	}
	if !strings.Contains(err.Error(), "store root token") {
		t.Errorf("want 'store root token' in error, got: %v", err)
	}
}
