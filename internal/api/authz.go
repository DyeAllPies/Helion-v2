// internal/api/authz.go
//
// Feature 37 glue between the authz evaluator and HTTP handlers.
//
// The evaluator is pure (internal/authz package); this file is
// the handler-side adapter that turns an `*authz.DenyError` into
// an audited 403 response. Handlers call `s.authzCheck` with the
// resource they've already loaded; on deny it writes the
// audit event, the 403 body, and returns false so the handler
// bails out.

package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// ForbiddenResponse is the 403 body shape handlers return on an
// authz deny. `Error` is the stable "forbidden" literal the old
// handlers returned; `Code` is the new machine-readable deny
// code from the policy engine (feature 37). Dashboard tooling
// keys off Code; legacy consumers keep reading Error.
type ForbiddenResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// authzCheck evaluates p against the given action + resource.
// On allow: returns true, no side effects.
//
// On deny:
//   - Emits EventAuthzDeny with the deny code, action, and
//     resource context. Audit failures are logged at Error —
//     a silent authz deny without a paper trail is a security
//     regression.
//   - Writes a JSON 403 body carrying both the legacy "error"
//     literal and the typed deny Code.
//   - Returns false so the caller can `return` immediately.
//
// Handlers are expected to:
//   1. Load the resource from the store (so OwnerPrincipal is known).
//   2. Call this helper with a constructed *authz.Resource.
//   3. Return immediately if the helper returns false.
func (s *Server) authzCheck(w http.ResponseWriter, r *http.Request, action authz.Action, res *authz.Resource) bool {
	p := principal.FromContext(r.Context())
	if err := authz.Allow(p, action, res); err != nil {
		var de *authz.DenyError
		if !errors.As(err, &de) {
			// Defence in depth: authz.Allow is documented to
			// return only *DenyError or nil. An untyped error
			// here is a contract violation from below — fail
			// closed + surface.
			slog.Error("authz.Allow returned non-DenyError",
				slog.Any("err", err),
				slog.String("path", r.URL.Path))
			writeError(w, http.StatusForbidden, "forbidden")
			return false
		}

		s.emitAuthzDeny(r, de, res)

		// 403 body: the legacy shape (`error: "forbidden"`) PLUS
		// the new deny Code. Dashboards read Code; old CLIs read
		// error. Both match the "stable machine code + free-form
		// message" pattern the spec agreed on.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if err := json.NewEncoder(w).Encode(ForbiddenResponse{
			Error: "forbidden",
			Code:  de.Code,
		}); err != nil {
			slog.Warn("authzCheck: encode 403 failed", slog.Any("err", err))
		}
		return false
	}
	return true
}

// emitAuthzDeny records an audit event for the deny. Failures
// to persist the audit event log at Error — authz denials are
// security events; a coordinator that silently drops them is
// operating with an incomplete paper trail.
func (s *Server) emitAuthzDeny(r *http.Request, de *authz.DenyError, res *authz.Resource) {
	if s.audit == nil {
		return
	}
	p := principal.FromContext(r.Context())
	details := map[string]any{
		"code":          de.Code,
		"action":        string(de.Action),
		"resource_kind": string(de.ResourceKind),
		"reason":        de.Reason,
		"path":          r.URL.Path,
		"method":        r.Method,
	}
	if res != nil {
		if res.ID != "" {
			details["resource_id"] = res.ID
		}
		if res.OwnerPrincipal != "" {
			details["resource_owner"] = res.OwnerPrincipal
		}
	}
	actor := "unknown"
	if p != nil {
		actor = p.ID
		details["principal_kind"] = string(p.Kind)
	}
	if err := s.audit.Log(r.Context(), audit.EventAuthzDeny, actor, details); err != nil {
		// Security-critical: elevate to Error so monitoring
		// fires. A silent deny with no paper trail defeats
		// the purpose of the audit integration.
		logAuditErr(true, "authz.deny", err)
	}
}
