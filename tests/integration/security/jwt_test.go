// tests/integration/security/jwt_test.go
//
// Integration tests for JWT authentication system.
//
// Phase 4 exit criteria:
//   - Revoked token rejected within 1 second
//   - Expired token rejected
//   - Reuse of previous token rejected (after revocation)

package security

import (
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// mockTokenStore implements auth.TokenStore for testing.
type mockTokenStore struct {
	data map[string][]byte
	ttls map[string]time.Time
}

func newMockTokenStore() *mockTokenStore {
	return &mockTokenStore{
		data: make(map[string][]byte),
		ttls: make(map[string]time.Time),
	}
}

func (m *mockTokenStore) Get(key string) ([]byte, error) {
	// Check if expired
	if expiry, ok := m.ttls[key]; ok && time.Now().After(expiry) {
		delete(m.data, key)
		delete(m.ttls, key)
		return nil, &mockError{"key not found"}
	}
	
	val, ok := m.data[key]
	if !ok {
		return nil, &mockError{"key not found"}
	}
	return val, nil
}

func (m *mockTokenStore) Put(key string, value []byte, ttl time.Duration) error {
	m.data[key] = value
	if ttl > 0 {
		m.ttls[key] = time.Now().Add(ttl)
	}
	return nil
}

func (m *mockTokenStore) Delete(key string) error {
	delete(m.data, key)
	delete(m.ttls, key)
	return nil
}

type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

func TestJWTGeneration(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	token, err := tm.GenerateToken("test-user", "admin", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	if token == "" {
		t.Fatal("Generated token is empty")
	}

	// Token should have three parts (header.payload.signature)
	parts := 0
	for _, c := range token {
		if c == '.' {
			parts++
		}
	}
	if parts != 2 {
		t.Errorf("Token has %d dots, expected 2", parts)
	}
}

func TestJWTValidation(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	// Test 1: Valid token accepted
	token, err := tm.GenerateToken("test-user", "admin", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	claims, err := tm.ValidateToken(token)
	if err != nil {
		t.Errorf("ValidateToken failed for valid token: %v", err)
	}

	if claims.Subject != "test-user" {
		t.Errorf("Expected subject 'test-user', got '%s'", claims.Subject)
	}

	if claims.Role != "admin" {
		t.Errorf("Expected role 'admin', got '%s'", claims.Role)
	}
}

func TestJWTExpiry(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	// Generate token with 1 second expiry
	token, err := tm.GenerateToken("test-user", "admin", 1*time.Second)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// Token should be valid immediately
	_, err = tm.ValidateToken(token)
	if err != nil {
		t.Errorf("Token should be valid immediately: %v", err)
	}

	// Wait for expiry
	time.Sleep(2 * time.Second)

	// Token should now be expired
	_, err = tm.ValidateToken(token)
	if err == nil {
		t.Error("Expected token to be expired, but validation succeeded")
	}
}

func TestJWTRevocation(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	// Generate token
	token, err := tm.GenerateToken("test-user", "admin", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// Extract JTI
	jti, err := auth.ExtractJTI(token)
	if err != nil {
		t.Fatalf("ExtractJTI failed: %v", err)
	}

	// Token should be valid
	_, err = tm.ValidateToken(token)
	if err != nil {
		t.Errorf("Token should be valid before revocation: %v", err)
	}

	// Revoke token
	if err := tm.RevokeToken(jti); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}

	// Record revocation time
	revokeTime := time.Now()

	// Token should be rejected immediately (within 1 second per Phase 4 requirement)
	time.Sleep(100 * time.Millisecond)
	_, err = tm.ValidateToken(token)
	if err == nil {
		t.Error("Expected revoked token to be rejected")
	}

	// Verify rejection happened within 1 second
	rejectionTime := time.Now()
	if rejectionTime.Sub(revokeTime) > 1*time.Second {
		t.Errorf("Revoked token rejection took %v, expected < 1s", rejectionTime.Sub(revokeTime))
	}
}

func TestJWTReuseAfterRevocation(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	token, err := tm.GenerateToken("test-user", "admin", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	jti, err := auth.ExtractJTI(token)
	if err != nil {
		t.Fatalf("ExtractJTI failed: %v", err)
	}

	// Revoke token
	if err := tm.RevokeToken(jti); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}

	// Try to use the token multiple times (should all fail)
	for i := 0; i < 5; i++ {
		_, err = tm.ValidateToken(token)
		if err == nil {
			t.Errorf("Attempt %d: Expected revoked token to be rejected", i+1)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRootTokenGeneration(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	// Generate root token
	rootToken, err := tm.GenerateRootToken()
	if err != nil {
		t.Fatalf("GenerateRootToken failed: %v", err)
	}

	if rootToken == "" {
		t.Fatal("Root token is empty")
	}

	// Validate root token
	claims, err := tm.ValidateToken(rootToken)
	if err != nil {
		t.Errorf("Root token validation failed: %v", err)
	}

	if claims.Subject != "root" {
		t.Errorf("Expected subject 'root', got '%s'", claims.Subject)
	}

	if claims.Role != "admin" {
		t.Errorf("Expected role 'admin', got '%s'", claims.Role)
	}

	// Calling GenerateRootToken again should return the same token
	rootToken2, err := tm.GenerateRootToken()
	if err != nil {
		t.Fatalf("Second GenerateRootToken failed: %v", err)
	}

	if rootToken2 != rootToken {
		t.Error("Expected same root token on subsequent calls")
	}
}

func TestInvalidTokenSignature(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	// Create a token with a different manager (different secret)
	store2 := newMockTokenStore()
	tm2, err := auth.NewTokenManager(store2)
	if err != nil {
		t.Fatalf("NewTokenManager (2) failed: %v", err)
	}

	token, err := tm2.GenerateToken("test-user", "admin", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// Try to validate with the first manager (wrong secret)
	_, err = tm.ValidateToken(token)
	if err == nil {
		t.Error("Expected token with wrong signature to be rejected")
	}
}

func TestMalformedToken(t *testing.T) {
	store := newMockTokenStore()
	tm, err := auth.NewTokenManager(store)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	malformedTokens := []string{
		"",
		"not.a.token",
		"header.payload",
		"header.payload.signature.extra",
		"definitely-not-a-jwt",
	}

	for _, token := range malformedTokens {
		_, err := tm.ValidateToken(token)
		if err == nil {
			t.Errorf("Expected malformed token '%s' to be rejected", token)
		}
	}
}
