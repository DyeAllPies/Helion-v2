// internal/api/handlers_workflows.go
//
// Workflow CRUD handlers:
//   POST   /workflows        — submit a new workflow
//   GET    /workflows/{id}   — read a single workflow with job statuses
//   GET    /workflows        — list workflows (paginated)
//   DELETE /workflows/{id}   — cancel a running workflow

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// maxWorkflowJobs caps the number of jobs in a single workflow to prevent
// excessive memory usage during DAG validation and job creation.
const maxWorkflowJobs = 100

// ── Request / Response types ────────────────────────────────────────────────

// WorkflowJobRequest is a single job definition within a workflow submission.
type WorkflowJobRequest struct {
	Name           string            `json:"name"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
	DependsOn      []string          `json:"depends_on,omitempty"`
	Condition      string            `json:"condition,omitempty"` // "on_success" (default), "on_failure", "on_complete"
	Priority       *uint32           `json:"priority,omitempty"` // overrides workflow priority; 0-100

	// Step 2 — ML pipeline fields. Mirror SubmitRequest so workflow YAML
	// can declare artifact bindings per job; validated by the same rules.
	WorkingDir   string                   `json:"working_dir,omitempty"`
	Inputs       []ArtifactBindingRequest `json:"inputs,omitempty"`
	Outputs      []ArtifactBindingRequest `json:"outputs,omitempty"`
	NodeSelector map[string]string        `json:"node_selector,omitempty"`

	// Feature 26 — per-child secret env keys. Flows through
	// cpb.WorkflowJob and onto cpb.Job at workflow Start. See
	// SubmitRequest.SecretKeys for the full contract.
	SecretKeys []string `json:"secret_keys,omitempty"`
}

// SubmitWorkflowRequest is the JSON body for POST /workflows.
type SubmitWorkflowRequest struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Priority *uint32              `json:"priority,omitempty"` // default priority for all jobs; 0-100
	Jobs     []WorkflowJobRequest `json:"jobs"`
}

// WorkflowJobResponse is a single job in the workflow response.
type WorkflowJobResponse struct {
	Name           string            `json:"name"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	// Env is redacted: values whose key appears in SecretKeys render
	// as "[REDACTED]". See jobToResponse for the equivalent on
	// /jobs/{id}.
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	DependsOn      []string          `json:"depends_on,omitempty"`
	Condition      string            `json:"condition"`
	JobID          string            `json:"job_id,omitempty"`
	JobStatus      string            `json:"job_status,omitempty"`
	// Feature 26 — echoed back so the dashboard can render a
	// "secret" badge next to redacted values without guessing.
	SecretKeys     []string          `json:"secret_keys,omitempty"`
}

// WorkflowResponse is the JSON body returned for workflow endpoints.
type WorkflowResponse struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Status     string                `json:"status"`
	Jobs       []WorkflowJobResponse `json:"jobs"`
	CreatedAt  string                `json:"created_at"`
	StartedAt  string                `json:"started_at,omitempty"`
	FinishedAt string                `json:"finished_at,omitempty"`
	Error      string                `json:"error,omitempty"`
	// Feature 36 — owner principal ID. Same format as
	// JobResponse.OwnerPrincipal; "legacy:" for pre-feature-36
	// records.
	OwnerPrincipal string `json:"owner_principal,omitempty"`
}

// WorkflowListResponse is the response for GET /workflows.
type WorkflowListResponse struct {
	Workflows []WorkflowResponse `json:"workflows"`
	Total     int                `json:"total"`
	Page      int                `json:"page"`
	Size      int                `json:"size"`
}

// ── Handlers ────────────────────────────────────────────────────────────────

