// internal/api/handlers_jobs.go
//
// Job CRUD handlers:
//   POST /jobs        — submit a new job
//   GET  /jobs/{id}   — read a single job
//   GET  /jobs        — list jobs (paginated, filterable by status)

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// AUDIT M4 (fixed): body size is capped at 1 MiB via http.MaxBytesReader so that
// a large request cannot exhaust coordinator memory.
const maxSubmitBodyBytes = 1 << 20 // 1 MiB

// AUDIT M7 (fixed): timeout_seconds is bounded to 1 hour. Values outside
// [0, maxTimeoutSeconds] are rejected with 400 Bad Request.
const maxTimeoutSeconds = 3600 // 1 hour upper bound

// AUDIT C4 / C5 (fixed): input validation bounds.
//
// maxArgsLen and maxEnvLen prevent a caller from exhausting memory by
// submitting unbounded argv / environment slices. The limits are generous
// (much larger than any realistic job) but sharp enough that a runaway
// caller cannot weaponise them.
const (
	maxArgsLen = 512
	maxEnvLen  = 128
)

// minMemoryBytes is the floor enforced when MemoryBytes is set (cgroup memory
// controllers reject values this small and modern runtimes crash-loop below it).
const minMemoryBytes = 4 << 20 // 4 MiB

// CPUPeriodUS must be within the Linux cgroup-v2 valid range.
const (
	minCPUPeriodUS = 1_000
	maxCPUPeriodUS = 1_000_000
)

// maxCPUCores caps CPUQuotaUS at `maxCPUCores * CPUPeriodUS` so a single job
// cannot claim more than 512 logical cores.
const maxCPUCores = 512

// forbiddenCommandChars lists bytes that are never valid inside req.Command.
// Forbidding path separators prevents absolute-path execution; forbidding
// shell metacharacters is defense-in-depth (the runtime does not invoke a
// shell, but disallowing them surfaces mistakes early and blocks the most
// common injection shapes).
const forbiddenCommandChars = "/\\`$|&;<>\x00"

// validateSubmitRequest runs the AUDIT C4 / C5 input checks that do not depend
// on request state. Returns an empty string on success or a user-facing error
// message (which the caller sends with 400 Bad Request).
func validateSubmitRequest(req *SubmitRequest) string {
	// Command content — path separators, shell metacharacters, NUL.
	if strings.ContainsAny(req.Command, forbiddenCommandChars) {
		return "command must not contain path separators or shell metacharacters"
	}
	// Args count.
	if len(req.Args) > maxArgsLen {
		return fmt.Sprintf("args must not exceed %d entries", maxArgsLen)
	}
	// Env count.
	if len(req.Env) > maxEnvLen {
		return fmt.Sprintf("env must not exceed %d entries", maxEnvLen)
	}
	// Env key/value content — no `=` or NUL in keys, no NUL in values.
	for k, v := range req.Env {
		if k == "" {
			return "env keys must not be empty"
		}
		if strings.ContainsAny(k, "=\x00") {
			return "env keys must not contain '=' or NUL"
		}
		if strings.ContainsRune(v, '\x00') {
			return "env values must not contain NUL"
		}
	}
	// MemoryBytes floor (only when set).
	if req.Limits.MemoryBytes > 0 && req.Limits.MemoryBytes < minMemoryBytes {
		return fmt.Sprintf("limits.memory_bytes must be at least %d when set", minMemoryBytes)
	}
	// CPUPeriodUS range (only when set).
	if req.Limits.CPUPeriodUS > 0 {
		if req.Limits.CPUPeriodUS < minCPUPeriodUS || req.Limits.CPUPeriodUS > maxCPUPeriodUS {
			return fmt.Sprintf("limits.cpu_period_us must be in [%d, %d]", minCPUPeriodUS, maxCPUPeriodUS)
		}
	}
	// CPUQuotaUS cap — at most maxCPUCores × period.
	if req.Limits.CPUQuotaUS > 0 && req.Limits.CPUPeriodUS > 0 {
		if req.Limits.CPUQuotaUS > req.Limits.CPUPeriodUS*maxCPUCores {
			return fmt.Sprintf("limits.cpu_quota_us must not exceed %d × cpu_period_us", maxCPUCores)
		}
	}
	return ""
}

