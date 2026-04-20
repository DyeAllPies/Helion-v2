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
// On each startup the coordinator rotates the root token: the old JTI is
// revoked, a new token is generated, and the new value is printed to stdout
// (and optionally written to HELION_ROOT_TOKEN_FILE).  This eliminates the
// "10-year never-expiring token" problem and ensures that a leaked token from
// a previous run is invalidated automatically.
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
	"os"
	"path/filepath"
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
	Role string `json:"role"` // "admin" or "node" or "job" or "user"

	// RequiredCN binds the token to a specific operator cert
	// CN (feature 33 — per-operator accountability). Empty
	// = unbound (legacy behaviour: token works anywhere that
	// validates the signature).
	//
	// When non-empty, authMiddleware MUST verify that the
	// request arrived with a verified client cert whose CN
	// matches this value. A mismatch OR a cert-less request
	// produces 401 + an `EventTokenCertCNMismatch` audit
	// entry — the JWT is treated as if it had failed
	// signature validation.
	//
	// Integrity: JWT signature protects every claim, so a
	// holder of a token cannot tamper with the binding to
	// point at their own CN without invalidating the
	// signature and failing ValidateToken.
	RequiredCN string `json:"required_cn,omitempty"`
}

// TokenManager handles JWT creation, validation, and revocation.
type TokenManager struct {
	secret []byte // HS256 signing key
	store  TokenStore
}

// TokenStore is the interface for BadgerDB storage operations.
// All methods accept a context so that request cancellation and deadlines
// propagate through to the underlying BadgerDB calls.
type TokenStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// NewTokenManager creates a TokenManager with the given secret and store.
// If no secret is found in the store, a new random secret is generated and
// persisted for future starts.
func NewTokenManager(ctx context.Context, store TokenStore) (*TokenManager, error) {
	secret, err := store.Get(ctx, JWTSecretKey)
	if err != nil {
		// No secret exists; generate a new one.
		secret = make([]byte, 32) // 256 bits
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate JWT secret: %w", err)
		}
		if err := store.Put(ctx, JWTSecretKey, secret, 0); err != nil {
			return nil, fmt.Errorf("store JWT secret: %w", err)
		}
	}

	return &TokenManager{secret: secret, store: store}, nil
}

// GenerateToken creates a new JWT with the given subject and role.
// The token is valid for the given expiry and has a unique JTI.
// The JTI is stored in BadgerDB with a TTL matching the token expiry.
//
// Feature 33: this legacy signature omits the required_cn
// claim (unbound token). Callers that want to bind a token
// to an operator cert CN must use GenerateTokenWithCN.
func (tm *TokenManager) GenerateToken(ctx context.Context, subject, role string, expiry time.Duration) (string, error) {
	return tm.GenerateTokenWithCN(ctx, subject, role, "", expiry)
}

// GenerateTokenWithCN is the feature-33 extension of
// GenerateToken. When requiredCN is non-empty, the resulting
// token carries a `required_cn` claim that the auth
// middleware cross-checks against the caller's verified
// operator cert CN on every request.
//
// Empty requiredCN produces a token indistinguishable from
// one issued by the legacy GenerateToken path — the claim is
// emitted with `omitempty` so downstream consumers that don't
// know about the field still parse the payload cleanly.
//
// Holder-of-token-cannot-relax-binding property: the JWT
// signature protects every claim, so an attacker with the
// raw token cannot flip the required_cn to match their own
// cert without invalidating the signature.
func (tm *TokenManager) GenerateTokenWithCN(ctx context.Context, subject, role, requiredCN string, expiry time.Duration) (string, error) {
	now := time.Now()
	jti := uuid.New().String()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti,
		},
		Role:       role,
		RequiredCN: requiredCN,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString(tm.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	jtiKey := JTIPrefix + jti
	if err := tm.store.Put(ctx, jtiKey, []byte(subject), expiry); err != nil {
		return "", fmt.Errorf("store JTI: %w", err)
	}

	return signedToken, nil
}

// ValidateToken validates a JWT and returns its claims.
// Returns an error if:
//   - Signature is invalid
//   - Token is expired
//   - JTI does not exist in BadgerDB (token was revoked)
func (tm *TokenManager) ValidateToken(ctx context.Context, tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
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

	if time.Now().After(claims.ExpiresAt.Time) {
		return nil, fmt.Errorf("token expired")
	}

	jtiKey := JTIPrefix + claims.ID
	if _, err := tm.store.Get(ctx, jtiKey); err != nil {
		return nil, fmt.Errorf("token revoked or invalid JTI")
	}

	return claims, nil
}

