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
	"time"

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

// ── Analytics rate-limit constants ──────────────────────────────────────────

// Per-subject token-bucket limiter for GET /api/analytics/*. Analytics queries
// run PERCENTILE_CONT + ORDER BY on job_summary, which is expensive as data
// grows — without this limit, an authenticated user could DoS the coordinator.
//
// The dashboard loads 5 charts per page open (5 requests instantly) and users
// may navigate repeatedly, so a tight bucket (burst 10) would rate-limit real
// usage. Rate 2 rps + burst 30 accommodates ~6 full dashboard loads in quick
// succession and caps sustained load at ~120 queries per minute per subject —
// far above normal use, well below what a DoS attack would need.
const (
	analyticsQueryRate  = 2.0
	analyticsQueryBurst = 30
)

// ── Analytics input bounds ──────────────────────────────────────────────────

// Maximum time range for analytics queries. Longer windows cause full-table
// scans and unbounded PostgreSQL memory usage.
const analyticsMaxRange = 365 * 24 * time.Hour

// Maximum page size for GET /api/analytics/events. Prevents pulling the
// entire events table with limit=999999999.
const analyticsMaxLimit = 1000

// ── Token admin constants ─────────────────────────────────────────────────────

const (
	maxTokenTTLHours     = 720 // 30 days
	defaultTokenTTLHours = 8
)

// validRoles lists the JWT roles the /admin/tokens endpoint will
// mint. Enforcement of what each role can DO lives in the handler
// middleware (adminMiddleware checks role=="admin"), not here.
//
//   - "admin" — full REST surface including /admin/* (token
//     issuance + node revocation).
//   - "node"  — currently used by node-side callbacks
//     (ReportResult, Heartbeat); distinct from admin.
//   - "job"   — (feature 19) short-lived credentials minted by
//     the workflow submitter for in-workflow scripts that need
//     to call back into the coordinator (e.g. register.py
//     POSTing to /api/datasets + /api/models). adminMiddleware
//     rejects this role at 403, so a leaked job token cannot
//     mint more tokens or revoke nodes. Resource-scoped
//     permissions (per-endpoint allowlist) is a future
//     enhancement; today the role bounds blast radius to the
//     non-admin REST surface + token TTL.
var validRoles = map[string]bool{"admin": true, "node": true, "job": true}

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

// ── analyticsQueryAllow ──────────────────────────────────────────────────────

// analyticsQueryAllow returns true if the caller identified by subject is
// within the analytics-query rate limit. Creates a per-subject limiter on
// first use. Same token-bucket pattern as tokenIssueAllow.
func (s *Server) analyticsQueryAllow(subject string) bool {
	s.analyticsMu.Lock()
	lim, ok := s.analyticsLimiters[subject]
	if !ok {
		lim = rate.NewLimiter(analyticsQueryRate, analyticsQueryBurst)
		s.analyticsLimiters[subject] = lim
	}
	s.analyticsMu.Unlock()
	return lim.Allow()
}

// ── Registry rate-limit constants ──────────────────────────────────────────

// Per-subject token-bucket limiter on /api/datasets and /api/models.
// Registry writes are cheap individually (one BadgerDB put) but unbounded
// register rates from a single authed subject would flood the audit
// stream and chew through disk on the shared DB. Rate 2 rps + burst 30
// matches the analytics bucket — one flow, one operator alert threshold.
const (
	registryQueryRate  = 2.0
	registryQueryBurst = 30
)

// registryQueryAllow returns true if the caller identified by subject is
// within the registry rate limit. Same token-bucket pattern as
// analyticsQueryAllow; creates a per-subject limiter on first use.
func (s *Server) registryQueryAllow(subject string) bool {
	s.registryMu.Lock()
	lim, ok := s.registryLimiters[subject]
	if !ok {
		lim = rate.NewLimiter(registryQueryRate, registryQueryBurst)
		s.registryLimiters[subject] = lim
	}
	s.registryMu.Unlock()
	return lim.Allow()
}
