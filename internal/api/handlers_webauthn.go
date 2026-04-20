// internal/api/handlers_webauthn.go
//
// Feature 34 — WebAuthn / FIDO2 admin endpoints.
//
// Routes (all admin-only via adminMiddleware):
//
//   POST   /admin/webauthn/register-begin
//   POST   /admin/webauthn/register-finish
//   POST   /admin/webauthn/login-begin
//   POST   /admin/webauthn/login-finish
//   GET    /admin/webauthn/credentials             (list own credentials)
//   DELETE /admin/webauthn/credentials/{id}        (revoke any cred — admin only)
//
// Begin/finish ceremony flow
// ──────────────────────────
// Operator → POST register-begin
//   server returns PublicKeyCredentialCreationOptions + opaque challenge
// Browser   → navigator.credentials.create(options) — user touches YubiKey
// Operator → POST register-finish { attestationResponse }
//   server verifies attestation + stores CredentialRecord
//
// Login is the mirror image: login-begin returns
// PublicKeyCredentialRequestOptions; login-finish verifies
// the assertion + mints a WebAuthn-backed JWT.
//
// Session state between begin and finish lives in the
// coordinator's in-memory SessionStore keyed by the
// operator's JWT subject + ceremony purpose. Each session
// is single-use (Pop removes it) with a 5-minute TTL.
//
// Admin-only but self-scoped
// ──────────────────────────
// Every endpoint runs through adminMiddleware (role == admin)
// — non-admin operators cannot register or authenticate via
// WebAuthn in this slice. That matches the rollout story:
// admins harden their own login first, then the flow is
// extended to the broader operator population in a follow-up.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	wauthn "github.com/DyeAllPies/Helion-v2/internal/webauthn"
)

// ── Request / response shapes ────────────────────────────

// WebAuthnRegisterBeginRequest is the optional JSON body for
// /admin/webauthn/register-begin. All fields are metadata
// the CredentialRecord will carry — none are part of the
// WebAuthn protocol itself.
type WebAuthnRegisterBeginRequest struct {
	Label       string `json:"label,omitempty"`
	BoundCertCN string `json:"bound_cert_cn,omitempty"`
}

// WebAuthnRegisterBeginResponse carries the client-facing
// options object verbatim as the library produces it; the
// browser's `navigator.credentials.create` expects this
// exact shape under the `publicKey` key.
type WebAuthnRegisterBeginResponse struct {
	PublicKey any `json:"publicKey"`
}

