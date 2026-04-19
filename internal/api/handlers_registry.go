// internal/api/handlers_registry.go
//
// REST handlers for dataset + model registries. Every mutation
// endpoint rides the shared auth middleware, runs the per-subject
// rate limiter (registryQueryAllow), emits an event + an audit
// record, and returns a specific HTTP code per registry sentinel
// (409 for ErrAlreadyExists, 404 for ErrNotFound, 400 for validator
// failures).
//
// JSON body cap is inherited from maxSubmitBodyBytes (1 MiB). That's
// overkill for metadata-only payloads but matches the rest of the
// API and prevents a caller from streaming MB of free-form tags.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

// ── Shared helpers ───────────────────────────────────────────────────────

// registryActor returns the JWT subject of the current request, or
// "anonymous" when auth is disabled. Used to stamp CreatedBy and
// audit actor fields. When auth is enabled and the subject is
// missing, the authMiddleware already returned 401 — callers here
// can assume claims are valid.
func (s *Server) registryActor(ctx context.Context) string {
	if s.tokenManager == nil {
		return "anonymous"
	}
	if claims, ok := ctx.Value(claimsContextKey).(*auth.Claims); ok && claims.Subject != "" {
		return claims.Subject
	}
	return "anonymous"
}

// registryPreflight rate-limits the caller. Returns false if the
// caller exceeded their bucket — handler must respond 429 and stop.
// Intentionally runs *before* any JSON decoding or validation so a
// flood of badly-formed requests doesn't escape the limiter.
func (s *Server) registryPreflight(w http.ResponseWriter, r *http.Request) (actor string, ok bool) {
	actor = s.registryActor(r.Context())
	if !s.registryQueryAllow(actor) {
		writeError(w, http.StatusTooManyRequests, "registry rate limit exceeded")
		return actor, false
	}
	return actor, true
}

// registryConfigured returns 404 when the registry was not wired
// into this Server. Keeps the endpoints invisible on a coordinator
// deployment that didn't opt into the registry (or on a node-only
// dev binary that never called SetRegistryStore).
func (s *Server) registryConfigured(w http.ResponseWriter) bool {
	if s.datasets == nil || s.models == nil {
		writeError(w, http.StatusNotFound, "registry is not configured on this coordinator")
		return false
	}
	return true
}

// ── Datasets ─────────────────────────────────────────────────────────────

