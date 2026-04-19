// internal/api/handlers_shares.go
//
// Feature 38 — owner-or-admin endpoints for per-resource share
// CRUD. The caller does not have to be an admin; they have to be
// the owner of the resource. The `/admin` URL prefix is a
// naming convention, not a policy gate — adminMiddleware is
// NOT applied here. Instead each handler loads the resource,
// runs a custom authz check (owner OR admin), and then mutates.
//
// Route map
// ─────────
//   POST   /admin/resources/{kind}/share?id=<id>
//     body: {grantee, actions}
//   GET    /admin/resources/{kind}/shares?id=<id>
//   DELETE /admin/resources/{kind}/share?id=<id>&grantee=<id>
//
// The resource id rides in a query parameter rather than the
// path because dataset + model resource ids contain "/"
// (name/version) and Go's servemux {id} pathvalue doesn't
// accept multi-segment names without a catch-all. Query
// parameters sidestep the encoding entirely.
//
// Mutation semantics
// ──────────────────
//   - Create is idempotent. Re-posting the same (grantee,
//     actions) tuple returns 200 with the existing record.
//     If a Share for the same grantee exists with different
//     actions, the NEW actions replace it (last-writer-wins;
//     simpler + matches feature spec contract).
//   - Revoke is idempotent. Removing an absent share returns
//     200.
//   - Cap of MaxSharesPerResource enforced on create.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

// ShareRequest is the JSON body for POST /admin/resources/.../share.
type ShareRequest struct {
	Grantee string          `json:"grantee"`
	Actions []authz.Action  `json:"actions"`
}

// ShareResponse is a single share on the wire. Mirrors
// authz.Share.
type ShareResponse struct {
	Grantee   string         `json:"grantee"`
	Actions   []authz.Action `json:"actions"`
	GrantedBy string         `json:"granted_by"`
	GrantedAt time.Time      `json:"granted_at"`
}

// ShareListResponse is the body for GET shares.
type ShareListResponse struct {
	Shares []ShareResponse `json:"shares"`
	Total  int             `json:"total"`
}

func shareToResponse(s authz.Share) ShareResponse {
	return ShareResponse{
		Grantee:   s.Grantee,
		Actions:   s.Actions,
		GrantedBy: s.GrantedBy,
		GrantedAt: s.GrantedAt,
	}
}

// ── Common plumbing ────────────────────────────────────────

// resourceLoader captures the per-resource-kind read + mutate
// plumbing the share endpoints need. The map below hosts one
// entry per supported ResourceKind.
type resourceLoader struct {
	// load reads the resource and returns
	// (ownerPrincipal, currentShares, err). err is wrapped in
	// one of: registry.ErrNotFound, cluster.ErrJobNotFound,
	// cluster.ErrWorkflowNotFound.
	load func(s *Server, r *http.Request, id string) (owner string, shares []authz.Share, err error)
	// save persists the new share list. Called only AFTER
	// validation + authz pass.
	save func(s *Server, r *http.Request, id string, shares []authz.Share) error
	// notFound is the sentinel the handler compares against
	// for 404 mapping.
	notFound error
}

func shareLoaderFor(kind string) (*resourceLoader, bool) {
	switch kind {
	case "job":
		return &jobShareLoader, true
	case "workflow":
		return &workflowShareLoader, true
	case "dataset":
		return &datasetShareLoader, true
	case "model":
		return &modelShareLoader, true
	default:
		return nil, false
	}
}

var jobShareLoader = resourceLoader{
	load: func(s *Server, _ *http.Request, id string) (string, []authz.Share, error) {
		j, err := s.jobs.Get(id)
		if err != nil {
			return "", nil, err
		}
		return j.OwnerPrincipal, j.Shares, nil
	},
	save: func(s *Server, r *http.Request, id string, shares []authz.Share) error {
		return s.saveJobShares(r, id, shares)
	},
	notFound: cluster.ErrJobNotFound,
}

var workflowShareLoader = resourceLoader{
	load: func(s *Server, _ *http.Request, id string) (string, []authz.Share, error) {
		if s.workflowStore == nil {
			return "", nil, cluster.ErrWorkflowNotFound
		}
		wf, err := s.workflowStore.Get(id)
		if err != nil {
			return "", nil, err
		}
		return wf.OwnerPrincipal, wf.Shares, nil
	},
	save: func(s *Server, r *http.Request, id string, shares []authz.Share) error {
		return s.saveWorkflowShares(r, id, shares)
	},
	notFound: cluster.ErrWorkflowNotFound,
}

var datasetShareLoader = resourceLoader{
	load: func(s *Server, _ *http.Request, id string) (string, []authz.Share, error) {
		if s.datasets == nil {
			return "", nil, registry.ErrNotFound
		}
		name, version, err := splitRegistryID(id)
		if err != nil {
			return "", nil, err
		}
		d, err := s.datasets.GetDataset(name, version)
		if err != nil {
			return "", nil, err
		}
		return d.OwnerPrincipal, d.Shares, nil
	},
	save: func(s *Server, r *http.Request, id string, shares []authz.Share) error {
		return s.saveDatasetShares(r, id, shares)
	},
	notFound: registry.ErrNotFound,
}