// WebAuthnCredentialItem is the list-endpoint shape.
type WebAuthnCredentialItem struct {
	CredentialID string    `json:"credential_id"` // base64url
	Label        string    `json:"label,omitempty"`
	OperatorCN   string    `json:"operator_cn"`
	BoundCertCN  string    `json:"bound_cert_cn,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
	RegisteredBy string    `json:"registered_by"`
	LastUsedAt   time.Time `json:"last_used_at,omitempty"`
	// AAGUID is the authenticator's hardware model id
	// (rendered as lowercase hex, no dashes). Useful for
	// ops tooling that wants to filter YubiKey 5C vs Apple
	// platform authenticator, etc.
	AAGUIDHex string `json:"aaguid_hex"`
}

// WebAuthnCredentialsListResponse is the /credentials list
// response body.
type WebAuthnCredentialsListResponse struct {
	Credentials []WebAuthnCredentialItem `json:"credentials"`
	Total       int                      `json:"total"`
}

// WebAuthnRevokeRequest is the optional JSON body on
// DELETE /admin/webauthn/credentials/{id}.
type WebAuthnRevokeRequest struct {
	Reason string `json:"reason"`
}

// WebAuthnLoginBeginResponse mirrors RegisterBeginResponse.
type WebAuthnLoginBeginResponse struct {
	PublicKey any `json:"publicKey"`
}

// WebAuthnLoginFinishResponse carries the minted WebAuthn-
// backed JWT + its metadata.
type WebAuthnLoginFinishResponse struct {
	Token        string `json:"token"`
	Subject      string `json:"subject"`
	Role         string `json:"role"`
	TTLSeconds   int    `json:"ttl_seconds"`
	AuthMethod   string `json:"auth_method"` // always "webauthn"
	CredentialID string `json:"credential_id"`
}

// ── Helpers ──────────────────────────────────────────────

// webauthnConfigured gates every handler; the routes aren't
// registered without a configured WebAuthn instance but we
// defence-in-depth return 503 if the handler is called with
// the dependencies nil.
func (s *Server) webauthnConfigured(w http.ResponseWriter) bool {
	if s.webauthn == nil || s.webauthnStore == nil || s.webauthnSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "webauthn not configured")
		return false
	}
	return true
}

// webauthnTTL is the lifetime of a WebAuthn-minted JWT.
// Short (15 minutes) because the hardware touch is the
// load-bearing security property; a long TTL on the minted
// token would re-open the "compromised browser can act
// without user touch" hole.
const webauthnTTL = 15 * time.Minute

// subjectFromContext returns the authenticated JWT subject
// or an empty string if absent. Admin-middleware ensures
// a subject is always present, but we guard defensively.
func subjectFromContext(ctx context.Context) string {
	claims, ok := ctx.Value(claimsContextKey).(*auth.Claims)
	if !ok || claims == nil {
		return ""
	}
	return claims.Subject
}

// auditWebAuthn emits an audit event with the given type +
// details. Centralised so every lifecycle event records
// actor = the authenticated principal consistently.
func (s *Server) auditWebAuthn(r *http.Request, event string, details map[string]any) {
	if s.audit == nil {
		return
	}
	actor := principal.FromContext(r.Context()).ID
	if err := s.audit.Log(r.Context(), event, actor, details); err != nil {
		// Security-critical — a silent failure would let a
		// compromised audit sink hide WebAuthn anomalies.
		logAuditErr(true, event, err)
	}
}

// ── register-begin ──────────────────────────────────────

func (s *Server) handleWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !s.webauthnConfigured(w) {
		return
	}
	subject := subjectFromContext(r.Context())
	if subject == "" {
		writeError(w, http.StatusUnauthorized, "subject missing from token")
		return
	}

	var req WebAuthnRegisterBeginRequest
	if r.ContentLength > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KiB
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	// Include existing credentials as exclusion list so the
	// authenticator refuses to register a duplicate key for
	// this operator (spec-recommended UX — an operator who
	// accidentally double-clicks "register" doesn't end up
	// with two separate records for the same key).
	existing, err := s.webauthnStore.ListByOperator(r.Context(), wauthn.UserHandleFor(subject))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list credentials: "+err.Error())
		return
	}
	existingCreds := make([]webauthnlib.Credential, 0, len(existing))
	for _, rec := range existing {
		existingCreds = append(existingCreds, rec.Credential)
	}
	user := wauthn.NewUser(subject, existingCreds)

	creation, session, err := s.webauthn.BeginRegistration(
		user,
		webauthnlib.WithExclusions(webauthnlib.Credentials(existingCreds).CredentialDescriptors()),
	)
	if err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnRegisterReject, map[string]any{
			"subject": subject,
			"reason":  "begin_registration: " + err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "begin registration failed")
		return
	}

	// Stash the requested metadata alongside the session so
	// register-finish can apply it without a second request
	// body.
	s.webauthnSessions.Put(subject, wauthn.PurposeRegister, *session)
	s.stashRegisterMetadata(subject, req)

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleWebAuthnRegisterBegin", WebAuthnRegisterBeginResponse{
		PublicKey: creation.Response,
	})
}

// stashRegisterMetadata is a tiny side-channel where
// register-begin drops the operator-supplied Label +
// BoundCertCN for register-finish to pick up. Keyed on
// subject + purpose; same TTL as the session store.
func (s *Server) stashRegisterMetadata(subject string, req WebAuthnRegisterBeginRequest) {
	s.webauthnRegMetaMu.Lock()
	defer s.webauthnRegMetaMu.Unlock()
	if s.webauthnRegMeta == nil {
		s.webauthnRegMeta = map[string]registerMetadata{}
	}
	s.webauthnRegMeta[subject] = registerMetadata{
		label:       strings.TrimSpace(req.Label),
		boundCertCN: strings.TrimSpace(req.BoundCertCN),
		expires:     time.Now().Add(5 * time.Minute),
	}
}

// popRegisterMetadata retrieves + removes the register-
// begin metadata for a subject.
func (s *Server) popRegisterMetadata(subject string) (registerMetadata, bool) {
	s.webauthnRegMetaMu.Lock()
	defer s.webauthnRegMetaMu.Unlock()
	meta, ok := s.webauthnRegMeta[subject]
	if ok {
		delete(s.webauthnRegMeta, subject)
	}
	if ok && time.Now().After(meta.expires) {
		return registerMetadata{}, false
	}
	return meta, ok
}

// registerMetadata is the register-begin → register-finish
// handoff.
type registerMetadata struct {
	label       string
	boundCertCN string
	expires     time.Time
}

// ── register-finish ─────────────────────────────────────

func (s *Server) handleWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !s.webauthnConfigured(w) {
		return
	}
	subject := subjectFromContext(r.Context())
	if subject == "" {
		writeError(w, http.StatusUnauthorized, "subject missing from token")
		return
	}

	session, err := s.webauthnSessions.Pop(subject, wauthn.PurposeRegister)
	if err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnRegisterReject, map[string]any{
			"subject": subject,
			"reason":  "session_expired",
		})
		writeError(w, http.StatusBadRequest, "session expired; please start registration over")
		return
	}

	user := wauthn.NewUser(subject, nil) // attestation verify doesn't need prior creds

	// The library's FinishRegistration reads the body from
	// the request directly. We cap it first so an oversized
	// payload can't OOM.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	credential, err := s.webauthn.FinishRegistration(user, session, r)
	if err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnRegisterReject, map[string]any{
			"subject": subject,
			"reason":  "finish_registration: " + err.Error(),
		})
		writeError(w, http.StatusBadRequest, "registration verification failed")
		return
	}

	meta, _ := s.popRegisterMetadata(subject)

	actor := principal.FromContext(r.Context()).ID
	rec := &wauthn.CredentialRecord{
		Credential:   *credential,
		UserHandle:   wauthn.UserHandleFor(subject),
		OperatorCN:   subject,
		Label:        meta.label,
		BoundCertCN:  meta.boundCertCN,
		RegisteredAt: time.Now().UTC(),
		RegisteredBy: actor,
	}
	if err := s.webauthnStore.Create(r.Context(), rec); err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnRegisterReject, map[string]any{
			"subject":       subject,
			"credential_id": rec.IDHex(),
			"reason":        "store_create: " + err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "store credential: "+err.Error())
		return
	}

	s.auditWebAuthn(r, audit.EventWebAuthnRegistered, map[string]any{
		"subject":        subject,
		"credential_id":  rec.IDHex(),
		"label":          rec.Label,
		"bound_cert_cn":  rec.BoundCertCN,
		"registered_by":  rec.RegisteredBy,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleWebAuthnRegisterFinish", map[string]any{
		"credential_id": rec.IDHex(),
		"label":         rec.Label,
	})
}

// ── login-begin ─────────────────────────────────────────

func (s *Server) handleWebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.webauthnConfigured(w) {
		return
	}
	subject := subjectFromContext(r.Context())
	if subject == "" {
		writeError(w, http.StatusUnauthorized, "subject missing from token")
		return
	}

	existing, err := s.webauthnStore.ListByOperator(r.Context(), wauthn.UserHandleFor(subject))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list credentials: "+err.Error())
		return
	}
	if len(existing) == 0 {
		// Deliberately a distinct error from "session
		// expired" — the dashboard surfaces this as
		// "register a YubiKey first".
		writeError(w, http.StatusBadRequest, "no WebAuthn credentials registered for this operator")
		return
	}
	existingCreds := make([]webauthnlib.Credential, 0, len(existing))
	for _, rec := range existing {
		existingCreds = append(existingCreds, rec.Credential)
	}
	user := wauthn.NewUser(subject, existingCreds)

	assertion, session, err := s.webauthn.BeginLogin(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin login: "+err.Error())
		return
	}
	s.webauthnSessions.Put(subject, wauthn.PurposeLogin, *session)

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleWebAuthnLoginBegin", WebAuthnLoginBeginResponse{
		PublicKey: assertion.Response,
	})
}

// ── login-finish ────────────────────────────────────────

func (s *Server) handleWebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.webauthnConfigured(w) {
		return
	}
	subject := subjectFromContext(r.Context())
	if subject == "" {
		writeError(w, http.StatusUnauthorized, "subject missing from token")
		return
	}

	session, err := s.webauthnSessions.Pop(subject, wauthn.PurposeLogin)
	if err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnLoginReject, map[string]any{
			"subject": subject,
			"reason":  "session_expired",
		})
		writeError(w, http.StatusBadRequest, "session expired; please start login over")
		return
	}

	// Read body once so we can use it with both the library
	// finish-login call AND a fallback error path.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	existing, err := s.webauthnStore.ListByOperator(r.Context(), wauthn.UserHandleFor(subject))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list credentials: "+err.Error())
		return
	}
	existingCreds := make([]webauthnlib.Credential, 0, len(existing))
	for _, rec := range existing {
		existingCreds = append(existingCreds, rec.Credential)
	}
	user := wauthn.NewUser(subject, existingCreds)

	// Reconstruct the request body so FinishLogin can read it.
	finishReq := r.Clone(r.Context())
	finishReq.Body = io.NopCloser(bytes.NewReader(body))

	credential, err := s.webauthn.FinishLogin(user, session, finishReq)
	if err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnLoginReject, map[string]any{
			"subject": subject,
			"reason":  "finish_login: " + err.Error(),
		})
		writeError(w, http.StatusUnauthorized, "assertion verification failed")
		return
	}

	// Advance the sign counter + update LastUsedAt.
	if err := s.webauthnStore.UpdateSignCount(r.Context(), credential.ID, credential.Authenticator.SignCount); err != nil {
		s.auditWebAuthn(r, audit.EventWebAuthnLoginReject, map[string]any{
			"subject":       subject,
			"credential_id": wauthn.EncodeCredentialID(credential.ID),
			"reason":        "sign_count: " + err.Error(),
		})
		writeError(w, http.StatusUnauthorized, "replay detected: "+err.Error())
		return
	}

	// Look up the stored record so we can re-export the
	// BoundCertCN binding on the minted JWT (pairs with
	// feature 33 enforcement).
	stored, err := s.webauthnStore.Get(r.Context(), credential.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load credential: "+err.Error())
		return
	}

	// Read existing claims to copy Role over — the minted
	// WebAuthn JWT inherits the caller's role. If the
	// original token didn't have one, default to "user".
	role := "user"
	if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
		role = claims.Role
	}

	token, err := s.tokenManager.GenerateTokenWithClaims(r.Context(), auth.TokenClaims{
		Subject:    subject,
		Role:       role,
		RequiredCN: stored.BoundCertCN,
		AuthMethod: "webauthn",
		TTL:        webauthnTTL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mint token: "+err.Error())
		return
	}

	// Parse the minted token to pick up its JTI for audit.
	mintedClaims, _ := s.tokenManager.ValidateToken(r.Context(), token)
	jti := ""
	if mintedClaims != nil {
		jti = mintedClaims.ID
	}

	s.auditWebAuthn(r, audit.EventWebAuthnAuthenticated, map[string]any{
		"subject":       subject,
		"credential_id": wauthn.EncodeCredentialID(credential.ID),
		"jti":           jti,
	})

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleWebAuthnLoginFinish", WebAuthnLoginFinishResponse{
		Token:        token,
		Subject:      subject,
		Role:         role,
		TTLSeconds:   int(webauthnTTL.Seconds()),
		AuthMethod:   "webauthn",
		CredentialID: wauthn.EncodeCredentialID(credential.ID),
	})
}

// ── list credentials ────────────────────────────────────

func (s *Server) handleWebAuthnListCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.webauthnConfigured(w) {
		return
	}
	// Admins may list everyone's creds (they're already
	// admin-gated by adminMiddleware). Non-admin handling is
	// not in scope for this slice — adminMiddleware would
	// have 403'd them.
	all, err := s.webauthnStore.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list: "+err.Error())
		return
	}

	resp := WebAuthnCredentialsListResponse{
		Credentials: make([]WebAuthnCredentialItem, 0, len(all)),
		Total:       len(all),
	}
	for _, rec := range all {
		resp.Credentials = append(resp.Credentials, WebAuthnCredentialItem{
			CredentialID: rec.IDHex(),
			Label:        rec.Label,
			OperatorCN:   rec.OperatorCN,
			BoundCertCN:  rec.BoundCertCN,
			RegisteredAt: rec.RegisteredAt,
			RegisteredBy: rec.RegisteredBy,
			LastUsedAt:   rec.LastUsedAt,
			AAGUIDHex:    fmt.Sprintf("%x", rec.Credential.Authenticator.AAGUID),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleWebAuthnListCredentials", resp)
}

// ── revoke credential ──────────────────────────────────

func (s *Server) handleWebAuthnRevokeCredential(w http.ResponseWriter, r *http.Request) {
	if !s.webauthnConfigured(w) {
		return
	}
	idStr := r.PathValue("id")
	credID, err := wauthn.DecodeCredentialID(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id: "+err.Error())
		return
	}

	var req WebAuthnRevokeRequest
	if r.ContentLength > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	existing, err := s.webauthnStore.Get(r.Context(), credID)
	if err != nil {
		if errors.Is(err, wauthn.ErrCredentialNotFound) {
			writeError(w, http.StatusNotFound, "credential not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get: "+err.Error())
		return
	}
	if err := s.webauthnStore.Delete(r.Context(), credID); err != nil {
		writeError(w, http.StatusInternalServerError, "delete: "+err.Error())
		return
	}

	s.auditWebAuthn(r, audit.EventWebAuthnRevoked, map[string]any{
		"credential_id": existing.IDHex(),
		"operator_cn":   existing.OperatorCN,
		"revoked_by":    principal.FromContext(r.Context()).ID,
		"reason":        strings.TrimSpace(req.Reason),
	})

	w.WriteHeader(http.StatusNoContent)
}

// silence unused-import detector when partial file is
// commented out during refactors.
var _ = slog.Default
