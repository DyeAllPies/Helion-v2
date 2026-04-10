// internal/auth/jwt.go
//
// JWT (JSON Web Token) authentication for the coordinator's REST API.
//
// Phase 4 security hardening:
// ──────────────────────────
//   - Short-lived tokens (15-minute expiry) to limit exposure window
//   - JTI (JWT ID) stored in BadgerDB for revocation checking
//   - Revocation: delete JTI record from BadgerDB
//   - No refresh tokens in v2.0 (stateless, re-authenticate after expiry)
//
// Token claims:
// ────────────
//   - sub (subject): User or node ID (e.g., "root", "node-abc123")
//   - exp (expiry): Unix timestamp, 15 minutes from issue
//   - iat (issued at): Unix timestamp
//   - jti (JWT ID): Unique token ID (UUID v4), used for revocation
//   - role: "admin" or "node" (for future RBAC)
//
// Root token:
// ──────────
// On first startup, the coordinator generates a long-lived root token with:
//   - sub: "root"
//   - exp: 10 years from now (effectively never expires)
//   - role: "admin"
//
// The root token is:
//   1. Printed to stdout on first start (operator must save it)
//   2. Stored in BadgerDB at key "auth:root_token"
//   3. Used to bootstrap all other authentication
//
// On subsequent starts, the coordinator loads the root token from BadgerDB
// and does NOT generate a new one (prevents token churn).
//
// Revocation:
// ──────────
// A token is revoked by deleting its JTI record from BadgerDB:
//   DELETE BadgerDB["auth:jti:<jti>"]
//
// Validation checks:
//   1. Signature valid (HS256 with secret key)
//   2. Not expired (exp > now)
//   3. JTI exists in BadgerDB (not revoked)
//
// Tokens are rejected within 1 second of revocation due to the validation
// sequence: signature first (fast), then BadgerDB lookup (sub-ms latency).
//
// Secret key:
// ──────────
// The JWT signing key is a 256-bit random value generated on first start
// and stored in BadgerDB at key "auth:jwt_secret". It's loaded on every
// start and never changes (preserves existing tokens across restarts).

package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	// TokenExpiry is the default token lifetime (15 minutes).
	TokenExpiry = 15 * time.Minute

	// RootTokenExpiry is the expiry for the root token (10 years).
	RootTokenExpiry = 10 * 365 * 24 * time.Hour

	// JTIPrefix is the BadgerDB key prefix for JTI records.
	JTIPrefix = "auth:jti:"

	// RootTokenKey is the BadgerDB key for the root token.
	RootTokenKey = "auth:root_token"

	// JWTSecretKey is the BadgerDB key for the JWT signing secret.
	JWTSecretKey = "auth:jwt_secret"
)

// Claims represents the JWT claims for Helion tokens.
type Claims struct {
	jwt.RegisteredClaims
	Role string `json:"role"` // "admin" or "node"
}

// TokenManager handles JWT creation, validation, and revocation.
type TokenManager struct {
	secret []byte // HS256 signing key
	store  TokenStore
}

// TokenStore is the interface for BadgerDB storage operations.
type TokenStore interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte, ttl time.Duration) error
	Delete(key string) error
}

// NewTokenManager creates a TokenManager with the given secret and store.
// If the secret is nil, a new random secret is generated and stored in BadgerDB.
func NewTokenManager(store TokenStore) (*TokenManager, error) {
	// Try to load existing secret from BadgerDB
	secret, err := store.Get(JWTSecretKey)
	if err != nil {
		// No secret exists; generate a new one
		secret = make([]byte, 32) // 256 bits
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate JWT secret: %w", err)
		}

		// Store the secret for future starts
		if err := store.Put(JWTSecretKey, secret, 0); err != nil {
			return nil, fmt.Errorf("store JWT secret: %w", err)
		}
	}

	return &TokenManager{secret: secret, store: store}, nil
}

// GenerateToken creates a new JWT with the given subject and role.
// The token is valid for TokenExpiry (15 minutes) and has a unique JTI.
// The JTI is stored in BadgerDB with a TTL matching the token expiry.
func (tm *TokenManager) GenerateToken(subject, role string, expiry time.Duration) (string, error) {
	now := time.Now()
	jti := uuid.New().String()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti,
		},
		Role: role,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString(tm.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	// Store JTI in BadgerDB with TTL = token expiry
	jtiKey := JTIPrefix + jti
	if err := tm.store.Put(jtiKey, []byte(subject), expiry); err != nil {
		return "", fmt.Errorf("store JTI: %w", err)
	}

	return signedToken, nil
}

