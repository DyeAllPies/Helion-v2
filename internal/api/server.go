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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── context keys ──────────────────────────────────────────────────────────────

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	claimsContextKey contextKey = "claims"
)

// ── request / response types ─────────────────────────────────────────────────

// ResourceLimits is the optional cgroup v2 constraint block in SubmitRequest / JobResponse.
// All fields default to 0 (no limit). Enforced only when using the Rust runtime.
type ResourceLimits struct {
	MemoryBytes uint64 `json:"memory_bytes,omitempty"` // maximum RSS in bytes
	CPUQuotaUS  uint64 `json:"cpu_quota_us,omitempty"` // CPU quota per period in microseconds
	CPUPeriodUS uint64 `json:"cpu_period_us,omitempty"` // period in microseconds (default 100000)
}

// SubmitRequest is the JSON body for POST /jobs.
type SubmitRequest struct {
	ID             string            `json:"id"`              // client-generated; required
	Command        string            `json:"command"`         // required
	Args           []string          `json:"args"`            // optional
	Env            map[string]string `json:"env,omitempty"`   // optional key-value environment variables
	TimeoutSeconds int64             `json:"timeout_seconds"` // optional; 0 means no limit
	Limits         ResourceLimits    `json:"limits,omitempty"` // optional cgroup v2 resource limits
}

// JobResponse is the JSON body returned by POST /jobs and GET /jobs/{id}.
type JobResponse struct {
	ID             string            `json:"id"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	Limits         ResourceLimits    `json:"limits,omitempty"`
	Status         string            `json:"status"`
	NodeID         string            `json:"node_id,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	Error          string            `json:"error,omitempty"`
}

// ErrorResponse is the JSON body for error responses.
type ErrorResponse struct {
	Error string `json:"error"`
}

// NodeListResponse is the response for GET /nodes.
type NodeListResponse struct {
	Nodes []NodeInfo `json:"nodes"`
	Total int        `json:"total"`
}

// NodeInfo contains information about a registered node.
type NodeInfo struct {
	ID          string    `json:"id"`
	Health      string    `json:"health"` // "healthy" | "unhealthy"
	LastSeen    time.Time `json:"last_seen"`
	RunningJobs int       `json:"running_jobs"`
	Address     string    `json:"address"`
}

// JobListResponse is the response for GET /jobs (paginated).
type JobListResponse struct {
	Jobs  []JobResponse `json:"jobs"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// ClusterMetrics is the response for GET /metrics.
type ClusterMetrics struct {
	Nodes struct {
		Total   int `json:"total"`
		Healthy int `json:"healthy"`
	} `json:"nodes"`
	Jobs struct {
		Running   int `json:"running"`
		Pending   int `json:"pending"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
		Total     int `json:"total"`
	} `json:"jobs"`
	Timestamp time.Time `json:"timestamp"`
}

// AuditListResponse is the response for GET /audit.
type AuditListResponse struct {
	Events []audit.Event `json:"events"`
	Total  int           `json:"total"`
	Page   int           `json:"page"`
	Size   int           `json:"size"`
}

// IssueTokenRequest is the body for POST /admin/tokens.
type IssueTokenRequest struct {
	Subject  string `json:"subject"`   // required; e.g. "alice"
	Role     string `json:"role"`      // required; "admin" or "node"
	TTLHours int    `json:"ttl_hours"` // optional; defaults to 8 h; max 720 h (30 days)
}

// IssueTokenResponse is the response for POST /admin/tokens.
type IssueTokenResponse struct {
	Token   string `json:"token"`
	Subject string `json:"subject"`
	Role    string `json:"role"`
	TTLHours int   `json:"ttl_hours"`
}

// RevokeTokenRequest is the optional body for DELETE /admin/tokens/{jti}.
type RevokeTokenRequest struct{}

// RevokeTokenResponse is the response for DELETE /admin/tokens/{jti}.
type RevokeTokenResponse struct {
	Revoked bool   `json:"revoked"`
	JTI     string `json:"jti"`
}

// RevokeNodeRequest is the request body for POST /admin/nodes/{id}/revoke.
type RevokeNodeRequest struct {
	Reason string `json:"reason"`
}

// RevokeNodeResponse is the response for POST /admin/nodes/{id}/revoke.
type RevokeNodeResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ── JobStore interface ────────────────────────────────────────────────────────

// JobStoreIface is the narrow interface the HTTP server needs from the JobStore.
type JobStoreIface interface {
	Submit(ctx context.Context, j *cpb.Job) error
	Get(jobID string) (*cpb.Job, error)
	List(ctx context.Context, statusFilter string, page, size int) ([]*cpb.Job, int, error)
	GetJobsByStatus(ctx context.Context, status string) ([]*cpb.Job, error)
}

