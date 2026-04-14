// internal/api/server.go
//
// Helion coordinator HTTP API server.
//
// Wires together all HTTP handlers, middleware, and WebSocket upgrader into a
// single Server value. Routes are registered in registerRoutes.
//
// File layout
// ───────────
//   server.go          — Server struct, NewServer, Serve, Shutdown
//   types.go           — request/response types and store/provider interfaces
//   middleware.go      — authMiddleware, wsAuthMiddleware, adminMiddleware, rate limiting
//   helpers.go         — writeError, jobToResponse, logAuditErr
//   handlers_jobs.go   — POST /jobs, GET /jobs/{id}, GET /jobs
//   handlers_cluster.go — GET /healthz, /readyz, /nodes, /metrics, /audit
//   handlers_admin.go  — POST/DELETE /admin/tokens, POST /admin/nodes/{id}/revoke
//   handlers_ws.go     — GET /ws/jobs/{id}/logs, GET /ws/metrics
//   stubs.go           — NewStubNodeRegistry, NewStubMetricsProvider (dev/test)
//   adapters.go        — JobStoreAdapter (wraps cluster.JobStore for API layer)
//
// Routes
// ──────
//   Public (no auth):
//     GET  /healthz
//     GET  /readyz
//     GET  /metrics          (Prometheus scrape target when promHandler is set)
//
//   Authenticated:
//     POST /jobs
//     GET  /jobs/{id}
//     GET  /jobs
//     GET  /nodes
//     GET  /metrics          (JSON fallback when Prometheus handler not set)
//     GET  /audit
//     POST   /admin/nodes/{id}/revoke
//     POST   /admin/tokens
//     DELETE /admin/tokens/{jti}
//
//   WebSocket (JWT via query param or Authorization header):
//     GET /ws/jobs/{id}/logs
//     GET /ws/metrics

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/logstore"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

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
	workflowStore    *cluster.WorkflowStore // nil if workflow support not enabled
	workflowJobStore *cluster.JobStore      // needed to look up individual job statuses
	eventBus         *events.Bus            // nil if event system not enabled
	logStore         logstore.Store         // nil if log storage not enabled
	analyticsDB      AnalyticsDB            // nil if analytics not enabled
	promHandler      http.Handler           // Prometheus /metrics handler; nil disables
	mux              *http.ServeMux
	httpSrvMu      sync.Mutex
	httpSrv        *http.Server
	upgrader       websocket.Upgrader

	// AUDIT H2 (fixed): disableAuth must be set explicitly — usually only by
	// test helpers — to allow a nil tokenManager to pass auth. In production
	// the coordinator builds a real tokenManager and never sets this flag, so
	// any nil-tokenManager path now returns 500 instead of silently letting
	// every request through the middleware.
	disableAuth bool

	// tokenIssueMu protects tokenIssueLimiters.
	tokenIssueMu       sync.Mutex
	tokenIssueLimiters map[string]*rate.Limiter // keyed by admin subject

	// analyticsMu protects analyticsLimiters. Per-subject rate limiter on
	// the /api/analytics/* endpoints to prevent DoS via expensive queries
	// (PERCENTILE_CONT, date-range scans on job_summary).
	analyticsMu       sync.Mutex
	analyticsLimiters map[string]*rate.Limiter

	// registryMu protects registryLimiters. Per-subject rate limiter on
	// /api/datasets and /api/models. Registry writes are cheap (BadgerDB
	// single-key writes), but unbounded register rates would let an
	// authenticated user pollute the audit stream or chew through disk
	// on the shared BadgerDB. Matches analytics shape for consistency.
	registryMu       sync.Mutex
	registryLimiters map[string]*rate.Limiter

	// Dataset / model persistence — nil until SetRegistryStore is called
	// (coordinator wiring). Handlers return 404 when these are nil so a
	// node-only deployment without the registry enabled doesn't expose
	// phantom endpoints.
	datasets registry.DatasetStore
	models   registry.ModelStore
}

// DisableAuth turns off authentication for this Server. Intended ONLY for
// tests and developer tooling that construct a Server with a nil
// tokenManager. Never call this from production code: doing so removes the
// compile-time safety that AUDIT H2 restored.
func (s *Server) DisableAuth() {
	s.disableAuth = true
}

// SetLogStore enables job log retrieval via GET /jobs/{id}/logs.
func (s *Server) SetLogStore(ls logstore.Store) {
	s.logStore = ls
	s.mux.HandleFunc("GET /jobs/{id}/logs", s.authMiddleware(s.handleGetJobLogs))
}