var modelShareLoader = resourceLoader{
	load: func(s *Server, _ *http.Request, id string) (string, []authz.Share, error) {
		if s.models == nil {
			return "", nil, registry.ErrNotFound
		}
		name, version, err := splitRegistryID(id)
		if err != nil {
			return "", nil, err
		}
		m, err := s.models.GetModel(name, version)
		if err != nil {
			return "", nil, err
		}
		return m.OwnerPrincipal, m.Shares, nil
	},
	save: func(s *Server, r *http.Request, id string, shares []authz.Share) error {
		return s.saveModelShares(r, id, shares)
	},
	notFound: registry.ErrNotFound,
}

// splitRegistryID splits "name/version" into its parts.
// Returns registry.ErrNotFound on a malformed id so the
// handler responds 404 rather than leaking the parse error.
func splitRegistryID(id string) (string, string, error) {
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			if i == 0 || i == len(id)-1 {
				return "", "", registry.ErrNotFound
			}
			return id[:i], id[i+1:], nil
		}
	}
	return "", "", registry.ErrNotFound
}

// isOwnerOrAdmin encapsulates the share-mutation policy:
// authz.Allow doesn't have a precise fit (the owner can share
// their own resource, but that's not ActionAdmin), so we
// compose two checks. No share-mutation endpoint escapes this
// helper.
func (s *Server) isOwnerOrAdmin(r *http.Request, owner string) bool {
	p := principal.FromContext(r.Context())
	if p.IsAdmin() {
		return true
	}
	// Nil owner or empty (shouldn't happen on non-system
	// resources; feature 36 guarantees a stamp) denies
	// non-admins.
	if owner == "" {
		return false
	}
	return p.ID == owner
}

// resourceIDParam returns the `id` query parameter or a 400
// with a stable error body.
func resourceIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing required 'id' query parameter")
		return "", false
	}
	return id, true
}