// NodeRegistryIface is the interface for node operations.
type NodeRegistryIface interface {
	ListNodes(ctx context.Context) ([]NodeInfo, error)
	GetNodeHealth(nodeID string) (string, time.Time, error)
	GetRunningJobCount(nodeID string) int
	RevokeNode(ctx context.Context, nodeID, reason string) error
}

// MetricsProvider computes cluster metrics.
type MetricsProvider interface {
	GetClusterMetrics(ctx context.Context) (*ClusterMetrics, error)
}

// ReadinessChecker reports whether the coordinator is ready to serve traffic.
// Both conditions must pass for /readyz to return 200:
//   - Ping: BadgerDB is open and can execute transactions
//   - RegistryLen > 0: at least one node has registered
type ReadinessChecker interface {
	Ping() error
	RegistryLen() int
}

// ── Server ────────────────────────────────────────────────────────────────────

// Server is the coordinator's HTTP API server.
type Server struct {
	jobs           JobStoreIface
	nodes          NodeRegistryIface
	metrics        MetricsProvider
	audit          *audit.Logger
	tokenManager   *auth.TokenManager
	rateLimiter    *ratelimit.NodeLimiter
	readiness      ReadinessChecker
	promHandler    http.Handler // Prometheus /metrics handler; nil disables
	mux            *http.ServeMux
	httpSrvMu      sync.Mutex
	httpSrv        *http.Server
	upgrader       websocket.Upgrader
}

// NewServer creates an HTTP API server with all Phase 3/4 components.
func NewServer(
	jobs JobStoreIface,
	nodes NodeRegistryIface,
	metrics MetricsProvider,
	auditLog *audit.Logger,
	tokenMgr *auth.TokenManager,
	rateLim *ratelimit.NodeLimiter,
	readiness ReadinessChecker,
	promHandler http.Handler,
) *Server {
	s := &Server{
		jobs:         jobs,
		nodes:        nodes,
		metrics:      metrics,
		audit:        auditLog,
		tokenManager: tokenMgr,
		rateLimiter:  rateLim,
		readiness:    readiness,
		promHandler:  promHandler,
		mux:          http.NewServeMux(),
		upgrader: websocket.Upgrader{
			// Reject cross-origin WebSocket connections. Browsers always send an
			// Origin header on WebSocket upgrades; we compare its host component
			// against the request Host so that only same-origin pages can connect.
			// curl / native clients that omit Origin are allowed through.
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				u, err := url.Parse(origin)
				if err != nil {
					return false
				}
				return u.Host == r.Host
			},
		},
	}
	
	// Register routes
	s.registerRoutes()
	
	return s
}

// registerRoutes sets up all HTTP endpoints with authentication middleware.
func (s *Server) registerRoutes() {
	// Public endpoints (no auth)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	
	// Authenticated endpoints
	s.mux.HandleFunc("POST /jobs", s.authMiddleware(s.handleSubmitJob))
	s.mux.HandleFunc("GET /jobs/{id}", s.authMiddleware(s.handleGetJob))
	s.mux.HandleFunc("GET /jobs", s.authMiddleware(s.handleListJobs))
	s.mux.HandleFunc("GET /nodes", s.authMiddleware(s.handleListNodes))
	// /metrics serves Prometheus text format — no auth so scrapers work without tokens.
	// Falls back to JSON snapshot if no Prometheus handler was injected (tests/dev).
	if s.promHandler != nil {
		s.mux.Handle("GET /metrics", s.promHandler)
	} else {
		s.mux.HandleFunc("GET /metrics", s.authMiddleware(s.handleGetMetrics))
	}
	s.mux.HandleFunc("GET /audit", s.authMiddleware(s.handleGetAudit))
	s.mux.HandleFunc("POST /admin/nodes/{id}/revoke", s.authMiddleware(s.handleRevokeNode))
	s.mux.HandleFunc("POST /admin/tokens", s.authMiddleware(s.adminMiddleware(s.handleIssueToken)))
	s.mux.HandleFunc("DELETE /admin/tokens/{jti}", s.authMiddleware(s.adminMiddleware(s.handleRevokeToken)))
	
	// WebSocket endpoints (auth via query param or header)
	s.mux.HandleFunc("GET /ws/jobs/{id}/logs", s.wsAuthMiddleware(s.handleJobLogStream))
	s.mux.HandleFunc("GET /ws/metrics", s.wsAuthMiddleware(s.handleMetricsStream))
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Serve starts listening on addr. Blocks until the server is closed.
// Returns http.ErrServerClosed on graceful shutdown — callers should treat
// that as a clean exit.
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("api.Server listen %s: %w", addr, err)
	}
	hsrv := &http.Server{
		Handler:      s.mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	s.httpSrvMu.Lock()
	s.httpSrv = hsrv
	s.httpSrvMu.Unlock()
	return hsrv.Serve(lis)
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.httpSrvMu.Lock()
	hsrv := s.httpSrv
	s.httpSrvMu.Unlock()
	if hsrv == nil {
		return nil
	}
	return hsrv.Shutdown(ctx)
}