// SetEventBus enables the real-time event stream endpoint /ws/events.
func (s *Server) SetEventBus(bus *events.Bus) {
	s.eventBus = bus
	s.mux.HandleFunc("GET /ws/events", s.handleEventStream)
}

// SetWorkflowStore enables workflow support by injecting the WorkflowStore
// and the JobStore used to look up individual job statuses. Must be called
// before Serve. Registers the workflow routes on the mux.
func (s *Server) SetWorkflowStore(ws *cluster.WorkflowStore, jobs *cluster.JobStore) {
	s.workflowStore = ws
	s.workflowJobStore = jobs
	s.mux.HandleFunc("POST /workflows", s.authMiddleware(s.handleSubmitWorkflow))
	s.mux.HandleFunc("GET /workflows/{id}", s.authMiddleware(s.handleGetWorkflow))
	s.mux.HandleFunc("GET /workflows", s.authMiddleware(s.handleListWorkflows))
	s.mux.HandleFunc("DELETE /workflows/{id}", s.authMiddleware(s.handleCancelWorkflow))
}

// SetRegistryStore enables the dataset + model registry endpoints by
// injecting a registry.Store. Must be called before Serve. Registers
// the /api/datasets and /api/models routes on the mux. Callers that
// don't enable the registry get 404 from the handlers (via the
// registryConfigured guard) so the endpoints are invisible on
// deployments that opted out.
func (s *Server) SetRegistryStore(store registry.Store) {
	s.datasets = store
	s.models = store
	s.mux.HandleFunc("POST /api/datasets", s.authMiddleware(s.handleRegisterDataset))
	s.mux.HandleFunc("GET /api/datasets", s.authMiddleware(s.handleListDatasets))
	s.mux.HandleFunc("GET /api/datasets/{name}/{version}", s.authMiddleware(s.handleGetDataset))
	s.mux.HandleFunc("DELETE /api/datasets/{name}/{version}", s.authMiddleware(s.handleDeleteDataset))
	s.mux.HandleFunc("POST /api/models", s.authMiddleware(s.handleRegisterModel))
	s.mux.HandleFunc("GET /api/models", s.authMiddleware(s.handleListModels))
	// `/latest` lives before `/{name}/{version}` so the Go ServeMux's
	// most-specific-pattern-wins rule doesn't accidentally route
	// `GET /api/models/mymodel/latest` to the version handler.
	s.mux.HandleFunc("GET /api/models/{name}/latest", s.authMiddleware(s.handleLatestModel))
	s.mux.HandleFunc("GET /api/models/{name}/{version}", s.authMiddleware(s.handleGetModel))
	s.mux.HandleFunc("DELETE /api/models/{name}/{version}", s.authMiddleware(s.handleDeleteModel))
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
		jobs:               jobs,
		nodes:              nodes,
		metrics:            metrics,
		audit:              auditLog,
		tokenManager:       tokenMgr,
		rateLimiter:        rateLim,
		readiness:          readiness,
		promHandler:        promHandler,
		mux:                http.NewServeMux(),
		tokenIssueLimiters: make(map[string]*rate.Limiter),
		analyticsLimiters:  make(map[string]*rate.Limiter),
		registryLimiters:   make(map[string]*rate.Limiter),
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
	s.mux.HandleFunc("POST /jobs/{id}/cancel", s.authMiddleware(s.handleCancelJob))
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

	// AUDIT 2026-04-12/H2 (fixed): WebSocket endpoints authenticate via
	// first-message pattern instead of URL query parameters. The connection is
	// upgraded without auth; the first frame must be {"type":"auth","token":"..."}.
	// This keeps JWTs out of server access logs and browser history.
	s.mux.HandleFunc("GET /ws/jobs/{id}/logs", s.handleJobLogStream)
	s.mux.HandleFunc("GET /ws/metrics", s.handleMetricsStream)
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
	// AUDIT L6 (fixed): IdleTimeout prevents keep-alive connections from being held
	// open indefinitely, limiting the resource impact of slow or idle clients.
	// AUDIT 2026-04-12/L1 (fixed): ReadHeaderTimeout limits how long the
	// server waits for request headers, countering Slowloris-style attacks
	// that trickle headers one byte at a time to hold connection slots open.
	hsrv := &http.Server{
		Handler:           s.mux,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
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