func (s *Server) handleSubmitWorkflow(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)

	// Feature 24 — parse the dry-run flag BEFORE decoding the body
	// so an obvious typo (?dry_run=maybe) rejects cheap. The real
	// path and the dry-run path share every validator below; only
	// the terminal Submit+Start calls + audit-event type differ.
	dryRun, err := ParseDryRunParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req SubmitWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request format")
		return
	}

	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if len(req.Jobs) == 0 {
		writeError(w, http.StatusBadRequest, "at least one job is required")
		return
	}
	if len(req.Jobs) > maxWorkflowJobs {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("workflow must not exceed %d jobs", maxWorkflowJobs))
		return
	}

	// Validate each job.
	for _, j := range req.Jobs {
		if j.Name == "" {
			writeError(w, http.StatusBadRequest, "all jobs must have a name")
			return
		}
		if j.Command == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: command is required", j.Name))
			return
		}
		if strings.ContainsAny(j.Command, forbiddenCommandChars) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: command must not contain path separators or shell metacharacters", j.Name))
			return
		}
		if j.TimeoutSeconds < 0 || j.TimeoutSeconds > maxTimeoutSeconds {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: timeout_seconds must be in [0, %d]", j.Name, maxTimeoutSeconds))
			return
		}
		// Step 2 — reuse the submit-job validators so workflow job
		// bindings get the same treatment as standalone submits.
		if len(j.WorkingDir) > maxWorkingDirLen || strings.ContainsRune(j.WorkingDir, '\x00') {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: invalid working_dir", j.Name))
			return
		}
		// Workflow-job inputs may carry a `from: "<upstream>.<output>"`
		// reference — enable that path in the shared validator. Outputs
		// still take neither URI nor From (the runtime assigns URI).
		if msg := validateArtifactBindingsCtx("inputs", j.Inputs, true, true); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: %s", j.Name, msg))
			return
		}
		if msg := validateArtifactBindings("outputs", j.Outputs, false); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: %s", j.Name, msg))
			return
		}
		if dup := firstDuplicateBindingName(j.Inputs); dup != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: duplicate input name %q", j.Name, dup))
			return
		}
		if dup := firstDuplicateBindingName(j.Outputs); dup != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: duplicate output name %q", j.Name, dup))
			return
		}
		if msg := validateNodeSelector(j.NodeSelector); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: %s", j.Name, msg))
			return
		}
		// Feature 25 — env-map validation on each child job. Covers the
		// count cap, key/value shape rules, and the dynamic-loader
		// denylist. Child-job env flows verbatim into cpb.Job.Env when
		// the workflow is Started, so validating here is load-bearing —
		// without it a workflow could sneak LD_PRELOAD past the same
		// check that already guards POST /jobs. Per-node exceptions
		// consulted via the Server's parsed rules; each override fires
		// its own audit event.
		envRes := validateEnvMap(j.Env, j.NodeSelector, s.envDenylistExceptions)
		if envRes.Err != "" {
			if envRes.BlockedKey != "" && s.audit != nil {
				if err := s.audit.Log(r.Context(), audit.EventEnvDenylistReject, actorFromContext(r.Context()), map[string]interface{}{
					"workflow_id": req.ID,
					"job_name":    j.Name,
					"blocked_key": envRes.BlockedKey,
					"dry_run":     dryRun,
				}); err != nil {
					logAuditErr(false, "env_denylist_reject", err)
				}
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: %s", j.Name, envRes.Err))
			return
		}
		for _, k := range envRes.OverriddenKeys {
			if s.audit != nil {
				if err := s.audit.Log(r.Context(), audit.EventEnvDenylistOverride, actorFromContext(r.Context()), map[string]interface{}{
					"workflow_id": req.ID,
					"job_name":    j.Name,
					"env_key":     k,
					"dry_run":     dryRun,
				}); err != nil {
					logAuditErr(false, "env_denylist_override", err)
				}
			}
		}
		// Feature 26 — secret-key declarations on each child. Mirrors
		// the POST /jobs check: every flagged key must exist in the
		// child's Env, no duplicates, no empty entries, count capped.
		if msg := validateSecretKeys(j.Env, j.SecretKeys); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("job %q: %s", j.Name, msg))
			return
		}
	}

	// Convert request to internal types.
	wfJobs := make([]cpb.WorkflowJob, len(req.Jobs))
	for i, j := range req.Jobs {
		wj := cpb.WorkflowJob{
			Name:           j.Name,
			Command:        j.Command,
			Args:           j.Args,
			Env:            j.Env,
			TimeoutSeconds: j.TimeoutSeconds,
			Runtime:        j.Runtime,
			DependsOn:      j.DependsOn,
			Condition:      parseCondition(j.Condition),
			WorkingDir:     j.WorkingDir,
			Inputs:         convertBindings(j.Inputs),
			Outputs:        convertBindings(j.Outputs),
			NodeSelector:   j.NodeSelector,
			SecretKeys:     j.SecretKeys, // feature 26
		}
		if j.Priority != nil {
			wj.Priority = *j.Priority
		}
		wfJobs[i] = wj
	}

	wf := &cpb.Workflow{
		ID:   req.ID,
		Name: req.Name,
		Jobs: wfJobs,
		// Feature 36 — stamp the authoritative feature-35 Principal
		// as owner. Materialised child jobs inherit this at
		// Start()-time (see cluster/workflow_submit.go).
		OwnerPrincipal: principal.FromContext(r.Context()).ID,
	}
	if req.Priority != nil {
		wf.Priority = *req.Priority
	}

	// Feature 37 — gate workflow creation on ActionWrite. Kind=node
	// and Kind=anonymous principals are refused; users/operators
	// pass because they just stamped themselves as owner. This
	// check must run BEFORE the dry-run path so a dry-run probe
	// from a node-JWT produces a consistent 403.
	if !s.authzCheck(w, r, authz.ActionWrite,
		authz.WorkflowResource(wf.ID, wf.OwnerPrincipal)) {
		return
	}

	// Feature 24 — dry-run short-circuit for workflows. We still need
	// DAG validation to fire (cycles, unknown deps, unknown `from`
	// references), but we must NOT persist, NOT materialise jobs, and
	// NOT emit the workflow_submit audit event. Call cluster.ValidateDAG
	// directly — same validator WorkflowStore.Submit uses internally.
	// Duplicate-ID conflicts are deliberately NOT checked on the dry-run
	// path: a dry-run doesn't reserve the ID, so the same ID could be
	// submitted for real afterwards. Surfacing 409 here would just leak
	// whether an ID exists, adding noise without value.
	if dryRun {
		if err := cluster.ValidateDAG(wf.Jobs); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.audit != nil {
			dryDetails := map[string]interface{}{
				"workflow_id": wf.ID,
				"name":        wf.Name,
				"job_count":   len(wf.Jobs),
			}
			if wf.OwnerPrincipal != "" {
				dryDetails["resource_owner"] = wf.OwnerPrincipal // Feature 36
			}
			if err := s.audit.Log(r.Context(), audit.EventWorkflowDryRun, actorFromContext(r.Context()), dryDetails); err != nil {
				logAuditErr(false, "workflow.dry_run", err)
			}
		}
		s.recordSubmission(r, "workflow", wf.ID, true, true, "") // feature 28
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeJSON(w, "handleSubmitWorkflow.dry_run", dryRunResponse(workflowToResponse(wf, nil)))
		return
	}

	// Submit validates DAG and persists.
	if err := s.workflowStore.Submit(r.Context(), wf); err != nil {
		if errors.Is(err, cluster.ErrWorkflowExists) {
			writeError(w, http.StatusConflict, "workflow with this id already exists")
			return
		}
		if errors.Is(err, cluster.ErrDAGCycle) ||
			errors.Is(err, cluster.ErrDAGUnknownDep) ||
			errors.Is(err, cluster.ErrDAGDuplicateName) ||
			errors.Is(err, cluster.ErrDAGEmptyName) ||
			errors.Is(err, cluster.ErrDAGSelfDep) ||
			errors.Is(err, cluster.ErrDAGUnknownFrom) ||
			errors.Is(err, cluster.ErrDAGFromNotAncestor) ||
			errors.Is(err, cluster.ErrDAGFromUnknownOutput) ||
			errors.Is(err, cluster.ErrDAGFromConditionUnreachable) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("workflow submit failed", slog.String("workflow_id", wf.ID), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "workflow submission failed")
		return
	}

	// Start the workflow — create jobs and transition to running.
	if err := s.workflowStore.Start(r.Context(), wf.ID, s.workflowJobStore); err != nil {
		slog.Error("workflow start failed", slog.String("workflow_id", wf.ID), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "workflow start failed")
		return
	}

	// Re-read the workflow to get the updated state with job IDs.
	wf, _ = s.workflowStore.Get(wf.ID)

	s.recordSubmission(r, "workflow", wf.ID, false, true, "") // feature 28
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, "handleSubmitWorkflow", workflowToResponse(wf, s.workflowJobStore))
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	wf, err := s.workflowStore.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	// Feature 37 — first time this endpoint has any per-workflow
	// RBAC. Pre-37, any authenticated user could read any
	// workflow. Now: admin OR workflow owner. Feature 38 — the
	// share list is also honoured via rule 6b.
	if !s.authzCheck(w, r, authz.ActionRead,
		authz.WorkflowResource(wf.ID, wf.OwnerPrincipal, wf.Shares)) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetWorkflow", workflowToResponse(wf, s.workflowJobStore))
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
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

	all := s.workflowStore.List()

	// Sort newest first so recently submitted workflows appear on page 1.
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	// Feature 37 — filter per-row via authz.Allow(ActionRead).
	// Matches the handleListJobs strategy; see that comment for
	// the scope-push-down tradeoff. Per-row denials do NOT
	// audit — the filter is expected behaviour, not a
	// security event.
	p := principal.FromContext(r.Context())
	permitted := make([]*cpb.Workflow, 0, len(all))
	for _, wf := range all {
		if authz.Allow(p, authz.ActionRead,
			authz.WorkflowResource(wf.ID, wf.OwnerPrincipal, wf.Shares)) == nil {
			permitted = append(permitted, wf)
		}
	}
	total := len(permitted)

	// Paginate.
	start := (page - 1) * size
	if start >= total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	pageItems := permitted[start:end]

	responses := make([]WorkflowResponse, len(pageItems))
	for i, wf := range pageItems {
		responses[i] = workflowToResponse(wf, s.workflowJobStore)
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleListWorkflows", WorkflowListResponse{
		Workflows: responses,
		Total:     total,
		Page:      page,
		Size:      size,
	})
}

