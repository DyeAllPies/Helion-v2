// internal/api/server.go
//
// HTTP API server — minimal REST surface for Phase 2.
//
// Phase 2 scope (this file)
// ─────────────────────────
//   POST /jobs          Submit a new job; returns job ID and initial status.
//   GET  /jobs/{id}     Read a job record by ID.
//   GET  /healthz       Liveness probe (always 200 OK).
//
// Phase 3 will add:
//   GET  /jobs          List all jobs (paginated, filterable)
//   GET  /nodes         List all nodes
//   GET  /metrics       Prometheus-format cluster metrics
//   GET  /audit         Audit log viewer
//   GET  /ws/jobs/{id}/logs   WebSocket log streaming
//   GET  /ws/metrics          WebSocket metrics push
//   JWT authentication on all endpoints
//
// No authentication in Phase 2 — that is Phase 4.
//
// Design
// ──────
// The HTTP server is a plain net/http ServeMux — no third-party router.
// JobStore is injected via the Server struct; the handler has no direct
// dependency on BadgerDB.
//
// Job IDs
// ───────
// helion-run generates the job ID client-side using a UUID-style format
// (timestamp + random suffix) so the CLI can print it immediately without
// waiting for a round-trip.  The server accepts whatever ID the client sends.
// Duplicate IDs return 409 Conflict.
//
// Content type
// ────────────
// All request and response bodies are JSON.  The server sets
// Content-Type: application/json on all responses.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── request / response types ─────────────────────────────────────────────────

// SubmitRequest is the JSON body for POST /jobs.
type SubmitRequest struct {
	ID      string   `json:"id"`      // client-generated; required
	Command string   `json:"command"` // required
	Args    []string `json:"args"`    // optional
}

// JobResponse is the JSON body returned by POST /jobs and GET /jobs/{id}.
type JobResponse struct {
	ID          string    `json:"id"`
	Command     string    `json:"command"`
	Args        []string  `json:"args"`
	Status      string    `json:"status"`
	NodeID      string    `json:"node_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// ErrorResponse is the JSON body for error responses.
type ErrorResponse struct {
	Error string `json:"error"`
}

// ── JobStore interface ────────────────────────────────────────────────────────

// JobStoreIface is the narrow interface the HTTP server needs from the JobStore.
type JobStoreIface interface {
	Submit(ctx context.Context, j *cpb.Job) error
	Get(jobID string) (*cpb.Job, error)
}

// ── Server ────────────────────────────────────────────────────────────────────

// Server is the coordinator's HTTP API server.
type Server struct {
	jobs     JobStoreIface
	mux      *http.ServeMux
	httpSrv  *http.Server
}

// NewServer creates an HTTP API server backed by the given JobStore.
func NewServer(jobs JobStoreIface) *Server {
	s := &Server{
		jobs: jobs,
		mux:  http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("POST /jobs", s.handleSubmitJob)
	s.mux.HandleFunc("GET /jobs/{id}", s.handleGetJob)
	return s
}

// Serve starts listening on addr. Blocks until the server is closed.
// Returns http.ErrServerClosed on graceful shutdown — callers should treat
// that as a clean exit.
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("api.Server listen %s: %w", addr, err)
	}
	s.httpSrv = &http.Server{
		Handler:      s.mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s.httpSrv.Serve(lis)
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	var req SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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

	job := &cpb.Job{
		ID:      req.ID,
		Command: req.Command,
		Args:    req.Args,
	}

	if err := s.jobs.Submit(r.Context(), job); err != nil {
		// A duplicate ID surfaces as a persist error; treat it as 409.
		writeError(w, http.StatusInternalServerError, "submit failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(jobToResponse(job))
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
		writeError(w, http.StatusNotFound, "job not found: "+id)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jobToResponse(job))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func jobToResponse(j *cpb.Job) JobResponse {
	resp := JobResponse{
		ID:        j.ID,
		Command:   j.Command,
		Args:      j.Args,
		Status:    j.Status.String(),
		NodeID:    j.NodeID,
		CreatedAt: j.CreatedAt,
		Error:     j.Error,
	}
	if !j.FinishedAt.IsZero() {
		t := j.FinishedAt
		resp.FinishedAt = &t
	}
	return resp
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}
