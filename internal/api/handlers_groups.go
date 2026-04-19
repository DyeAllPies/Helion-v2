// internal/api/handlers_groups.go
//
// Feature 38 — admin endpoints for group membership CRUD.
//
// Route map
// ─────────
//   POST   /admin/groups                          {name}
//   GET    /admin/groups                          -> [Group...]
//   GET    /admin/groups/{name}                   -> Group
//   DELETE /admin/groups/{name}
//   POST   /admin/groups/{name}/members           {principal_id}
//   DELETE /admin/groups/{name}/members/{principal...}
//
// Every endpoint runs through s.adminMiddleware (ActionAdmin
// against SystemResource). The handler body then calls through
// to the groups.Store and emits an audit event. There is no
// per-resource Allow call — feature 37's adminMiddleware shim
// already did it.
//
// Deletion semantics
// ──────────────────
// Deleting a group does NOT sweep `group:{name}` shares off
// existing resources. Shares referencing a deleted group are
// inert (Principal.Groups cannot contain the name once the
// reverse index is gone), so feature 37 rule 6b simply never
// matches them. Admins who want hard cleanup iterate shares
// via the share endpoints.

package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/groups"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// GroupResponse is the JSON shape returned by /admin/groups
// endpoints. Mirrors groups.Group 1:1 but lives in the api
// package so an internal-type tweak doesn't leak into the
// public contract.
type GroupResponse struct {
	Name      string   `json:"name"`
	Members   []string `json:"members,omitempty"`
	CreatedAt string   `json:"created_at"`
	CreatedBy string   `json:"created_by"`
	UpdatedAt string   `json:"updated_at"`
}

// GroupListResponse is the JSON body for GET /admin/groups.
type GroupListResponse struct {
	Groups []GroupResponse `json:"groups"`
	Total  int             `json:"total"`
}

// GroupCreateRequest is the JSON body for POST /admin/groups.
type GroupCreateRequest struct {
	Name string `json:"name"`
}

// GroupAddMemberRequest is the JSON body for
// POST /admin/groups/{name}/members.
type GroupAddMemberRequest struct {
	PrincipalID string `json:"principal_id"`
}

// groupToResponse adapts a groups.Group to the wire shape.
func groupToResponse(g groups.Group) GroupResponse {
	return GroupResponse{
		Name:      g.Name,
		Members:   g.Members,
		CreatedAt: g.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		CreatedBy: g.CreatedBy,
		UpdatedAt: g.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// groupsConfigured short-circuits with 404 when no groups store
// is wired. Matches the pattern used by registryConfigured.
func (s *Server) groupsConfigured(w http.ResponseWriter) bool {
	if s.groups == nil {
		writeError(w, http.StatusNotFound, "group management is not configured on this coordinator")
		return false
	}
	return true
}

// ── Create ─────────────────────────────────────────────────

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KiB
	var req GroupCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := groups.ValidateName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	actor := principal.FromContext(r.Context()).ID
	g := groups.Group{
		Name:      req.Name,
		CreatedBy: actor,
	}
	if err := s.groups.Create(r.Context(), g); err != nil {
		if errors.Is(err, groups.ErrGroupExists) {
			writeError(w, http.StatusConflict, "group already exists")
			return
		}
		if errors.Is(err, groups.ErrInvalidName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("group create failed", slog.String("name", req.Name), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "group creation failed")
		return
	}

	s.auditGroup(r, audit.EventGroupCreated, map[string]any{
		"name":       req.Name,
		"created_by": actor,
	})

	stored, _ := s.groups.Get(r.Context(), req.Name)
	if stored == nil {
		// Race: the group was deleted between Create and Get.
		// Respond with a synthesised record — the Create
		// succeeded; the client's request is fulfilled.
		stored = &g
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleCreateGroup", groupToResponse(*stored))
}

// ── Get / List ─────────────────────────────────────────────

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	name := r.PathValue("name")
	g, err := s.groups.Get(r.Context(), name)
	if err != nil {
		if errors.Is(err, groups.ErrGroupNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		if errors.Is(err, groups.ErrInvalidName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("group get failed", slog.String("name", name), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetGroup", groupToResponse(*g))
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	all, err := s.groups.List(r.Context())
	if err != nil {
		slog.Error("group list failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := GroupListResponse{
		Groups: make([]GroupResponse, len(all)),
		Total:  len(all),
	}
	for i, g := range all {
		resp.Groups[i] = groupToResponse(g)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListGroups", resp)
}

// ── Delete ─────────────────────────────────────────────────

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	name := r.PathValue("name")
	if err := s.groups.Delete(r.Context(), name); err != nil {
		if errors.Is(err, groups.ErrGroupNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		if errors.Is(err, groups.ErrInvalidName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("group delete failed", slog.String("name", name), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "group deletion failed")
		return
	}
	s.auditGroup(r, audit.EventGroupDeleted, map[string]any{
		"name":       name,
		"deleted_by": principal.FromContext(r.Context()).ID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── Member add / remove ────────────────────────────────────

func (s *Server) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	name := r.PathValue("name")
	var req GroupAddMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := groups.ValidatePrincipalID(req.PrincipalID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.groups.AddMember(r.Context(), name, req.PrincipalID); err != nil {
		if errors.Is(err, groups.ErrGroupNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		if errors.Is(err, groups.ErrInvalidName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("group add member failed",
			slog.String("group", name),
			slog.String("principal", req.PrincipalID),
			slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "add member failed")
		return
	}
	s.auditGroup(r, audit.EventGroupMemberAdded, map[string]any{
		"name":         name,
		"principal_id": req.PrincipalID,
		"added_by":     principal.FromContext(r.Context()).ID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	if !s.groupsConfigured(w) {
		return
	}
	name := r.PathValue("name")
	principalID := r.PathValue("principal")
	if err := groups.ValidatePrincipalID(principalID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.groups.RemoveMember(r.Context(), name, principalID); err != nil {
		if errors.Is(err, groups.ErrGroupNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		if errors.Is(err, groups.ErrInvalidName) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("group remove member failed",
			slog.String("group", name),
			slog.String("principal", principalID),
			slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "remove member failed")
		return
	}
	s.auditGroup(r, audit.EventGroupMemberRemoved, map[string]any{
		"name":         name,
		"principal_id": principalID,
		"removed_by":   principal.FromContext(r.Context()).ID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── Audit helper ───────────────────────────────────────────

// auditGroup centralises the eventType + actor + details write
// so every handler emits a consistent record shape. Log
// failures go to Error — group + share events are
// security-review evidence, not telemetry.
func (s *Server) auditGroup(r *http.Request, eventType string, details map[string]any) {
	if s.audit == nil {
		return
	}
	actor := principal.FromContext(r.Context()).ID
	if err := s.audit.Log(r.Context(), eventType, actor, details); err != nil {
		logAuditErr(true, eventType, err)
	}
}