// ── handlers ─────────────────────────────────────────────────────────────────

// authMiddleware validates JWT Bearer tokens and injects claims into context.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If tokenManager is nil, authentication is disabled (for tests/development)
		if s.tokenManager == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Extract token from Authorization header
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			if s.audit != nil {
				if err := s.audit.LogAuthFailure(r.Context(), "missing authorization header", r.RemoteAddr); err != nil {
					slog.Warn("audit log failed", slog.Any("err", err))
				}
			}
			writeError(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Validate token
		claims, err := s.tokenManager.ValidateToken(r.Context(), token)
		if err != nil {
			if s.audit != nil {
				if aerr := s.audit.LogAuthFailure(r.Context(), err.Error(), r.RemoteAddr); aerr != nil {
					slog.Warn("audit log failed", slog.Any("err", aerr))
				}
			}
			slog.Error("token validation failed", slog.String("remote", r.RemoteAddr), slog.Any("err", err))
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}

		// Store claims in request context
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// wsAuthMiddleware validates JWT for WebSocket connections (token in query param).
func (s *Server) wsAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If tokenManager is nil, authentication is disabled
		if s.tokenManager == nil {
			next.ServeHTTP(w, r)
			return
		}

		// For WebSocket, token can be in query param (browsers can't set headers in EventSource/WS)
		token := r.URL.Query().Get("token")
		if token == "" {
			// Fall back to Authorization header
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if token == "" {
			if s.audit != nil {
				if err := s.audit.LogAuthFailure(r.Context(), "missing token", r.RemoteAddr); err != nil {
					slog.Warn("audit log failed", slog.Any("err", err))
				}
			}
			http.Error(w, "unauthorized: missing token", http.StatusUnauthorized)
			return
		}

		claims, err := s.tokenManager.ValidateToken(r.Context(), token)
		if err != nil {
			if s.audit != nil {
				if aerr := s.audit.LogAuthFailure(r.Context(), err.Error(), r.RemoteAddr); aerr != nil {
					slog.Warn("audit log failed", slog.Any("err", aerr))
				}
			}
			slog.Error("ws token validation failed", slog.String("remote", r.RemoteAddr), slog.Any("err", err))
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

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

	if err := s.jobs.Submit(r.Context(), job); err != nil {
		slog.Error("job submit failed", slog.String("job_id", job.ID), slog.Any("err", err))
		// A duplicate ID surfaces as a persist error; treat it as 409.
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
			slog.Warn("audit log failed", slog.String("job_id", job.ID), slog.Any("err", err))
		}
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

// validJobStatuses is the set of status strings accepted by the ?status= query
// parameter. Values are uppercase to match the underlying store convention.
var validJobStatuses = map[string]bool{
	"UNKNOWN": true, "PENDING": true, "DISPATCHING": true, "RUNNING": true,
	"COMPLETED": true, "FAILED": true, "TIMEOUT": true, "LOST": true,
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
	_ = json.NewEncoder(w).Encode(resp)
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

func (s *Server) handleRevokeNode(w http.ResponseWriter, r *http.Request) {
	if s.nodes == nil {
		writeError(w, http.StatusNotImplemented, "node registry not configured")
		return
	}

	nodeID := r.PathValue("id")
	if nodeID == "" {
		nodeID = strings.TrimPrefix(r.URL.Path, "/admin/nodes/")
		nodeID = strings.TrimSuffix(nodeID, "/revoke")
	}
	
	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "node id required")
		return
	}
	
	var req RevokeNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Reason is optional, use default
		req.Reason = "manual revocation"
	}
	
	// Get actor from claims (if auth is enabled)
	actor := "system"
	if s.tokenManager != nil {
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			actor = claims.Subject
		}
	}
	
	// Revoke the node
	if err := s.nodes.RevokeNode(r.Context(), nodeID, req.Reason); err != nil {
		slog.Error("revoke node failed", slog.String("node_id", nodeID), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	
	// Log revocation (if audit is enabled)
	if s.audit != nil {
		if err := s.audit.LogNodeRevoke(r.Context(), actor, nodeID, req.Reason); err != nil {
			slog.Warn("audit log failed", slog.String("node_id", nodeID), slog.Any("err", err))
		}
	}
	
	resp := RevokeNodeResponse{
		Success: true,
		Message: fmt.Sprintf("node %s revoked", nodeID),
	}
	
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleJobLogStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		jobID = strings.TrimPrefix(r.URL.Path, "/ws/jobs/")
		jobID = strings.TrimSuffix(jobID, "/logs")
	}
	
	// Upgrade to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Connection already hijacked, can't write error response
		return
	}
	defer conn.Close()
	
	// For Phase 4 initial implementation, we'll send a placeholder message
	// Full implementation requires job log streaming from node agents
	msg := map[string]interface{}{
		"type":    "info",
		"message": fmt.Sprintf("Log streaming for job %s (placeholder)", jobID),
		"timestamp": time.Now(),
	}
	
	_ = conn.WriteJSON(msg)
	
	// Keep connection alive for 30 seconds (demo)
	time.Sleep(30 * time.Second)
}

