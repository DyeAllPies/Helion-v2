// internal/api/handlers_logs.go
//
// GET /jobs/{id}/logs — retrieve stored job stdout/stderr.

package api

import (
	"net/http"
	"strconv"
)

func (s *Server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tail := 0
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			tail = n
		}
	}

	entries, err := s.logStore.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve logs")
		return
	}

	// Apply tail if requested.
	if tail > 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetJobLogs", map[string]interface{}{
		"job_id":  id,
		"entries": entries,
		"total":   len(entries),
	})
}
