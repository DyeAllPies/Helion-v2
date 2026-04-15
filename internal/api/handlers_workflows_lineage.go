// internal/api/handlers_workflows_lineage.go
//
// Feature 18 → deferred/24: HTTP handler for
//   GET /workflows/{id}/lineage
//
// Returns the workflow's job DAG joined with JobStore status +
// ResolvedOutputs + registered models (via ModelStore.ListBySourceJob).
// Powers the dashboard's Pipelines DAG view; pure read-only, so no
// rate limiter beyond the standard auth middleware.
//
// Route registration lives in server.go (under SetWorkflowStore).
// The registry join is best-effort: a coordinator without the model
// registry wired still serves lineage — the response just has empty
// models_produced slices.

package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
)

func (s *Server) handleGetWorkflowLineage(w http.ResponseWriter, r *http.Request) {
	if s.workflowStore == nil || s.workflowJobStore == nil {
		writeError(w, http.StatusNotFound, "workflows are not configured on this coordinator")
		return
	}

	// ModelStore is optional — a coordinator without the registry
	// still has workflows, and lineage should degrade to "no models"
	// rather than 404 in that case.
	var modelReader cluster.LineageModelReader
	if s.models != nil {
		modelReader = s.models
	}

	id := r.PathValue("id")
	lineage, err := cluster.BuildWorkflowLineage(
		r.Context(), id, s.workflowStore, s.workflowJobStore, modelReader,
	)
	if err != nil {
		if errors.Is(err, cluster.ErrWorkflowLineageNotFound) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		slog.Error("workflow lineage failed",
			slog.String("workflow_id", id), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetWorkflowLineage", lineage)
}