// ValidateToken validates a JWT and returns its claims.
// Returns an error if:
//   - Signature is invalid
//   - Token is expired
//   - JTI does not exist in BadgerDB (token was revoked)
func (tm *TokenManager) ValidateToken(tokenString string) (*Claims, error) {
	// Parse and validate signature
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		// Ensure the signing method is HS256
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return tm.secret, nil
	})

	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Check expiry (jwt library already does this, but double-check)
	if time.Now().After(claims.ExpiresAt.Time) {
		return nil, fmt.Errorf("token expired")
	}

	// Check if JTI exists in BadgerDB (not revoked)
	jtiKey := JTIPrefix + claims.ID
	if _, err := tm.store.Get(jtiKey); err != nil {
		return nil, fmt.Errorf("token revoked or invalid JTI")
	}

	return claims, nil
}

// RevokeToken revokes a token by deleting its JTI from BadgerDB.
// After this call, ValidateToken will reject the token immediately.
func (tm *TokenManager) RevokeToken(jti string) error {
	jtiKey := JTIPrefix + jti
	if err := tm.store.Delete(jtiKey); err != nil {
		return fmt.Errorf("delete JTI: %w", err)
	}
	return nil
}

// GenerateRootToken creates the initial root token on first start.
// This token has a 10-year expiry and "admin" role.
// Returns the token string (to print to stdout) and stores it in BadgerDB.
func (tm *TokenManager) GenerateRootToken() (string, error) {
	// Check if root token already exists
	existingToken, err := tm.store.Get(RootTokenKey)
	if err == nil {
		// Root token exists; return it without generating a new one
		return string(existingToken), nil
	}

	// Generate new root token
	token, err := tm.GenerateToken("root", "admin", RootTokenExpiry)
	if err != nil {
		return "", fmt.Errorf("generate root token: %w", err)
	}

	// Store root token in BadgerDB (no TTL, persists forever)
	if err := tm.store.Put(RootTokenKey, []byte(token), 0); err != nil {
		return "", fmt.Errorf("store root token: %w", err)
	}

	return token, nil
}

// GetRootToken retrieves the root token from BadgerDB.
// Returns empty string if no root token exists (should call GenerateRootToken first).
func (tm *TokenManager) GetRootToken() (string, error) {
	token, err := tm.store.Get(RootTokenKey)
	if err != nil {
		return "", fmt.Errorf("get root token: %w", err)
	}
	return string(token), nil
}

// ExtractJTI extracts the JTI claim from a JWT without full validation.
// Used for revocation when we need to revoke a token without validating it.
func ExtractJTI(tokenString string) (string, error) {
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, &Claims{})
	if err != nil {
		return "", fmt.Errorf("parse token (unverified): %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return "", fmt.Errorf("invalid claims type")
	}

	return claims.ID, nil
}

// TokenStoreAdapter adapts the persistence.Store to TokenStore interface.
// This allows us to use the existing BadgerDB store without circular deps.
type TokenStoreAdapter struct {
	store interface {
		Get(ctx context.Context, key string) ([]byte, error)
		Put(ctx context.Context, key string, value []byte) error
		PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error
		Delete(ctx context.Context, key string) error
	}
}

// NewTokenStoreAdapter wraps a persistence.Store for use with TokenManager.
func NewTokenStoreAdapter(store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}) *TokenStoreAdapter {
	return &TokenStoreAdapter{store: store}
}

// Get implements TokenStore.Get using context.Background().
func (a *TokenStoreAdapter) Get(key string) ([]byte, error) {
	return a.store.Get(context.Background(), key)
}

// Put implements TokenStore.Put. If ttl > 0, uses PutWithTTL.
func (a *TokenStoreAdapter) Put(key string, value []byte, ttl time.Duration) error {
	ctx := context.Background()
	if ttl > 0 {
		return a.store.PutWithTTL(ctx, key, value, ttl)
	}
	return a.store.Put(ctx, key, value)
}

// Delete implements TokenStore.Delete.
func (a *TokenStoreAdapter) Delete(key string) error {
	return a.store.Delete(context.Background(), key)
}

// PrintRootTokenInstructions prints the root token to stdout with instructions.
// Called on coordinator first start.
func PrintRootTokenInstructions(token string) {
	fmt.Println("╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("║         HELION COORDINATOR - FIRST START                       ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════╣")
	fmt.Println("║ Root API token generated. Save this token securely!            ║")
	fmt.Println("║ It will NOT be shown again. Use it to authenticate API calls.  ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║ Token: %-56s ║\n", truncate(token, 56))
	fmt.Println("╠════════════════════════════════════════════════════════════════╣")
	fmt.Println("║ Usage:                                                         ║")
	fmt.Println("║   curl -H 'Authorization: Bearer <token>' \\                    ║")
	fmt.Println("║        https://coordinator:8443/jobs                           ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════╝")
}

// truncate truncates a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
