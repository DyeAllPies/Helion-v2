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
	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// ── Token admin rate-limit constants ─────────────────────────────────────────

// AUDIT M2 (fixed): per-subject token-bucket rate limit on POST /admin/tokens.
// Previously there was no rate limit, allowing an admin to flood the token store.
const (
	tokenIssueRate  = 1.0 // 1 token per second per subject
	tokenIssueBurst = 5   // allow short bursts up to 5
)

// Feature 27 — per-admin rate limit on POST /admin/operator-certs.
// Conservative: cert issuance is expensive (keygen + sign + PKCS#12
// encode), and the resulting artefact is a long-lived credential.
// A single admin shouldn't need more than a handful of issuances
// per hour during normal onboarding.
const (
	issueOpCertRate  = 0.1 // 1 every 10s per subject
	issueOpCertBurst = 3
)

// Feature 26 — per-admin rate limit on POST /admin/jobs/{id}/reveal-secret.
// Kept deliberately tight. Every reveal writes an audit event and gives the
// operator a plaintext value; a compromised admin token should not be able
// to dump every secret in bulk in the seconds before the operator notices.
// At 1/5s with a burst of 3, a single-subject attacker can read ≤3 secrets
// before the limiter slows them to a crawl that is easy to detect in the
// audit stream.
const (
	revealSecretRate  = 0.2 // 1 reveal per 5 seconds per subject
	revealSecretBurst = 3   // tolerate a small burst for legitimate batch use
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

// devAdminPrincipal is the synthetic Principal stamped on every
// request when Server.DisableAuth() is active. Feature 37 funnels
// every authz decision through `authz.Allow`; anonymous is denied
// by design, so dev mode needs SOME well-formed identity to keep
// the handler path identical to production.
//
// The display name is deliberately unambiguous — any audit event
// carrying this Principal in production is an alarm: it means
// DisableAuth is on in a prod binary, which is a misconfiguration.
var devAdminPrincipal = principal.User("dev-admin-disableauth", "admin")

// ── authMiddleware ────────────────────────────────────────────────────────────

// authMiddleware validates JWT Bearer tokens and injects claims into context.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// AUDIT H2 (fixed): if tokenManager is nil, refuse to serve rather
		// than silently bypass auth. Tests that need no-auth must call
		// Server.DisableAuth() explicitly.
		if s.tokenManager == nil {
			if s.disableAuth {
				// Feature 37 — DisableAuth is a dev-only mode.
				// Stamp a synthetic dev-admin Principal so the
				// unified authz evaluator sees a well-formed
				// identity and doesn't have to carry a bypass
				// branch. Anonymous would be denied by every
				// Allow rule; using a named dev-admin keeps the
				// authz path identical to production.
				//
				// This is documented as a dev-mode shortcut.
				// `Server.DisableAuth()` is feature-gated on a
				// startup flag; a production deploy that ever
				// flips it has bigger problems than this one
				// principal.
				ctx := principal.NewContext(r.Context(), devAdminPrincipal)
				next.ServeHTTP(w, r.WithContext(ctx))
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
			s.recordAuthFail(r, events.AuthFailReasonMissingToken, "") // feature 28
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
			// Feature 28 — classify the failure for the analytics
			// panel. ValidateToken's error string is not structured;
			// we substring-match the common cases and fall back to
			// invalid_signature for anything else.
			s.recordAuthFail(r, classifyAuthFailure(err), "") // feature 28
			slog.Error("token validation failed", slog.String("remote", r.RemoteAddr), slog.Any("err", err))
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}

		// Feature 28 — successful auth goes to analytics too so the
		// dashboard's auth-events panel can show logins-per-minute.
		s.recordAuthOK(r, claims.Subject)

		// Store claims in request context
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)

		// Feature 35 — resolve a typed Principal from the auth
		// material just validated. Stamped into the same context
		// alongside the legacy claims so handlers can start reading
		// Principal while downstream code (feature 36 + 37) still
		// reads claims until the migration is complete.
		//
		// If clientCertMiddleware already ran and stamped a cert
		// principal in `ctx`, that principal is preserved when the
		// ClientCertTier is active: the cert CN is strictly
		// stronger identity than the JWT alone, so we do NOT
		// overwrite it with a JWT-derived principal.
		existing := principal.FromContext(ctx)
		var p *principal.Principal
		if existing.Kind == principal.KindOperator {
			p = existing
		} else {
			p = resolvePrincipalFromClaims(claims)
		}
		// Feature 38 — populate Principal.Groups from the
		// configured group store. Without this, authz's rule 6b
		// (share-via-group) can never match: the Principal's
		// Groups list would be empty regardless of membership.
		// If no groups store is configured (dev deployments that
		// opted out) we leave Groups nil; direct `user:<id>`
		// shares still work, only `group:<name>` shares become
		// inert.
		s.populateGroups(r.Context(), p)
		ctx = principal.NewContext(ctx, p)

		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// populateGroups consults the groups store and fills p.Groups
// in place. Safe to call with a nil store (no-op) or on a
// Principal whose Kind has no stable ID (anonymous: skipped
// because group membership on anonymous is nonsensical and
// authz denies anonymous everywhere regardless).
//
// Store failures are logged at Warn and do NOT block the
// request — a coordinator that refused auth on every group
// lookup failure would be operationally fragile. The cost of
// a missed group lookup is that a group-share grantee might
// be denied an action until the next retry; the cost of a
// hard failure would be a total auth outage. Logged for
// monitoring.
func (s *Server) populateGroups(ctx context.Context, p *principal.Principal) {
	if s == nil || s.groups == nil || p == nil {
		return
	}
	if p.Kind == principal.KindAnonymous {
		return
	}
	list, err := s.groups.GroupsFor(ctx, p.ID)
	if err != nil {
		slog.Warn("groups lookup failed",
			slog.String("principal", p.ID),
			slog.Any("err", err))
		return
	}
	p.Groups = list
}

// resolvePrincipalFromClaims maps a validated *auth.Claims into a
// typed Principal. Factored out so the resolution table lives in
// one place — a future change to JWT role names or a new role lands
// here, not in the authMiddleware body.
//
// Role → Kind mapping:
//
//   role == "node"         → KindNode     (legacy node JWTs)
//   role == "job"          → KindJob      (workflow-scoped tokens)
//   anything else (admin,
//     user, empty, new)    → KindUser     (human operator via JWT)
//
// The node-role JWT path is preserved for back-compat; production
// clusters should prefer mTLS node auth (feature 23). A node JWT
// that somehow claims role=admin is still KindNode and feature 37
// will refuse admin actions on it.
func resolvePrincipalFromClaims(claims *auth.Claims) *principal.Principal {
	if claims == nil {
		return principal.Anonymous()
	}
	switch claims.Role {
	case "node":
		return principal.Node(claims.Subject)
	case "job":
		return principal.Job(claims.Subject)
	default:
		return principal.User(claims.Subject, claims.Role)
	}
}

// classifyAuthFailure maps a ValidateToken error into one of the
// stable events.AuthFailReason* constants the analytics panel
// filters on. The TokenManager doesn't expose typed errors today,
// so we substring-match — imperfect but the reasons are stable
// English strings in internal/auth/token.go.
func classifyAuthFailure(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "expired"):
		return events.AuthFailReasonExpired
	case strings.Contains(msg, "revoked"):
		return events.AuthFailReasonRevoked
	case strings.Contains(msg, "invalid") && strings.Contains(msg, "signature"):
		return events.AuthFailReasonInvalidSignature
	case strings.Contains(msg, "malformed"), strings.Contains(msg, "format"):
		return events.AuthFailReasonInvalidFormat
	default:
		return events.AuthFailReasonInvalidSignature
	}
}

