// internal/api/handlers_secretstore.go
//
// Feature 30 — admin endpoints for envelope-encryption
// operations:
//
//   POST /admin/secretstore/rotate
//     Trigger a rewrap sweep. Every persisted Job +
//     WorkflowJob with a secret envelope under a non-active
//     KEK version is re-encrypted under the active KEK and
//     rewritten. Idempotent. Blocks until the sweep completes
//     (admin-initiated; operators running long deployments
//     can fire-and-forget with the timeout of their choosing).
//
//   GET /admin/secretstore/status
//     Report the configured keyring: active version, all
//     loaded versions, whether encryption is enabled.
//
// Both routes are admin-only (ActionAdmin against
// SystemResource). A deployment without
// HELION_SECRETSTORE_KEK configured never reaches this file —
// SetSecretStoreAdmin only registers the handlers when the
// persister has a live keyring.

package api

import (
	"net/http"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// SecretStoreRotateResponse is the JSON body returned by the
// rotate endpoint. Reports the number of envelopes advanced
// to the new KEK and the number of records scanned.
type SecretStoreRotateResponse struct {
	ActiveVersion uint32   `json:"active_version"`
	LoadedVersions []uint32 `json:"loaded_versions"`
	RewrappedEnvelopes int      `json:"rewrapped_envelopes"`
	ScannedRecords     int      `json:"scanned_records"`
}

// SecretStoreStatusResponse is the read-only view of the
// configured keyring. Useful for monitoring + the dashboard's
// rotation panel.
type SecretStoreStatusResponse struct {
	Enabled        bool     `json:"enabled"`
	ActiveVersion  uint32   `json:"active_version,omitempty"`
	LoadedVersions []uint32 `json:"loaded_versions,omitempty"`
}

// handleSecretStoreRotate runs a rewrap sweep.
//
// Response codes:
//   200 — sweep completed; body reports counts.
//   503 — secretstore admin not configured (route shouldn't
//         be reachable in that case, but defensive guard).
//   500 — sweep hit a failure part-way; body reports the
//         rewrapped-so-far counts plus the error.
func (s *Server) handleSecretStoreRotate(w http.ResponseWriter, r *http.Request) {
	if s.secretAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "secretstore not configured")
		return
	}
	ring := s.secretAdmin.KeyRing()
	if ring == nil {
		writeError(w, http.StatusServiceUnavailable, "secretstore keyring not configured")
		return
	}

	rewrapped, scanned, err := s.secretAdmin.RewrapAll(r.Context())

	// Audit BEFORE returning so a mid-sweep coordinator
	// restart still leaves evidence that someone kicked off
	// rotation.
	if s.audit != nil {
		actor := principal.FromContext(r.Context()).ID
		details := map[string]any{
			"active_version":      ring.ActiveVersion(),
			"loaded_versions":     ring.Versions(),
			"rewrapped_envelopes": rewrapped,
			"scanned_records":     scanned,
		}
		if err != nil {
			details["error"] = err.Error()
		}
		if aerr := s.audit.Log(r.Context(), audit.EventSecretStoreRotate, actor, details); aerr != nil {
			// Critical security event — elevate the log.
			logAuditErr(true, "secretstore.rotate", aerr)
		}
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "rotate failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleSecretStoreRotate", SecretStoreRotateResponse{
		ActiveVersion:      ring.ActiveVersion(),
		LoadedVersions:     ring.Versions(),
		RewrappedEnvelopes: rewrapped,
		ScannedRecords:     scanned,
	})
}

// handleSecretStoreStatus returns keyring diagnostics without
// mutating anything.
func (s *Server) handleSecretStoreStatus(w http.ResponseWriter, r *http.Request) {
	resp := SecretStoreStatusResponse{}
	if s.secretAdmin != nil {
		if ring := s.secretAdmin.KeyRing(); ring != nil {
			resp.Enabled = true
			resp.ActiveVersion = ring.ActiveVersion()
			resp.LoadedVersions = ring.Versions()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleSecretStoreStatus", resp)
}
