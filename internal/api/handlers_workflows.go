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

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
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
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	DependsOn      []string          `json:"depends_on,omitempty"`
	Condition      string            `json:"condition"`
	JobID          string            `json:"job_id,omitempty"`
	JobStatus      string            `json:"job_status,omitempty"`
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
	}
	if req.Priority != nil {
		wf.Priority = *req.Priority
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
			errors.Is(err, cluster.ErrDAGSelfDep) {
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

	total := len(all)

	// Paginate.
	start := (page - 1) * size
	if start >= total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	pageItems := all[start:end]

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
		ID:        wf.ID,
		Name:      wf.Name,
		Status:    wf.Status.String(),
		CreatedAt: wf.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Error:     wf.Error,
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
			Env:            wj.Env,
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