// ── adminMiddleware ───────────────────────────────────────────────────────────

// adminMiddleware enforces ActionAdmin against SystemResource
// via the unified feature-37 authz evaluator.
//
// Pre-feature-37 this middleware read `claims.Role` directly.
// Feature 37 reshapes the check so every 403 flows through one
// policy point (internal/authz.Allow), emits one audit event
// type (EventAuthzDeny), and carries a machine-readable deny
// code on the response body.
//
// Must be composed inside authMiddleware so a Principal is in
// context. DisableAuth mode stamps a synthetic dev-admin
// Principal in authMiddleware; that principal satisfies
// ActionAdmin trivially, so tests that use DisableAuth keep
// working without a special bypass here.
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authzCheck(w, r, authz.ActionAdmin, authz.SystemResource()) {
			return
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

// ── issueOpCertAllow ────────────────────────────────────────────────────────

// issueOpCertAllow returns true if the caller identified by subject
// is within the feature-27 operator-cert issuance rate limit.
// Creates a per-subject limiter on first use. Same shape as
// tokenIssueAllow / revealSecretAllow, tighter rate because each
// issuance mints a long-lived credential.
func (s *Server) issueOpCertAllow(subject string) bool {
	s.issueOpCertMu.Lock()
	lim, ok := s.issueOpCertLimiters[subject]
	if !ok {
		lim = rate.NewLimiter(issueOpCertRate, issueOpCertBurst)
		s.issueOpCertLimiters[subject] = lim
	}
	s.issueOpCertMu.Unlock()
	return lim.Allow()
}

// ── revealSecretAllow ────────────────────────────────────────────────────────

// revealSecretAllow returns true if the caller identified by subject is
// within the reveal-secret rate limit. Feature 26: POST /admin/jobs/{id}/
// reveal-secret is tighter than token issuance because every call exposes
// a plaintext value — a slow limiter bounds the damage of a leaked admin
// token. Creates a per-subject limiter on first use.
func (s *Server) revealSecretAllow(subject string) bool {
	s.revealSecretMu.Lock()
	lim, ok := s.revealSecretLimiters[subject]
	if !ok {
		lim = rate.NewLimiter(revealSecretRate, revealSecretBurst)
		s.revealSecretLimiters[subject] = lim
	}
	s.revealSecretMu.Unlock()
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
