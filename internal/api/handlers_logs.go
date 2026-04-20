// internal/api/handlers_logs.go
//
// GET /jobs/{id}/logs — retrieve stored job stdout/stderr.

package api

import (
	"net/http"
	"strconv"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/logstore"
)

func (s *Server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tail := 0
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			tail = n
		}
	}

	// Feature 37 — per-job RBAC on log read. The logs of a
	// user's job can leak every bit of state the job touched
	// (stdout contents, error traces, partial output). Treat
	// the same as reading the Job record itself: admin OR
	// owner OR a feature-38 shareholder with ActionRead.
	//
	// Load the job BEFORE touching the log store so the authz
	// decision sees the authoritative owner + share list.
	job, err := s.jobs.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if !s.authzCheck(w, r, authz.ActionRead,
		authz.JobResource(job.ID, job.OwnerPrincipal, job.WorkflowID, job.Shares)) {
		return
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

	// Feature 29 — response-path secret redaction (defence in
	// depth). The ScrubbingStore decorator already scrubbed
	// at write time, but chunks persisted BEFORE the decorator
	// was configured (mid-rollout, stale Badger records) or a
	// chunk that happened to arrive while the submit record
	// didn't yet carry its SecretKeys list would have landed
	// verbatim. We run the same substitution on the response
	// bytes as a second pass. Idempotent against already-
	// scrubbed content.
	if len(job.SecretKeys) > 0 {
		values := collectSecretValues(job.Env, job.SecretKeys)
		for i := range entries {
			entries[i].Data = string(logstore.Scrub([]byte(entries[i].Data), values))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetJobLogs", map[string]interface{}{
		"job_id":  id,
		"entries": entries,
		"total":   len(entries),
	})
}

// collectSecretValues materialises the set of secret VALUES to
// scrub from log output: env[k] for each k in secretKeys where
// the env entry actually exists. Missing keys are silently
// skipped — a declared-but-unset secret has no value to scrub
// and shouldn't drag an empty string into the substitution
// (the Scrub helper already guards against empty values, but
// keeping the helper pure means we filter here).
func collectSecretValues(env map[string]string, secretKeys []string) []string {
	out := make([]string, 0, len(secretKeys))
	for _, k := range secretKeys {
		if v, ok := env[k]; ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}