func (s *Server) handleMetricsStream(w http.ResponseWriter, r *http.Request) {
	// Upgrade to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			metrics, err := s.metrics.GetClusterMetrics(r.Context())
			if err != nil {
				return
			}
			
			if err := conn.WriteJSON(metrics); err != nil {
				return // Client disconnected
			}
			
		case <-r.Context().Done():
			return
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func jobToResponse(j *cpb.Job) JobResponse {
	resp := JobResponse{
		ID:             j.ID,
		Command:        j.Command,
		Args:           j.Args,
		Env:            j.Env,
		TimeoutSeconds: j.TimeoutSeconds,
		Limits: ResourceLimits{
			MemoryBytes: j.Limits.MemoryBytes,
			CPUQuotaUS:  j.Limits.CPUQuotaUS,
			CPUPeriodUS: j.Limits.CPUPeriodUS,
		},
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

// ── Token admin endpoints ─────────────────────────────────────────────────────

const (
	maxTokenTTLHours     = 720 // 30 days
	defaultTokenTTLHours = 8
)

var validRoles = map[string]bool{"admin": true, "node": true}

// adminMiddleware rejects requests whose JWT role is not "admin".
// Must be composed inside authMiddleware so claims are already in context.
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.tokenManager != nil {
			claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
			if !ok || claims.Role != "admin" {
				writeError(w, http.StatusForbidden, "admin role required")
				return
			}
		}
		next.ServeHTTP(w, r)
	}
}

// handleIssueToken handles POST /admin/tokens.
// Issues a short-lived scoped JWT for the given subject and role.
func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	if s.tokenManager == nil {
		writeError(w, http.StatusNotImplemented, "token manager not configured")
		return
	}

	var req IssueTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Subject == "" {
		writeError(w, http.StatusBadRequest, "subject is required")
		return
	}
	if !validRoles[req.Role] {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'node'")
		return
	}

	ttl := req.TTLHours
	if ttl <= 0 {
		ttl = defaultTokenTTLHours
	}
	if ttl > maxTokenTTLHours {
		writeError(w, http.StatusBadRequest, "ttl_hours must not exceed 720 (30 days)")
		return
	}

	token, err := s.tokenManager.GenerateToken(r.Context(), req.Subject, req.Role, time.Duration(ttl)*time.Hour)
	if err != nil {
		slog.Error("issue token failed", slog.String("subject", req.Subject), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}

	if s.audit != nil {
		actor := "system"
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			actor = claims.Subject
		}
		_ = s.audit.Log(r.Context(), "token.issued", actor, map[string]interface{}{
			"subject":   req.Subject,
			"role":      req.Role,
			"ttl_hours": ttl,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(IssueTokenResponse{
		Token:    token,
		Subject:  req.Subject,
		Role:     req.Role,
		TTLHours: ttl,
	})
}

// handleRevokeToken handles DELETE /admin/tokens/{jti}.
// Immediately invalidates a token by deleting its JTI from BadgerDB.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if s.tokenManager == nil {
		writeError(w, http.StatusNotImplemented, "token manager not configured")
		return
	}

	jti := r.PathValue("jti")
	if jti == "" {
		jti = strings.TrimPrefix(r.URL.Path, "/admin/tokens/")
	}
	if jti == "" {
		writeError(w, http.StatusBadRequest, "jti is required")
		return
	}

	if err := s.tokenManager.RevokeToken(r.Context(), jti); err != nil {
		slog.Error("revoke token failed", slog.String("jti", jti), slog.Any("err", err))
		writeError(w, http.StatusInternalServerError, "revocation failed")
		return
	}

	if s.audit != nil {
		actor := "system"
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims); ok {
			actor = claims.Subject
		}
		_ = s.audit.Log(r.Context(), "token.revoked", actor, map[string]interface{}{
			"jti":    jti,
			"reason": "explicit revocation",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(RevokeTokenResponse{Revoked: true, JTI: jti})
}