// RevokeToken revokes a token by deleting its JTI from BadgerDB.
// After this call, ValidateToken will reject the token immediately.
func (tm *TokenManager) RevokeToken(ctx context.Context, jti string) error {
	jtiKey := JTIPrefix + jti
	if err := tm.store.Delete(ctx, jtiKey); err != nil {
		return fmt.Errorf("delete JTI: %w", err)
	}
	return nil
}

// RotateRootToken revokes the existing root token (if any) and issues a new
// one.  It is called on every coordinator start so that a token leaked from a
// prior run is invalidated automatically.
// Returns the new token string so the caller can display or persist it.
func (tm *TokenManager) RotateRootToken(ctx context.Context) (string, error) {
	// AUDIT 2026-04-12-01/M4 (fixed): revoke the old token's JTI using validated
	// parsing instead of ParseUnverified. If signature validation fails (e.g.
	// the secret was regenerated), the old token is already invalid under the
	// new secret — skip revocation entirely rather than parsing untrusted data.
	existing, err := tm.store.Get(ctx, RootTokenKey)
	if err == nil && len(existing) > 0 {
		if claims, err := tm.ValidateToken(ctx, string(existing)); err == nil {
			_ = tm.RevokeToken(ctx, claims.ID)
		}
		_ = tm.store.Delete(ctx, RootTokenKey)
	}

	token, err := tm.GenerateToken(ctx, "root", "admin", RootTokenExpiry)
	if err != nil {
		return "", fmt.Errorf("generate root token: %w", err)
	}

	if err := tm.store.Put(ctx, RootTokenKey, []byte(token), 0); err != nil {
		return "", fmt.Errorf("store root token: %w", err)
	}

	return token, nil
}

// GetRootToken retrieves the current root token from BadgerDB.
// Returns an error if no root token exists.
func (tm *TokenManager) GetRootToken(ctx context.Context) (string, error) {
	token, err := tm.store.Get(ctx, RootTokenKey)
	if err != nil {
		return "", fmt.Errorf("get root token: %w", err)
	}
	return string(token), nil
}

// extractJTIUnchecked extracts the JTI claim from a JWT without validating
// the signature or checking the BadgerDB revocation store.
//
// AUDIT C3 (fixed): previously this function was exported as ExtractJTI, making
// it easy for callers to accidentally extract JTIs from attacker-controlled
// tokens without signature validation. It is now unexported; external callers
// must use ExtractJTIFromValidatedToken which validates the signature first.
//
// SECURITY: This function uses ParseUnverified and MUST only be called on
// tokens retrieved from trusted internal storage (e.g., the BadgerDB token
// store). Passing attacker-controlled tokens here is safe only if the JTI is
// subsequently used solely for revocation — an attacker cannot forge a valid
// JTI because revocation deletes the JTI from the store, and ValidateToken
// always re-checks the store. Never use this to authenticate a request.
func extractJTIUnchecked(tokenString string) (string, error) {
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, &Claims{})
	if err != nil {
		return "", fmt.Errorf("parse token (unverified): %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return "", fmt.Errorf("invalid claims type")
	}

	if claims.ID == "" {
		return "", fmt.Errorf("token has no JTI")
	}

	return claims.ID, nil
}

// ExtractJTIFromValidatedToken validates the token's signature and revocation
// status, then returns its JTI. Use this whenever the token comes from an
// external (untrusted) source or when you need a strongly authoritative JTI.
func ExtractJTIFromValidatedToken(ctx context.Context, tokenString string, tm *TokenManager) (string, error) {
	claims, err := tm.ValidateToken(ctx, tokenString)
	if err != nil {
		return "", fmt.Errorf("validate token: %w", err)
	}
	if claims.ID == "" {
		return "", fmt.Errorf("validated token has no JTI")
	}
	return claims.ID, nil
}

// WriteRootToken persists the root token to path with owner-only permissions
// and prints a single-line message pointing at that path.
//
// AUDIT H1 (fixed): previously the raw token was rendered in an ASCII banner
// to stdout on every coordinator start, leaking into container logs, process
// supervisors, and any log-aggregation pipeline that captured stdout. Now the
// token value never touches stdout — the caller reads it from the file on
// disk (mode 0600) and can apply its own access controls.
//
// The parent directory is created with 0700 if it does not already exist.
// Returns an error if the parent cannot be created or the file cannot be
// written; callers should treat a non-nil return as a fatal startup error.
func WriteRootToken(token, path string) error {
	if path == "" {
		return fmt.Errorf("WriteRootToken: path is required")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("WriteRootToken: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("WriteRootToken: write %s: %w", path, err)
	}
	fmt.Printf("root token written to %s (mode 0600)\n", path)
	return nil
}
