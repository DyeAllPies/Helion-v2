// internal/api/handlers_cluster.go
//
// Cluster observation handlers:
//   GET /healthz    — liveness probe (always 200)
//   GET /readyz     — readiness probe (DB + at least one node)
//   GET /nodes      — list registered nodes
//   GET /metrics    — cluster metrics (JSON fallback when Prometheus not wired)
//   GET /audit      — paginated audit log viewer

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.readiness == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ready":true}`))
		return
	}

	if err := s.readiness.Ping(); err != nil {
		slog.Error("readiness ping failed", slog.Any("err", err))
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "db not ready"})
		return
	}

	if s.readiness.RegistryLen() == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no nodes registered"})
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ready":true}`))
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	if s.nodes == nil {
		writeError(w, http.StatusNotImplemented, "node registry not configured")
		return
	}

	nodes, err := s.nodes.ListNodes(r.Context())
	if err != nil {
		slog.Error("list nodes failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := NodeListResponse{
		Nodes: nodes,
		Total: len(nodes),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeError(w, http.StatusNotImplemented, "metrics provider not configured")
		return
	}

	metrics, err := s.metrics.GetClusterMetrics(r.Context())
	if err != nil {
		slog.Error("get cluster metrics failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metrics)
}

func (s *Server) handleGetAudit(w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		writeError(w, http.StatusNotImplemented, "audit logging not configured")
		return
	}

	// Parse query parameters
	pageStr := r.URL.Query().Get("page")
	page := 1
	if pageStr != "" {
		p, err := strconv.Atoi(pageStr)
		if err != nil || p < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return
		}
		page = p
	}

	sizeStr := r.URL.Query().Get("size")
	size := 50
	if sizeStr != "" {
		sz, err := strconv.Atoi(sizeStr)
		if err != nil || sz < 1 || sz > 100 {
			writeError(w, http.StatusBadRequest, "size must be an integer between 1 and 100")
			return
		}
		size = sz
	}

	typeFilter := r.URL.Query().Get("type")

	// Query audit log
	query := audit.Query{
		Type:  typeFilter,
		Limit: size,
	}

	events, err := s.audit.QueryEvents(r.Context(), query)
	if err != nil {
		slog.Error("query audit log failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Simple pagination (skip first (page-1)*size events)
	skip := (page - 1) * size
	if skip >= len(events) {
		events = []audit.Event{}
	} else {
		end := skip + size
		if end > len(events) {
			end = len(events)
		}
		events = events[skip:end]
	}

	resp := AuditListResponse{
		Events: events,
		Total:  len(events),
		Page:   page,
		Size:   size,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
