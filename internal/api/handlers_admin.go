// internal/api/handlers_admin.go
//
// Admin-only handlers (all require authMiddleware + adminMiddleware):
//   POST   /admin/tokens          — issue a scoped JWT
//   DELETE /admin/tokens/{jti}    — revoke a token immediately
//   POST   /admin/nodes/{id}/revoke — force-revoke a node

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// handleRevokeNode handles POST /admin/nodes/{id}/revoke.
func (s *Server) handleRevokeNode(w http.ResponseWriter, r *http.Request) {
	if s.nodes == nil {
		writeError(w, http.StatusNotImplemented, "node registry not configured")
		return
	}

	nodeID := r.PathValue("id")
	if nodeID == "" {
		nodeID = strings.TrimPrefix(r.URL.Path, "/admin/nodes/")
		nodeID = strings.TrimSuffix(nodeID, "/revoke")
	}

	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "node id required")
		return
	}

	var req RevokeNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Reason is optional, use default
		req.Reason = "manual revocation"
	}

	// Get actor from claims (if auth is enabled)
	actor := "system"
	if s.tokenManager != nil {
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			actor = claims.Subject
		}
	}

	// Revoke the node
	if err := s.nodes.RevokeNode(r.Context(), nodeID, req.Reason); err != nil {
		slog.Error("revoke node failed", slog.String("node_id", nodeID), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Log revocation (if audit is enabled)
	if s.audit != nil {
		if err := s.audit.LogNodeRevoke(r.Context(), actor, nodeID, req.Reason); err != nil {
			logAuditErr(true, "node.revoke", err)
		}
	}

	resp := RevokeNodeResponse{
		Success: true,
		Message: fmt.Sprintf("node %s revoked", nodeID),
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleRevokeNode", resp)
}

// handleIssueToken handles POST /admin/tokens.
// Issues a short-lived scoped JWT for the given subject and role.
func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	if s.tokenManager == nil {
		writeError(w, http.StatusNotImplemented, "token manager not configured")
		return
	}

	// Per-admin rate limit: at most tokenIssueRate issuances per second per subject.
	if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
		if !s.tokenIssueAllow(claims.Subject) {
			writeError(w, http.StatusTooManyRequests, "token issuance rate limit exceeded")
			return
		}
	}

	var req IssueTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Debug("invalid JSON in token issue request", slog.Any("err", err))
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Subject == "" {
		writeError(w, http.StatusBadRequest, "subject is required")
		return
	}
	if !validRoles[req.Role] {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'node'")
		return
	}

	ttl := req.TTLHours
	if ttl <= 0 {
		ttl = defaultTokenTTLHours
	}
	if ttl > maxTokenTTLHours {
		writeError(w, http.StatusBadRequest, "ttl_hours must not exceed 720 (30 days)")
		return
	}

	token, err := s.tokenManager.GenerateToken(r.Context(), req.Subject, req.Role, time.Duration(ttl)*time.Hour)
	if err != nil {
		slog.Error("issue token failed", slog.String("subject", req.Subject), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}

	if s.audit != nil {
		actor := "system"
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			actor = claims.Subject
		}
		if err := s.audit.Log(r.Context(), "token.issued", actor, map[string]interface{}{
			"subject":   req.Subject,
			"role":      req.Role,
			"ttl_hours": ttl,
		}); err != nil {
			logAuditErr(true, "token.issue", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleIssueToken", IssueTokenResponse{
		Token:    token,
		Subject:  req.Subject,
		Role:     req.Role,
		TTLHours: ttl,
	})
}

// handleRevokeToken handles DELETE /admin/tokens/{jti}.
// Immediately invalidates a token by deleting its JTI from BadgerDB.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if s.tokenManager == nil {
		writeError(w, http.StatusNotImplemented, "token manager not configured")
		return
	}

	jti := r.PathValue("jti")
	if jti == "" {
		jti = strings.TrimPrefix(r.URL.Path, "/admin/tokens/")
	}
	if jti == "" {
		writeError(w, http.StatusBadRequest, "jti is required")
		return
	}

	if err := s.tokenManager.RevokeToken(r.Context(), jti); err != nil {
		slog.Error("revoke token failed", slog.String("jti", jti), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "revocation failed")
		return
	}

	if s.audit != nil {
		actor := "system"
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			actor = claims.Subject
		}
		if err := s.audit.Log(r.Context(), "token.revoked", actor, map[string]interface{}{
			"jti":    jti,
			"reason": "explicit revocation",
		}); err != nil {
			logAuditErr(true, "token.revoke", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleRevokeToken", RevokeTokenResponse{Revoked: true, JTI: jti})
}

// handleRevealSecret handles POST /admin/jobs/{id}/reveal-secret.
//
// Feature 26 deferred-item-rolled-in: operator-facing "show me the
// secret" action. Admin-only; every success AND every reject is
// audited; per-subject rate-limited; body requires a non-empty
// `reason` field that goes into the audit record.
//
// Safety properties (see SECURITY.md §9.4):
//
//   - Admin role only (adminMiddleware runs before this handler).
//   - Rate-limited per subject at 1 reveal / 5 s (burst 3). A
//     compromised admin token cannot dump bulk secrets quickly.
//   - Key must be on the job's declared SecretKeys list. Reading
//     a non-secret env value is refused because that endpoint is
//     not a generic env-dump — the user can use GET /jobs/{id}
//     for non-secret values.
//   - Reason field is mandatory and non-empty. Embedded in audit
//     detail so post-incident review can tell intentional debugging
//     apart from enumeration.
//   - Audit event is written BEFORE the plaintext appears in the
//     response body. A downed audit sink yields 500 rather than
//     leaking the value with no record.
func (s *Server) handleRevealSecret(w http.ResponseWriter, r *http.Request) {
	// Cap body — the request is tiny (key + reason), no reason to
	// accept anywhere near maxSubmitBodyBytes.
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)

	// Resolve actor FIRST so every subsequent reject path can audit
	// with a real subject. adminMiddleware guarantees a non-nil
	// tokenManager and a claims-bearing context; we defensively
	// fall back to "unknown" only if somehow those aren't present
	// (a test misconfiguration, not a real request).
	actor := "unknown"
	if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
		actor = claims.Subject
	}

	// Rate limit per subject. Rejections here are NOT audited
	// individually — 429 floods would fill the audit log with noise.
	// The initial reveals that escaped the limiter and the
	// subsequent 429s are traceable via the per-subject limiter
	// observations in standard rate-limit metrics instead.
	if !s.revealSecretAllow(actor) {
		writeError(w, http.StatusTooManyRequests, "reveal-secret rate limit exceeded")
		return
	}

	jobID := r.PathValue("id")
	if jobID == "" {
		s.auditRevealReject(r, actor, "", "", "missing job id in path")
		writeError(w, http.StatusBadRequest, "job id required")
		return
	}

	var req RevealSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditRevealReject(r, actor, jobID, "", "invalid request body")
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}
	if msg := validateRevealSecretRequest(&req); msg != "" {
		s.auditRevealReject(r, actor, jobID, req.Key, msg)
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	job, err := s.jobs.Get(jobID)
	if err != nil || job == nil {
		s.auditRevealReject(r, actor, jobID, req.Key, "job not found")
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// The key must be on this job's declared SecretKeys list.
	// Otherwise the endpoint becomes a generic env reader, which
	// bypasses the feature-24 redaction on GET /jobs/{id}. A legit
	// operator who needs a non-secret value already has GET.
	if !containsSecretKey(job.SecretKeys, req.Key) {
		s.auditRevealReject(r, actor, jobID, req.Key, "key not declared secret on this job")
		writeError(w, http.StatusNotFound, "no such secret key on this job")
		return
	}

	value, present := job.Env[req.Key]
	if !present {
		// SecretKeys named a key that isn't in Env — the submit
		// validator rejects this case, so reaching here means a
		// stored record was mutated by something other than submit
		// (manual BadgerDB edit, test fixture). Fail closed.
		s.auditRevealReject(r, actor, jobID, req.Key, "declared secret but missing from env map")
		writeError(w, http.StatusInternalServerError, "secret declared but not present")
		return
	}

	now := time.Now().UTC()
	// Audit FIRST, then respond. If the audit sink is down, we
	// fail the reveal closed so no plaintext leaves the coordinator
	// without a durable record of who asked and why.
	if s.audit != nil {
		if err := s.audit.Log(r.Context(), audit.EventSecretRevealed, actor, map[string]interface{}{
			"job_id":      jobID,
			"key":         req.Key,
			"reason":      strings.TrimSpace(req.Reason),
			"revealed_at": now.Format(time.RFC3339Nano),
		}); err != nil {
			// Critical: the whole point of this endpoint is its audit trail.
			logAuditErr(true, "secret.revealed", err)
			writeError(w, http.StatusInternalServerError, "audit log unavailable; reveal refused")
			return
		}
	}

	resp := RevealSecretResponse{
		JobID:       jobID,
		Key:         req.Key,
		Value:       value,
		RevealedAt:  now.Format(time.RFC3339Nano),
		RevealedBy:  actor,
		AuditNotice: "This reveal was recorded in the audit log (event type: secret_revealed). Reason recorded: " + strings.TrimSpace(req.Reason),
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleRevealSecret", resp)
}

// auditRevealReject records a failed reveal-secret attempt. Feature
// 26: every reject is audited so an attacker probing for which keys
// are flagged secret on which jobs shows up in the audit stream.
// Called from every reject branch in handleRevealSecret.
func (s *Server) auditRevealReject(r *http.Request, actor, jobID, key, reason string) {
	if s.audit == nil {
		return
	}
	details := map[string]interface{}{
		"reason": reason,
	}
	if jobID != "" {
		details["job_id"] = jobID
	}
	if key != "" {
		details["key"] = key
	}
	if err := s.audit.Log(r.Context(), audit.EventSecretRevealReject, actor, details); err != nil {
		logAuditErr(false, "secret.reveal_reject", err)
	}
}
