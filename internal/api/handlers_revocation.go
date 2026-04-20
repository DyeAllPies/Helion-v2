// internal/api/handlers_revocation.go
//
// Feature 31 — operator-cert revocation endpoints.
//
// Routes
// ──────
//   POST /admin/operator-certs/{serial}/revoke
//     body: {"reason": "<free-form>"}
//     Idempotent. Admin-only. Emits EventOperatorCertRevoked.
//
//   GET /admin/operator-certs/revocations
//     Returns every revocation record, newest first. Admin-only.
//
//   GET /admin/ca/crl
//     Returns a PEM-encoded CRL signed by the CA, listing every
//     revoked serial. Admin-only. Consumers: `ssl_crl` in
//     Nginx, operators running `openssl crl -in ... -text`.
//
// Idempotency
// ───────────
// Re-revoking the same serial returns 200 with the existing
// record, NOT a fresh record under the new reason. The
// audit event still fires, but the `idempotent` detail is
// `true` so reviewers can distinguish "genuine revocation"
// from "panicked operator double-clicked the button".

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// ── Request / response shapes ─────────────────────────────

// RevokeOperatorCertRequest is the JSON body for
// POST /admin/operator-certs/{serial}/revoke.
type RevokeOperatorCertRequest struct {
	// Reason is a free-form operator justification. Required
	// non-empty — a blank reason defeats the audit story
	// ("why was this cert revoked?"). Trimmed + length-
	// capped at the store layer.
	Reason string `json:"reason"`

	// CommonName is optional. When supplied, stored in the
	// revocation record for audit context. Operators typically
	// don't know the CN offhand, so we accept and store
	// whatever the endpoint sees (or "" if absent).
	CommonName string `json:"common_name,omitempty"`
}

// RevokeOperatorCertResponse echoes the stored revocation
// record back plus an `idempotent` flag that's true when the
// serial was already revoked.
type RevokeOperatorCertResponse struct {
	SerialHex  string    `json:"serial_hex"`
	CommonName string    `json:"common_name,omitempty"`
	RevokedAt  time.Time `json:"revoked_at"`
	RevokedBy  string    `json:"revoked_by"`
	Reason     string    `json:"reason,omitempty"`
	Idempotent bool      `json:"idempotent"`
}

// RevocationListResponse is the JSON body for the
// /admin/operator-certs/revocations read endpoint.
type RevocationListResponse struct {
	Revocations []RevocationItem `json:"revocations"`
	Total       int              `json:"total"`
}

// RevocationItem is a single entry on the list response.
// Decouples the wire shape from pqcrypto.RevocationRecord so
// an internal storage-format tweak doesn't leak into the API
// contract.
type RevocationItem struct {
	SerialHex  string    `json:"serial_hex"`
	CommonName string    `json:"common_name,omitempty"`
	RevokedAt  time.Time `json:"revoked_at"`
	RevokedBy  string    `json:"revoked_by"`
	Reason     string    `json:"reason,omitempty"`
}

// ── Helpers ───────────────────────────────────────────────

// revocationsConfigured short-circuits with 503 when the
// revocation store isn't wired. Matches the pattern used by
// other optional admin surfaces. Shouldn't trigger in
// practice because SetRevocationStore only registers the
// routes when non-nil, but defensive guard against a future
// refactor that forgets the nil check.
func (s *Server) revocationsConfigured(w http.ResponseWriter) bool {
	if s.revocationStore == nil {
		writeError(w, http.StatusServiceUnavailable, "revocation store not configured")
		return false
	}
	return true
}

// ── POST revoke ───────────────────────────────────────────

func (s *Server) handleRevokeOperatorCert(w http.ResponseWriter, r *http.Request) {
	if !s.revocationsConfigured(w) {
		return
	}
	serialRaw := r.PathValue("serial")
	norm, err := pqcrypto.NormalizeSerialHex(serialRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid serial: "+err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var req RevokeOperatorCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}

	actor := principal.FromContext(r.Context()).ID

	rec, isNew, err := s.revocationStore.Revoke(r.Context(), pqcrypto.RevocationRecord{
		SerialHex:  norm,
		CommonName: req.CommonName,
		RevokedBy:  actor,
		Reason:     req.Reason,
	})
	if err != nil {
		if errors.Is(err, pqcrypto.ErrInvalidSerial) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "revoke failed: "+err.Error())
		return
	}

	// Audit BEFORE response: a downed audit sink fails the
	// reveal-style "no accountability, no action" contract
	// consistently across feature-31 and feature-26 paths.
	if s.audit != nil {
		details := map[string]any{
			"serial_hex":  rec.SerialHex,
			"revoked_by":  rec.RevokedBy,
			"idempotent":  !isNew,
		}
		if rec.CommonName != "" {
			details["common_name"] = rec.CommonName
		}
		if rec.Reason != "" {
			details["reason"] = rec.Reason
		}
		if aerr := s.audit.Log(r.Context(), audit.EventOperatorCertRevoked, actor, details); aerr != nil {
			// Security-critical — elevate via the critical
			// audit-error path. A revocation without a paper
			// trail is worse than a refused request.
			logAuditErr(true, "operator_cert_revoked", aerr)
			writeError(w, http.StatusInternalServerError, "audit persist failed; revocation not surfaced")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
	}
	w.WriteHeader(status)
	writeJSON(w, "handleRevokeOperatorCert", RevokeOperatorCertResponse{
		SerialHex:  rec.SerialHex,
		CommonName: rec.CommonName,
		RevokedAt:  rec.RevokedAt,
		RevokedBy:  rec.RevokedBy,
		Reason:     rec.Reason,
		Idempotent: !isNew,
	})
}

// ── GET revocations list ──────────────────────────────────

func (s *Server) handleListRevocations(w http.ResponseWriter, r *http.Request) {
	if !s.revocationsConfigured(w) {
		return
	}
	list, err := s.revocationStore.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: "+err.Error())
		return
	}
	resp := RevocationListResponse{
		Revocations: make([]RevocationItem, len(list)),
		Total:       len(list),
	}
	for i, rec := range list {
		resp.Revocations[i] = RevocationItem{
			SerialHex:  rec.SerialHex,
			CommonName: rec.CommonName,
			RevokedAt:  rec.RevokedAt,
			RevokedBy:  rec.RevokedBy,
			Reason:     rec.Reason,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListRevocations", resp)
}

// ── GET CRL ───────────────────────────────────────────────

// handleGetCRL returns a PEM-encoded CRL signed by the CA.
// NextUpdate window defaults to 24h; consumers are expected
// to refetch within that period. Unauthenticated fetches are
// refused — the CRL reveals which serials are revoked (and
// by recency implicitly when), which is information worth
// protecting from an attacker enumerating issued certs.
// Admin-only at the route level.
func (s *Server) handleGetCRL(w http.ResponseWriter, r *http.Request) {
	if !s.revocationsConfigured(w) {
		return
	}
	if s.crlSigner == nil {
		writeError(w, http.StatusServiceUnavailable, "CRL signing not configured")
		return
	}

	list, err := s.revocationStore.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: "+err.Error())
		return
	}
	pemBytes, err := s.crlSigner.CreateCRLPEM(list, time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CRL sign failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="helion-ca.crl"`)
	_, _ = w.Write(pemBytes)
}
