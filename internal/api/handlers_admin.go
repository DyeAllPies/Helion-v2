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
	_ = json.NewEncoder(w).Encode(resp)
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
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
	_ = json.NewEncoder(w).Encode(IssueTokenResponse{
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
	_ = json.NewEncoder(w).Encode(RevokeTokenResponse{Revoked: true, JTI: jti})
}
