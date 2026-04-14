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

// Step 2 — ML pipeline input/output bounds. These caps mirror the
// defense-in-depth approach used elsewhere in this file: each bound is
// generous (much larger than any realistic ML job) but sharp enough that
// an unbounded request cannot weaponise it. The same limits apply to
// inputs and outputs independently.
const (
	maxArtifactBindings    = 64   // per-direction (inputs or outputs)
	maxArtifactNameLen     = 64   // environment-variable name ceiling
	maxArtifactLocalPath   = 512  // relative path length
	maxArtifactURILen      = 2048 // URI length ceiling
	maxNodeSelectorEntries = 32
	maxNodeSelectorKeyLen  = 63
	maxNodeSelectorValLen  = 253
	maxWorkingDirLen       = 512
)

// validateArtifactName enforces that Name is a valid shell identifier.
// The runtime uses it verbatim in HELION_INPUT_<NAME>, so anything that
// isn't [A-Z_][A-Z0-9_]* would either fail on exec or, worse, be
// interpreted oddly by the child process's shell wrappers.
func validateArtifactName(name string) bool {
	if name == "" || len(name) > maxArtifactNameLen {
		return false
	}
	for i, c := range name {
		switch {
		case c >= 'A' && c <= 'Z':
		case c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// validateLocalPath rejects paths that could escape the working
// directory. Absolute paths, backslashes (Windows separators snuck past
// clients), NUL bytes, and any ".." segment are refused. A traversal
// check runs after filepath.Clean-style normalisation in the runtime,
// but we reject early here so a bad request never even reaches a node.
func validateLocalPath(p string) string {
	if p == "" {
		return "local_path is required"
	}
	if len(p) > maxArtifactLocalPath {
		return fmt.Sprintf("local_path must not exceed %d bytes", maxArtifactLocalPath)
	}
	if strings.ContainsRune(p, '\x00') {
		return "local_path must not contain NUL"
	}
	if strings.ContainsRune(p, '\\') {
		return "local_path must use forward slashes"
	}
	if strings.HasPrefix(p, "/") {
		return "local_path must be relative"
	}
	// Segment scan: reject empty and ".." segments. "." is allowed only
	// as the single leading segment in patterns like "./file", which
	// we normalise by not allowing it outright — keep things simple.
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == ".." || seg == "." {
			return "local_path must not contain empty, '.', or '..' segments"
		}
	}
	return ""
}

// validateNodeSelector applies label-shape rules before persisting.
// Keys and values are clamped to Kubernetes-compatible sizes so that
// once the scheduler wires these in (step 4), the limits already match
// operator muscle memory.
func validateNodeSelector(sel map[string]string) string {
	if len(sel) > maxNodeSelectorEntries {
		return fmt.Sprintf("node_selector must not exceed %d entries", maxNodeSelectorEntries)
	}
	for k, v := range sel {
		if k == "" || len(k) > maxNodeSelectorKeyLen {
			return fmt.Sprintf("node_selector keys must be 1-%d bytes", maxNodeSelectorKeyLen)
		}
		if strings.ContainsAny(k, "=\x00") {
			return "node_selector keys must not contain '=' or NUL"
		}
		if len(v) > maxNodeSelectorValLen {
			return fmt.Sprintf("node_selector values must not exceed %d bytes", maxNodeSelectorValLen)
		}
		if strings.ContainsRune(v, '\x00') {
			return "node_selector values must not contain NUL"
		}
	}
	return ""
}

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
	// Step 2 — ML pipeline fields.
	if len(req.WorkingDir) > maxWorkingDirLen {
		return fmt.Sprintf("working_dir must not exceed %d bytes", maxWorkingDirLen)
	}
	if strings.ContainsRune(req.WorkingDir, '\x00') {
		return "working_dir must not contain NUL"
	}
	if msg := validateArtifactBindings("inputs", req.Inputs, true); msg != "" {
		return msg
	}
	if msg := validateArtifactBindings("outputs", req.Outputs, false); msg != "" {
		return msg
	}
	// Names must be unique across inputs alone and outputs alone so the
	// HELION_INPUT_<NAME> / HELION_OUTPUT_<NAME> exports are unambiguous.
	if dup := firstDuplicateBindingName(req.Inputs); dup != "" {
		return fmt.Sprintf("inputs: duplicate name %q", dup)
	}
	if dup := firstDuplicateBindingName(req.Outputs); dup != "" {
		return fmt.Sprintf("outputs: duplicate name %q", dup)
	}
	if msg := validateNodeSelector(req.NodeSelector); msg != "" {
		return msg
	}
	return ""
}

// validateArtifactBindings runs per-binding checks common to inputs and
// outputs. requireURI differentiates the two: inputs must name the
// artifact to pull, outputs have their URI assigned by the runtime.
func validateArtifactBindings(kind string, bs []ArtifactBindingRequest, requireURI bool) string {
	if len(bs) > maxArtifactBindings {
		return fmt.Sprintf("%s must not exceed %d entries", kind, maxArtifactBindings)
	}
	for i, b := range bs {
		if !validateArtifactName(b.Name) {
			return fmt.Sprintf("%s[%d].name must match [A-Z_][A-Z0-9_]*", kind, i)
		}
		if msg := validateLocalPath(b.LocalPath); msg != "" {
			return fmt.Sprintf("%s[%d].%s", kind, i, msg)
		}
		if len(b.URI) > maxArtifactURILen {
			return fmt.Sprintf("%s[%d].uri must not exceed %d bytes", kind, i, maxArtifactURILen)
		}
		for j := 0; j < len(b.URI); j++ {
			if c := b.URI[j]; c == 0 || c < 0x20 || c == 0x7f {
				return fmt.Sprintf("%s[%d].uri must not contain NUL or control bytes", kind, i)
			}
		}
		if requireURI && b.URI == "" {
			return fmt.Sprintf("%s[%d].uri is required", kind, i)
		}
		if !requireURI && b.URI != "" {
			return fmt.Sprintf("%s[%d].uri must be empty on submit (assigned by runtime)", kind, i)
		}
		// Input URIs are dereferenced by the node's stager: the
		// command line to attach an attacker's malware to a training
		// job is a Put + a submit pointing at `http://attacker/x`.
		// Lock the scheme to what the configured artifact Store
		// understands — everything else is rejected here, long before
		// the node gets involved.
		if requireURI && !isAllowedArtifactScheme(b.URI) {
			return fmt.Sprintf("%s[%d].uri scheme must be file:// or s3://", kind, i)
		}
	}
	return ""
}

// isAllowedArtifactScheme returns true iff uri starts with one of the
// schemes the artifact Store understands. Kept separate from the URL
// parser: we only need a prefix match and want no allocation.
func isAllowedArtifactScheme(uri string) bool {
	return strings.HasPrefix(uri, "file://") || strings.HasPrefix(uri, "s3://")
}

func firstDuplicateBindingName(bs []ArtifactBindingRequest) string {
	seen := make(map[string]struct{}, len(bs))
	for _, b := range bs {
		if _, ok := seen[b.Name]; ok {
			return b.Name
		}
		seen[b.Name] = struct{}{}
	}
	return ""
}

// convertBindings lifts the API-layer request shape to the persisted
// cpb.ArtifactBinding form. Returns nil for an empty slice so that the
// BadgerDB-serialised Job carries an omitted key rather than an empty
// array (match the rest of this struct's ergonomics).
func convertBindings(bs []ArtifactBindingRequest) []cpb.ArtifactBinding {
	if len(bs) == 0 {
		return nil
	}
	out := make([]cpb.ArtifactBinding, len(bs))
	for i, b := range bs {
		out[i] = cpb.ArtifactBinding{
			Name:      b.Name,
			URI:       b.URI,
			LocalPath: b.LocalPath,
		}
	}
	return out
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
		WorkingDir:   req.WorkingDir,
		Inputs:       convertBindings(req.Inputs),
		Outputs:      convertBindings(req.Outputs),
		NodeSelector: req.NodeSelector,
	}

	// Parse optional priority.
	if req.Priority != nil {
		if *req.Priority > 100 {
			writeError(w, http.StatusBadRequest, "priority must be between 0 and 100")
			return
		}
		job.Priority = *req.Priority
	}

	// Parse optional resource request.
	if req.Resources != nil {
		job.Resources = cpb.ResourceRequest{
			CpuMillicores: req.Resources.CpuMillicores,
			MemoryBytes:   req.Resources.MemoryBytes,
			Slots:         req.Resources.Slots,
		}
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

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := s.jobs.CancelJob(r.Context(), id, "cancelled via API"); err != nil {
		if errors.Is(err, cluster.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		if errors.Is(err, cluster.ErrJobAlreadyTerminal) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Error("cancel job failed", slog.String("job_id", id), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "job cancellation failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleCancelJob", map[string]interface{}{
		"id":      id,
		"status":  "cancelled",
		"message": "job cancelled successfully",
	})
}

// validJobStatuses is the set of status strings accepted by the ?status= query
// parameter. Values are uppercase to match the underlying store convention.
var validJobStatuses = map[string]bool{
	"UNKNOWN": true, "PENDING": true, "DISPATCHING": true, "RUNNING": true,
	"COMPLETED": true, "FAILED": true, "TIMEOUT": true, "LOST": true,
	"RETRYING": true, "SCHEDULED": true, "CANCELLED": true, "SKIPPED": true,
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