func (s *Server) handleCancelWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Feature 37 — fetch the workflow first so the authz
	// decision sees the authoritative owner. Pre-37 had no
	// per-workflow RBAC on cancel; any authenticated user
	// could cancel any workflow.
	wf, err := s.workflowStore.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	if !s.authzCheck(w, r, authz.ActionCancel,
		authz.WorkflowResource(wf.ID, wf.OwnerPrincipal, wf.Shares)) {
		return
	}

	if err := s.workflowStore.Cancel(r.Context(), id, s.workflowJobStore); err != nil {
		if errors.Is(err, cluster.ErrWorkflowNotFound) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		if errors.Is(err, cluster.ErrWorkflowAlreadyTerminal) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Error("workflow cancel failed", slog.String("workflow_id", id), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "workflow cancellation failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleCancelWorkflow", map[string]interface{}{
		"id":      id,
		"status":  "cancelled",
		"message": "workflow cancelled successfully",
	})
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func parseCondition(s string) cpb.DependencyCondition {
	switch strings.ToLower(s) {
	case "on_failure":
		return cpb.DependencyOnFailure
	case "on_complete":
		return cpb.DependencyOnComplete
	default:
		return cpb.DependencyOnSuccess
	}
}

// workflowJobStore is the interface for looking up individual job statuses
// when building workflow responses. Defined here to avoid importing the
// full cluster.JobStore.
type workflowJobStoreIface interface {
	Get(jobID string) (*cpb.Job, error)
}

func workflowToResponse(wf *cpb.Workflow, jobs workflowJobStoreIface) WorkflowResponse {
	resp := WorkflowResponse{
		ID:             wf.ID,
		Name:           wf.Name,
		Status:         wf.Status.String(),
		CreatedAt:      wf.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Error:          wf.Error,
		OwnerPrincipal: wf.OwnerPrincipal, // Feature 36
	}
	if !wf.StartedAt.IsZero() {
		resp.StartedAt = wf.StartedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if !wf.FinishedAt.IsZero() {
		resp.FinishedAt = wf.FinishedAt.Format("2006-01-02T15:04:05Z07:00")
	}

	resp.Jobs = make([]WorkflowJobResponse, len(wf.Jobs))
	for i, wj := range wf.Jobs {
		jr := WorkflowJobResponse{
			Name:           wj.Name,
			Command:        wj.Command,
			Args:           wj.Args,
			// Feature 26 — redact the child's env on the way out.
			// Stored record keeps plaintext (runtime needs it);
			// response strips values whose key is flagged secret.
			Env:            redactSecretEnv(wj.Env, wj.SecretKeys),
			SecretKeys:     wj.SecretKeys,
			TimeoutSeconds: wj.TimeoutSeconds,
			DependsOn:      wj.DependsOn,
			Condition:      wj.Condition.String(),
			JobID:          wj.JobID,
		}
		// Look up the real job status if available.
		if jobs != nil && wj.JobID != "" {
			if j, err := jobs.Get(wj.JobID); err == nil {
				jr.JobStatus = j.Status.String()
			}
		}
		resp.Jobs[i] = jr
	}

	return resp
}