func (s *Server) handleRegisterDataset(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	actor, ok := s.registryPreflight(w, r)
	if !ok {
		return
	}

	// Feature 24 — parse dry-run BEFORE body decode so ?dry_run=maybe
	// rejects cheap. The rate-limit check above still ran, keeping
	// dry-run bound to the same bucket as the real registration path.
	dryRun, err := ParseDryRunParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)
	var req DatasetRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}

	d := &registry.Dataset{
		Name:      req.Name,
		Version:   req.Version,
		URI:       req.URI,
		SizeBytes: req.SizeBytes,
		SHA256:    req.SHA256,
		Tags:      req.Tags,
		CreatedAt: time.Now().UTC(),
		CreatedBy: actor,
		// Feature 36 — typed Principal for feature 37's authz.
		OwnerPrincipal: principal.FromContext(r.Context()).ID,
	}
	if err := registry.ValidateDataset(d); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Feature 24 — dry-run short-circuit. Validators above already
	// ran; skip the durable RegisterDataset call, skip the event
	// publish (a dataset.registered bus event on a dry-run would
	// fire downstream subscribers for an object that never existed),
	// and emit a distinct dataset.dry_run audit event instead.
	// Deliberately NOT checking ErrAlreadyExists: a dry-run doesn't
	// reserve the slot, so surfacing 409 here would just leak whether
	// a version exists without adding real value.
	if dryRun {
		if s.audit != nil {
			dryDetails := map[string]any{
				"name":       d.Name,
				"version":    d.Version,
				"uri":        d.URI,
				"size_bytes": d.SizeBytes,
			}
			if d.OwnerPrincipal != "" {
				dryDetails["resource_owner"] = d.OwnerPrincipal // Feature 36
			}
			if aerr := s.audit.Log(r.Context(), audit.EventDatasetDryRun, actor, dryDetails); aerr != nil {
				logAuditErr(false, "dataset.dry_run", aerr)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeJSON(w, "handleRegisterDataset.dry_run", dryRunResponse(datasetToResponse(d)))
		return
	}

	if err := s.datasets.RegisterDataset(r.Context(), d); err != nil {
		switch {
		case errors.Is(err, registry.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "dataset already registered at this version")
			return
		default:
			slog.Error("register dataset failed",
				slog.String("name", d.Name), slog.String("version", d.Version),
				slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	// Audit + event — side-effects after the durable write so a
	// persist failure doesn't emit a misleading "registered" signal.
	if s.audit != nil {
		details := map[string]any{
			"name":       d.Name,
			"version":    d.Version,
			"uri":        d.URI,
			"size_bytes": d.SizeBytes,
		}
		if d.OwnerPrincipal != "" {
			details["resource_owner"] = d.OwnerPrincipal // Feature 36
		}
		if aerr := s.audit.Log(r.Context(), "dataset.registered", actor, details); aerr != nil {
			logAuditErr(false, "dataset.registered", aerr)
		}
	}
	if s.eventBus != nil {
		s.eventBus.Publish(events.DatasetRegistered(d.Name, d.Version, d.URI, actor, d.SizeBytes))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleRegisterDataset", datasetToResponse(d))
}

func (s *Server) handleGetDataset(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	if _, ok := s.registryPreflight(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	d, err := s.datasets.GetDataset(name, version)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dataset not found")
			return
		}
		slog.Error("get dataset failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetDataset", datasetToResponse(d))
}

func (s *Server) handleListDatasets(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	if _, ok := s.registryPreflight(w, r); !ok {
		return
	}
	page, size := parsePageSize(r)
	datasets, total, err := s.datasets.ListDatasets(r.Context(), page, size)
	if err != nil {
		slog.Error("list datasets failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := DatasetListResponse{
		Datasets: make([]DatasetResponse, len(datasets)),
		Total:    total,
		Page:     page,
		Size:     size,
	}
	for i, d := range datasets {
		resp.Datasets[i] = datasetToResponse(d)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListDatasets", resp)
}

func (s *Server) handleDeleteDataset(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	actor, ok := s.registryPreflight(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	if err := s.datasets.DeleteDataset(r.Context(), name, version); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dataset not found")
			return
		}
		slog.Error("delete dataset failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if s.audit != nil {
		if aerr := s.audit.Log(r.Context(), "dataset.deleted", actor, map[string]any{
			"name":    name,
			"version": version,
		}); aerr != nil {
			logAuditErr(false, "dataset.deleted", aerr)
		}
	}
	if s.eventBus != nil {
		s.eventBus.Publish(events.DatasetDeleted(name, version, actor))
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Models ───────────────────────────────────────────────────────────────

func (s *Server) handleRegisterModel(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	actor, ok := s.registryPreflight(w, r)
	if !ok {
		return
	}

	// Feature 24 — parse dry-run BEFORE body decode so ?dry_run=maybe
	// rejects cheap. See handleRegisterDataset for rationale.
	dryRun, err := ParseDryRunParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)
	var req ModelRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}

	m := &registry.Model{
		Name:        req.Name,
		Version:     req.Version,
		URI:         req.URI,
		Framework:   req.Framework,
		SourceJobID: req.SourceJobID,
		Metrics:     req.Metrics,
		SizeBytes:   req.SizeBytes,
		SHA256:      req.SHA256,
		Tags:        req.Tags,
		CreatedAt:   time.Now().UTC(),
		CreatedBy:   actor,
		// Feature 36 — typed Principal for feature 37's authz.
		OwnerPrincipal: principal.FromContext(r.Context()).ID,
	}
	if req.SourceDataset != nil {
		m.SourceDataset = registry.DatasetRef{
			Name:    req.SourceDataset.Name,
			Version: req.SourceDataset.Version,
		}
	}
	if err := registry.ValidateModel(m); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Feature 24 — dry-run short-circuit. Same pattern as datasets:
	// run all validators, then emit model.dry_run and return 200
	// without persisting or publishing the model.registered event.
	if dryRun {
		if s.audit != nil {
			dryDetails := map[string]any{
				"name":          m.Name,
				"version":       m.Version,
				"uri":           m.URI,
				"source_job_id": m.SourceJobID,
			}
			if m.OwnerPrincipal != "" {
				dryDetails["resource_owner"] = m.OwnerPrincipal // Feature 36
			}
			if aerr := s.audit.Log(r.Context(), audit.EventModelDryRun, actor, dryDetails); aerr != nil {
				logAuditErr(false, "model.dry_run", aerr)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeJSON(w, "handleRegisterModel.dry_run", dryRunResponse(modelToResponse(m)))
		return
	}

	if err := s.models.RegisterModel(r.Context(), m); err != nil {
		switch {
		case errors.Is(err, registry.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "model already registered at this version")
			return
		default:
			slog.Error("register model failed",
				slog.String("name", m.Name), slog.String("version", m.Version),
				slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if s.audit != nil {
		details := map[string]any{
			"name":          m.Name,
			"version":       m.Version,
			"uri":           m.URI,
			"source_job_id": m.SourceJobID,
		}
		if m.OwnerPrincipal != "" {
			details["resource_owner"] = m.OwnerPrincipal // Feature 36
		}
		if aerr := s.audit.Log(r.Context(), "model.registered", actor, details); aerr != nil {
			logAuditErr(false, "model.registered", aerr)
		}
	}
	if s.eventBus != nil {
		s.eventBus.Publish(events.ModelRegistered(m.Name, m.Version, m.URI, actor,
			m.SourceJobID, m.SourceDataset.Name, m.SourceDataset.Version))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleRegisterModel", modelToResponse(m))
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	if _, ok := s.registryPreflight(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	m, err := s.models.GetModel(name, version)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "model not found")
			return
		}
		slog.Error("get model failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetModel", modelToResponse(m))
}

func (s *Server) handleLatestModel(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	if _, ok := s.registryPreflight(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	m, err := s.models.LatestModel(name)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "model not found")
			return
		}
		slog.Error("latest model failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleLatestModel", modelToResponse(m))
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	if _, ok := s.registryPreflight(w, r); !ok {
		return
	}
	page, size := parsePageSize(r)
	models, total, err := s.models.ListModels(r.Context(), page, size)
	if err != nil {
		slog.Error("list models failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := ModelListResponse{
		Models: make([]ModelResponse, len(models)),
		Total:  total,
		Page:   page,
		Size:   size,
	}
	for i, m := range models {
		resp.Models[i] = modelToResponse(m)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListModels", resp)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if !s.registryConfigured(w) {
		return
	}
	actor, ok := s.registryPreflight(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	if err := s.models.DeleteModel(r.Context(), name, version); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "model not found")
			return
		}
		slog.Error("delete model failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if s.audit != nil {
		if aerr := s.audit.Log(r.Context(), "model.deleted", actor, map[string]any{
			"name":    name,
			"version": version,
		}); aerr != nil {
			logAuditErr(false, "model.deleted", aerr)
		}
	}
	if s.eventBus != nil {
		s.eventBus.Publish(events.ModelDeleted(name, version, actor))
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── conversion helpers ───────────────────────────────────────────────────

func datasetToResponse(d *registry.Dataset) DatasetResponse {
	return DatasetResponse{
		Name:           d.Name,
		Version:        d.Version,
		URI:            d.URI,
		SizeBytes:      d.SizeBytes,
		SHA256:         d.SHA256,
		Tags:           d.Tags,
		CreatedAt:      d.CreatedAt,
		CreatedBy:      d.CreatedBy,
		OwnerPrincipal: d.OwnerPrincipal, // Feature 36
	}
}

func modelToResponse(m *registry.Model) ModelResponse {
	resp := ModelResponse{
		Name:           m.Name,
		Version:        m.Version,
		URI:            m.URI,
		Framework:      m.Framework,
		SourceJobID:    m.SourceJobID,
		Metrics:        m.Metrics,
		SizeBytes:      m.SizeBytes,
		SHA256:         m.SHA256,
		Tags:           m.Tags,
		CreatedAt:      m.CreatedAt,
		CreatedBy:      m.CreatedBy,
		OwnerPrincipal: m.OwnerPrincipal, // Feature 36
	}
	if m.SourceDataset.Name != "" || m.SourceDataset.Version != "" {
		resp.SourceDataset = &DatasetRefRequest{
			Name:    m.SourceDataset.Name,
			Version: m.SourceDataset.Version,
		}
	}
	return resp
}

// parsePageSize pulls page/size from the query string with the same
// caps used elsewhere in the API (page max 10_000, size max 100,
// default size 20). Invalid values fall back to defaults rather than
// 400-ing — the registry is a low-traffic admin surface and an
// obscure "invalid size" error would be more annoying than useful.
func parsePageSize(r *http.Request) (int, int) {
	page := 1
	size := 20
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 10_000 {
			page = n
		}
	}
	if sz := r.URL.Query().Get("size"); sz != "" {
		if n, err := strconv.Atoi(sz); err == nil && n > 0 && n <= 100 {
			size = n
		}
	}
	return page, size
}