// ── handlers ──────────────────────────────────────────────

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	kind := r.PathValue("kind")
	loader, ok := shareLoaderFor(kind)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown resource kind %q", kind))
		return
	}
	id, ok := resourceIDParam(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var req ShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	newShare := authz.Share{
		Grantee:   req.Grantee,
		Actions:   req.Actions,
		GrantedBy: principal.FromContext(r.Context()).ID,
		GrantedAt: time.Now().UTC(),
	}
	if err := authz.ValidateShare(newShare); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	owner, existing, err := loader.load(s, r, id)
	if err != nil {
		if errors.Is(err, loader.notFound) {
			writeError(w, http.StatusNotFound, kind+" not found")
			return
		}
		slog.Error("share: load failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if !s.isOwnerOrAdmin(r, owner) {
		// Emit an authz_deny event so the share-escalation
		// attempt shows up in the analytics panel like any
		// other 403.
		s.emitAuthzDeny(r, &authz.DenyError{
			Code:         authz.DenyCodeNotOwner,
			Reason:       "share mutation requires owner or admin",
			Action:       authz.ActionWrite,
			Kind:         string(principal.FromContext(r.Context()).Kind),
			ResourceKind: authz.ResourceKind(kind),
		}, &authz.Resource{Kind: authz.ResourceKind(kind), ID: id, OwnerPrincipal: owner})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, "handleCreateShare.forbidden", ForbiddenResponse{
			Error: "forbidden", Code: authz.DenyCodeNotOwner,
		})
		return
	}

	merged := mergeShares(existing, newShare)
	if len(merged) > authz.MaxSharesPerResource {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"share list would exceed cap of %d per resource — use a group grantee instead",
			authz.MaxSharesPerResource))
		return
	}

	if err := loader.save(s, r, id, merged); err != nil {
		slog.Error("share: save failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "share persist failed")
		return
	}

	s.auditGroup(r, audit.EventResourceShared, map[string]any{
		"resource_kind": kind,
		"resource_id":   id,
		"grantee":       newShare.Grantee,
		"actions":       actionsToStrings(newShare.Actions),
		"granted_by":    newShare.GrantedBy,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleCreateShare", shareToResponse(newShare))
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	kind := r.PathValue("kind")
	loader, ok := shareLoaderFor(kind)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown resource kind %q", kind))
		return
	}
	id, ok := resourceIDParam(w, r)
	if !ok {
		return
	}
	owner, shares, err := loader.load(s, r, id)
	if err != nil {
		if errors.Is(err, loader.notFound) {
			writeError(w, http.StatusNotFound, kind+" not found")
			return
		}
		slog.Error("share list: load failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Listing shares is ActionRead on the resource: owner,
	// admin, AND any existing grantee with ActionRead can see
	// who else has access. Reuse the authz engine instead of
	// a bespoke owner check.
	if !s.authzCheck(w, r, authz.ActionRead,
		&authz.Resource{
			Kind:           authz.ResourceKind(kind),
			ID:             id,
			OwnerPrincipal: owner,
			Shares:         shares,
		}) {
		return
	}
	resp := ShareListResponse{
		Shares: make([]ShareResponse, len(shares)),
		Total:  len(shares),
	}
	for i, sh := range shares {
		resp.Shares[i] = shareToResponse(sh)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListShares", resp)
}

func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	kind := r.PathValue("kind")
	loader, ok := shareLoaderFor(kind)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown resource kind %q", kind))
		return
	}
	id, ok := resourceIDParam(w, r)
	if !ok {
		return
	}
	grantee := r.URL.Query().Get("grantee")
	if grantee == "" {
		writeError(w, http.StatusBadRequest, "missing required 'grantee' query parameter")
		return
	}

	owner, existing, err := loader.load(s, r, id)
	if err != nil {
		if errors.Is(err, loader.notFound) {
			writeError(w, http.StatusNotFound, kind+" not found")
			return
		}
		slog.Error("share revoke: load failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !s.isOwnerOrAdmin(r, owner) {
		s.emitAuthzDeny(r, &authz.DenyError{
			Code:         authz.DenyCodeNotOwner,
			Reason:       "share revoke requires owner or admin",
			Action:       authz.ActionWrite,
			Kind:         string(principal.FromContext(r.Context()).Kind),
			ResourceKind: authz.ResourceKind(kind),
		}, &authz.Resource{Kind: authz.ResourceKind(kind), ID: id, OwnerPrincipal: owner})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, "handleRevokeShare.forbidden", ForbiddenResponse{
			Error: "forbidden", Code: authz.DenyCodeNotOwner,
		})
		return
	}

	trimmed := make([]authz.Share, 0, len(existing))
	for _, sh := range existing {
		if sh.Grantee == grantee {
			continue
		}
		trimmed = append(trimmed, sh)
	}
	if err := loader.save(s, r, id, trimmed); err != nil {
		slog.Error("share revoke: save failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "share revoke failed")
		return
	}
	s.auditGroup(r, audit.EventResourceShareRevoked, map[string]any{
		"resource_kind": kind,
		"resource_id":   id,
		"grantee":       grantee,
		"revoked_by":    principal.FromContext(r.Context()).ID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// mergeShares implements the idempotent create semantics: if a
// share for the same grantee already exists, replace its
// Actions (last-writer-wins). Otherwise append.
func mergeShares(existing []authz.Share, newShare authz.Share) []authz.Share {
	for i, sh := range existing {
		if sh.Grantee == newShare.Grantee {
			existing[i] = newShare
			return existing
		}
	}
	return append(existing, newShare)
}

// actionsToStrings flattens []Action to []string for audit
// detail JSON (the wire format wants strings; the enum would
// otherwise serialize as quoted strings anyway but this makes
// the intent explicit).
func actionsToStrings(actions []authz.Action) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = string(a)
	}
	return out
}

// ── save glue per resource kind ────────────────────────────
//
// The cluster + registry stores don't have an UpdateShares
// method today (feature 36 didn't need one — the shares field
// was zero on every persisted record). Rather than extend each
// store's interface we go through the lowest-cost path: read
// the record, patch the Shares slice, re-persist via SaveJob /
// SaveWorkflow / RegisterDataset semantics. For the registry
// this means a Get + Delete + Register because there's no
// Update primitive; the Delete + Register pair is not atomic,
// but share mutations are admin / owner-only and rare, so a
// crash between the two is acceptable (operator retries).
//
// If this becomes a hot path we add proper UpdateShares
// methods to the relevant stores. For now, keep the surface
// change narrow.

func (s *Server) saveJobShares(r *http.Request, id string, shares []authz.Share) error {
	// cluster.JobStore doesn't expose a generic Update; we
	// fetch the job, patch Shares, and re-persist via the
	// persister directly. A cleaner follow-up would add
	// UpdateShares to JobStore, but that's a per-store contract
	// change worth defering until more mutation paths need it.
	jobStore, ok := s.jobs.(*JobStoreAdapter)
	if !ok {
		return fmt.Errorf("saveJobShares: job store does not support share mutation")
	}
	return jobStore.UpdateShares(r.Context(), id, shares)
}

func (s *Server) saveWorkflowShares(r *http.Request, id string, shares []authz.Share) error {
	if s.workflowStore == nil {
		return cluster.ErrWorkflowNotFound
	}
	return s.workflowStore.UpdateShares(r.Context(), id, shares)
}

func (s *Server) saveDatasetShares(r *http.Request, id string, shares []authz.Share) error {
	if s.datasets == nil {
		return registry.ErrNotFound
	}
	name, version, err := splitRegistryID(id)
	if err != nil {
		return err
	}
	return s.datasets.UpdateDatasetShares(r.Context(), name, version, shares)
}

func (s *Server) saveModelShares(r *http.Request, id string, shares []authz.Share) error {
	if s.models == nil {
		return registry.ErrNotFound
	}
	name, version, err := splitRegistryID(id)
	if err != nil {
		return err
	}
	return s.models.UpdateModelShares(r.Context(), name, version, shares)
}

// Ensure cpb.Shares stays importable — the share-loading glue
// above reads j.Shares / wf.Shares. If a future refactor moves
// the field to a different package this assertion fails at
// compile time.
var _ = []authz.Share(cpb.Job{}.Shares)