func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	// AUDIT M4: cap request body before decoding to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)
	var req SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// AUDIT M4: generic message prevents leaking internal decode details.
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}
	// AUDIT M7: reject negative and excessive timeout values.
	if req.TimeoutSeconds < 0 {
		writeError(w, http.StatusBadRequest, "timeout_seconds must not be negative")
		return
	}
	if req.TimeoutSeconds > maxTimeoutSeconds {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("timeout_seconds must not exceed %d", maxTimeoutSeconds))
		return
	}
	// AUDIT C4 / C5: validate command, args, env, and resource limits.
	if msg := validateSubmitRequest(&req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	job := &cpb.Job{
		ID:             req.ID,
		Command:        req.Command,
		Args:           req.Args,
		Env:            req.Env,
		TimeoutSeconds: req.TimeoutSeconds,
		Limits: cpb.ResourceLimits{
			MemoryBytes: req.Limits.MemoryBytes,
			CPUQuotaUS:  req.Limits.CPUQuotaUS,
			CPUPeriodUS: req.Limits.CPUPeriodUS,
		},
	}

	// Parse optional retry policy.
	if req.RetryPolicy != nil {
		rp := req.RetryPolicy
		if rp.MaxAttempts > 100 {
			writeError(w, http.StatusBadRequest, "retry_policy.max_attempts must not exceed 100")
			return
		}
		policy := &cpb.RetryPolicy{
			MaxAttempts:    rp.MaxAttempts,
			InitialDelayMs: rp.InitialDelayMs,
			MaxDelayMs:     rp.MaxDelayMs,
			Jitter:         true, // default
		}
		if rp.Jitter != nil {
			policy.Jitter = *rp.Jitter
		}
		switch strings.ToLower(rp.Backoff) {
		case "none":
			policy.Backoff = cpb.BackoffNone
		case "linear":
			policy.Backoff = cpb.BackoffLinear
		case "", "exponential":
			policy.Backoff = cpb.BackoffExponential
		default:
			writeError(w, http.StatusBadRequest, "retry_policy.backoff must be none, linear, or exponential")
			return
		}
		if policy.MaxAttempts > 1 {
			job.RetryPolicy = policy
		}
	}

	// AUDIT L1 (fixed): record the caller's JWT subject so handleGetJob
	// can enforce per-job RBAC. Skipped when auth is disabled (dev mode).
	if s.tokenManager != nil {
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			job.SubmittedBy = claims.Subject
		}
	}

	if err := s.jobs.Submit(r.Context(), job); err != nil {
		// AUDIT M8 (fixed): duplicate job IDs return 409 Conflict rather than
		// silently overwriting the existing job or returning 500.
		if errors.Is(err, cluster.ErrJobExists) {
			writeError(w, http.StatusConflict, "job with this id already exists")
			return
		}
		slog.Error("job submit failed", slog.String("job_id", job.ID), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "job submission failed")
		return
	}

	// Phase 4: Log job submission to audit log
	if s.audit != nil {
		actor := "anonymous"
		if s.tokenManager != nil {
			if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
				actor = claims.Subject
			}
		}
		if err := s.audit.LogJobSubmit(r.Context(), actor, job.ID, job.Command); err != nil {
			logAuditErr(false, "job.submit", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleSubmitJob", jobToResponse(job))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	// Go 1.22+ ServeMux pattern variables via r.PathValue.
	id := r.PathValue("id")
	if id == "" {
		// Fallback for older pattern parsing: extract from URL path.
		id = strings.TrimPrefix(r.URL.Path, "/jobs/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "job id required")
		return
	}

	job, err := s.jobs.Get(id)
	if err != nil {
		// AUDIT L4 (fixed): generic "job not found" — does not echo the ID back,
		// preventing enumeration information leakage.
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// AUDIT L1 (fixed): per-job RBAC. When auth is enabled, only admins and
	// the original submitter can read a job. Dev mode (nil tokenManager) and
	// explicit DisableAuth skip the check so existing tooling keeps working.
	if s.tokenManager != nil {
		claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
		if !ok {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if claims.Role != "admin" && claims.Subject != job.SubmittedBy {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetJob", jobToResponse(job))
}

// validJobStatuses is the set of status strings accepted by the ?status= query
// parameter. Values are uppercase to match the underlying store convention.
var validJobStatuses = map[string]bool{
	"UNKNOWN": true, "PENDING": true, "DISPATCHING": true, "RUNNING": true,
	"COMPLETED": true, "FAILED": true, "TIMEOUT": true, "LOST": true,
	"RETRYING": true,
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
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
	size := 20
	if sizeStr != "" {
		sz, err := strconv.Atoi(sizeStr)
		if err != nil || sz < 1 || sz > 100 {
			writeError(w, http.StatusBadRequest, "size must be an integer between 1 and 100")
			return
		}
		size = sz
	}

	// AUDIT M6 (fixed): cap page number to prevent integer-overflow style
	// offset calculations that could OOM the coordinator.
	const maxPage = 10_000
	if page > maxPage {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("page must not exceed %d", maxPage))
		return
	}

	statusFilter := strings.ToUpper(r.URL.Query().Get("status"))
	if statusFilter != "" && !validJobStatuses[statusFilter] {
		writeError(w, http.StatusBadRequest, "invalid status filter")
		return
	}

	// Get jobs from store
	jobs, total, err := s.jobs.List(r.Context(), statusFilter, page, size)
	if err != nil {
		slog.Error("list jobs failed", slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Convert to response format
	jobResponses := make([]JobResponse, len(jobs))
	for i, job := range jobs {
		jobResponses[i] = jobToResponse(job)
	}

	resp := JobListResponse{
		Jobs:  jobResponses,
		Total: total,
		Page:  page,
		Size:  size,
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListJobs", resp)
}
