// internal/api/middleware.go
//
// HTTP middleware for the coordinator API:
//   authMiddleware    — validates JWT Bearer tokens; injects claims into context.
//   wsAuthMiddleware  — validates JWT for WebSocket connections (query param or header).
//   adminMiddleware   — rejects non-admin JWT roles; must be composed after authMiddleware.
//   tokenIssueAllow   — per-subject token-bucket rate limiter for POST /admin/tokens.

package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/time/rate"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// ── Token admin rate-limit constants ─────────────────────────────────────────

// AUDIT M2 (fixed): per-subject token-bucket rate limit on POST /admin/tokens.
// Previously there was no rate limit, allowing an admin to flood the token store.
const (
	tokenIssueRate  = 1.0 // 1 token per second per subject
	tokenIssueBurst = 5   // allow short bursts up to 5
)

// ── Token admin constants ─────────────────────────────────────────────────────

const (
	maxTokenTTLHours     = 720 // 30 days
	defaultTokenTTLHours = 8
)

var validRoles = map[string]bool{"admin": true, "node": true}

// ── authMiddleware ────────────────────────────────────────────────────────────

// authMiddleware validates JWT Bearer tokens and injects claims into context.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// AUDIT H2 (fixed): if tokenManager is nil, refuse to serve rather
		// than silently bypass auth. Tests that need no-auth must call
		// Server.DisableAuth() explicitly.
		if s.tokenManager == nil {
			if s.disableAuth {
				next.ServeHTTP(w, r)
				return
			}
			slog.Error("auth middleware: tokenManager is nil and DisableAuth not set")
			writeError(w, http.StatusInternalServerError, "authentication not configured")
			return
		}

		// Extract token from Authorization header
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			if s.audit != nil {
				if err := s.audit.LogAuthFailure(r.Context(), "missing authorization header", r.RemoteAddr); err != nil {
					logAuditErr(true, "auth.missing_bearer", err)
				}
			}
			writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Validate token
		claims, err := s.tokenManager.ValidateToken(r.Context(), token)
		if err != nil {
			if s.audit != nil {
				if aerr := s.audit.LogAuthFailure(r.Context(), err.Error(), r.RemoteAddr); aerr != nil {
					logAuditErr(true, "auth.invalid_token", aerr)
				}
			}
			slog.Error("token validation failed", slog.String("remote", r.RemoteAddr), slog.Any("err", err))
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}

		// Store claims in request context
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// ── wsAuthMiddleware ──────────────────────────────────────────────────────────

// wsAuthMiddleware validates JWT for WebSocket connections. The token may
// arrive in the ?token= query parameter (browsers cannot set headers on
// WebSocket upgrades) or in the Authorization: Bearer header.
func (s *Server) wsAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// AUDIT H2 (fixed): nil tokenManager now returns 500 unless the
		// server was explicitly constructed with DisableAuth().
		if s.tokenManager == nil {
			if s.disableAuth {
				next.ServeHTTP(w, r)
				return
			}
			slog.Error("ws auth middleware: tokenManager is nil and DisableAuth not set")
			http.Error(w, "authentication not configured", http.StatusInternalServerError)
			return
		}

		// For WebSocket, token can be in query param (browsers can't set headers in EventSource/WS)
		token := r.URL.Query().Get("token")
		if token == "" {
			// Fall back to Authorization header
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if token == "" {
			if s.audit != nil {
				if err := s.audit.LogAuthFailure(r.Context(), "missing token", r.RemoteAddr); err != nil {
					logAuditErr(true, "wsauth.missing_token", err)
				}
			}
			http.Error(w, "unauthorized: missing token", http.StatusUnauthorized)
			return
		}

		claims, err := s.tokenManager.ValidateToken(r.Context(), token)
		if err != nil {
			if s.audit != nil {
				if aerr := s.audit.LogAuthFailure(r.Context(), err.Error(), r.RemoteAddr); aerr != nil {
					logAuditErr(true, "wsauth.invalid_token", aerr)
				}
			}
			slog.Error("ws token validation failed", slog.String("remote", r.RemoteAddr), slog.Any("err", err))
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// ── adminMiddleware ───────────────────────────────────────────────────────────

// adminMiddleware rejects requests whose JWT role is not "admin".
// Must be composed inside authMiddleware so claims are already in context.
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// AUDIT H2 (fixed): when tokenManager is nil and DisableAuth has NOT
		// been set, authMiddleware already short-circuited with 500, so this
		// branch is never reached in that case. When DisableAuth is set (test
		// path) we fall through without a role check — same as before.
		if s.tokenManager != nil {
			claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
			if !ok || claims.Role != "admin" {
				writeError(w, http.StatusForbidden, "admin role required")
				return
			}
		}
		next.ServeHTTP(w, r)
	}
}

// ── tokenIssueAllow ───────────────────────────────────────────────────────────

// tokenIssueAllow returns true if the caller identified by subject is within
// the token-issuance rate limit. It creates a per-subject limiter on first use.
func (s *Server) tokenIssueAllow(subject string) bool {
	s.tokenIssueMu.Lock()
	lim, ok := s.tokenIssueLimiters[subject]
	if !ok {
		lim = rate.NewLimiter(tokenIssueRate, tokenIssueBurst)
		s.tokenIssueLimiters[subject] = lim
	}
	s.tokenIssueMu.Unlock()
	return lim.Allow()
}
