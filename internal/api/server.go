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
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
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
	promHandler    http.Handler // Prometheus /metrics handler; nil disables
	mux            *http.ServeMux
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
}

// DisableAuth turns off authentication for this Server. Intended ONLY for
// tests and developer tooling that construct a Server with a nil
// tokenManager. Never call this from production code: doing so removes the
// compile-time safety that AUDIT H2 restored.
func (s *Server) DisableAuth() {
	s.disableAuth = true
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
	// AUDIT L6 (fixed): IdleTimeout prevents keep-alive connections from being held
	// open indefinitely, limiting the resource impact of slow or idle clients.
	hsrv := &http.Server{
		Handler:      s.mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
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
