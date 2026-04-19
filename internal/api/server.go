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
//   Feature-flagged (registered only when the backing store is injected):
//     POST   /api/datasets                      (SetRegistryStore)
//     GET    /api/datasets, /api/datasets/{name}/{version}
//     DELETE /api/datasets/{name}/{version}
//     POST   /api/models
//     GET    /api/models, /api/models/{name}/latest, /api/models/{name}/{version}
//     DELETE /api/models/{name}/{version}
//     GET    /api/services, /api/services/{job_id}   (SetServiceRegistry; feature 17)
//     GET    /workflows/{id}/lineage                  (SetWorkflowStore + SetRegistryStore; feature 18 DAG view)
//
//   WebSocket (JWT via query param or Authorization header):
//     GET /ws/jobs/{id}/logs
//     GET /ws/metrics

package api

import (
	"context"
	"crypto/tls"
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
	"github.com/DyeAllPies/Helion-v2/internal/groups"
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

	// Feature 26 — per-admin rate limiter on POST /admin/jobs/{id}/reveal-secret.
	// Deliberately tighter than token issuance (see middleware.go comment).
	revealSecretMu       sync.Mutex
	revealSecretLimiters map[string]*rate.Limiter // keyed by admin subject

	// Feature 27 — per-admin rate limiter on POST /admin/operator-certs.
	// Issuance is far more expensive than a token issuance (ECDSA
	// keygen + cert signing + PKCS#12 encode) and the resulting
	// artefact is a long-lived credential; the limiter is on the
	// conservative side.
	issueOpCertMu       sync.Mutex
	issueOpCertLimiters map[string]*rate.Limiter // keyed by admin subject

	// Feature 27 — CA handle used by the operator-cert issuance
	// handler. nil on deployments that don't enable it (the legacy
	// node-only trust path); handler returns 501 in that case.
	operatorCA clientCertIssuer

	// Feature 27 — client-cert enforcement tier. One of off/warn/on.
	// See clientCertMiddleware for semantics.
	clientCertTier clientCertTier

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

	// Feature 17 — inference-service endpoint registry. nil on
	// deployments that don't opt into services; the /api/services/{id}
	// route is only registered by SetServiceRegistry.
	services *cluster.ServiceRegistry

	// Feature 25 — per-node overrides for the env denylist. nil / empty
	// means the denylist is absolute (the safe default). Populated by
	// the coordinator from HELION_ENV_DENYLIST_EXCEPTIONS at startup.
	// Read-only after construction; handlers consult the slice on every
	// submit with no locking because it's never mutated post-Serve.
	envDenylistExceptions []EnvDenylistException

	// Feature 38 — group membership store. Populated via
	// SetGroupsStore during coordinator wiring. When non-nil
	// authMiddleware calls groups.GroupsFor() after stamping
	// the Principal so p.Groups is available to the authz
	// evaluator for share-via-group matching. Nil disables
	// group membership entirely — shares with `group:<name>`
	// grantees are inert, direct `user:<id>` shares still work.
	groups groups.Store
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

// SetGroupsStore wires the feature-38 group-membership store.
// Must be called BEFORE Serve starts dispatching requests so
// authMiddleware can populate Principal.Groups on every
// authenticated call.
//
// Enables the admin endpoints:
//   POST   /admin/groups                          — create
//   GET    /admin/groups                          — list
//   GET    /admin/groups/{name}                   — get
//   DELETE /admin/groups/{name}                   — delete
//   POST   /admin/groups/{name}/members           — add member
//   DELETE /admin/groups/{name}/members/{id...}   — remove member
//
// A nil store disables group membership entirely — shares with
// `group:<name>` grantees become inert, direct shares still
// work. That's acceptable for dev deployments that haven't
// opted into groups, but production should always configure
// a real BadgerStore.
func (s *Server) SetGroupsStore(g groups.Store) {
	s.groups = g
	if g == nil {
		return
	}
	s.mux.HandleFunc("POST /admin/groups", s.authMiddleware(s.adminMiddleware(s.handleCreateGroup)))
	s.mux.HandleFunc("GET /admin/groups", s.authMiddleware(s.adminMiddleware(s.handleListGroups)))
	s.mux.HandleFunc("GET /admin/groups/{name}", s.authMiddleware(s.adminMiddleware(s.handleGetGroup)))
	s.mux.HandleFunc("DELETE /admin/groups/{name}", s.authMiddleware(s.adminMiddleware(s.handleDeleteGroup)))
	s.mux.HandleFunc("POST /admin/groups/{name}/members", s.authMiddleware(s.adminMiddleware(s.handleAddGroupMember)))
	// The member principal ID contains a ':' (e.g. "user:alice")
	// and may contain further colons for subjects like
	// "operator:alice@ops". Use a catch-all path pattern so the
	// entire suffix after "members/" is the principal ID.
	s.mux.HandleFunc("DELETE /admin/groups/{name}/members/{principal...}",
		s.authMiddleware(s.adminMiddleware(s.handleRemoveGroupMember)))

	// Share CRUD — owner-or-admin per-resource. Path:
	//   /admin/resources/{kind}/{id...}/share[/{grantee...}]
	// The `{id...}` is a catch-all so dataset keys like
	// "mnist/1.0.0" (name/version) work without needing two
	// levels of path variable.
	s.mux.HandleFunc("POST /admin/resources/{kind}/share", s.authMiddleware(s.handleCreateShare))
	s.mux.HandleFunc("GET /admin/resources/{kind}/shares", s.authMiddleware(s.handleListShares))
	s.mux.HandleFunc("DELETE /admin/resources/{kind}/share", s.authMiddleware(s.handleRevokeShare))
}

// SetEnvDenylistExceptions installs per-node overrides for the feature-
// 25 env-var denylist. Must be called before Serve — the slice is read
// without locking on every submit, so mutating it at runtime is not
// supported.
//
// Parse the coordinator's HELION_ENV_DENYLIST_EXCEPTIONS via
// ParseEnvDenylistExceptions before calling; pass nil / empty to keep
// the denylist absolute (the safe default).
func (s *Server) SetEnvDenylistExceptions(exceptions []EnvDenylistException) {
	s.envDenylistExceptions = exceptions
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
	// `/lineage` is registered before `/{id}` so ServeMux's most-
	// specific-pattern-wins rule doesn't route `GET /workflows/123/lineage`
	// to the base detail handler. The lineage handler itself returns 404
	// when the model store is not wired (coordinator without registry).
	s.mux.HandleFunc("GET /workflows/{id}/lineage", s.authMiddleware(s.handleGetWorkflowLineage))
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
		tokenIssueLimiters:   make(map[string]*rate.Limiter),
		revealSecretLimiters: make(map[string]*rate.Limiter),
		issueOpCertLimiters:  make(map[string]*rate.Limiter),
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
	// Feature 19 follow-up: node revocation is an admin-only operation
	// (takes a node offline cluster-wide). Pre-existing wiring used
	// only authMiddleware, which let any authenticated caller — node
	// role, or the new feature-19 `job` role — revoke any node.
	// Now guarded by adminMiddleware consistent with the other
	// /admin/* surface.
	s.mux.HandleFunc("POST /admin/nodes/{id}/revoke", s.authMiddleware(s.adminMiddleware(s.handleRevokeNode)))
	s.mux.HandleFunc("POST /admin/tokens", s.authMiddleware(s.adminMiddleware(s.handleIssueToken)))
	s.mux.HandleFunc("DELETE /admin/tokens/{jti}", s.authMiddleware(s.adminMiddleware(s.handleRevokeToken)))
	// Feature 26 — operator-facing "show me the secret" action.
	// Admin-only; every call rate-limited + audited (success AND reject).
	s.mux.HandleFunc("POST /admin/jobs/{id}/reveal-secret", s.authMiddleware(s.adminMiddleware(s.handleRevealSecret)))

	// AUDIT 2026-04-12-01/H2 (fixed): WebSocket endpoints authenticate via
	// first-message pattern instead of URL query parameters. The connection is
	// upgraded without auth; the first frame must be {"type":"auth","token":"..."}.
	// This keeps JWTs out of server access logs and browser history.
	s.mux.HandleFunc("GET /ws/jobs/{id}/logs", s.handleJobLogStream)
	s.mux.HandleFunc("GET /ws/metrics", s.handleMetricsStream)
}

// Handler returns the underlying http.Handler for testing. Feature
// 27: when the client-cert tier is warn or on, the returned handler
// wraps the mux with clientCertMiddleware so tests see the same
// enforcement the real Serve/ServeTLS path applies.
func (s *Server) Handler() http.Handler {
	if s.clientCertTier != clientCertOff {
		return http.HandlerFunc(s.clientCertMiddleware(s.mux.ServeHTTP))
	}
	return s.mux
}

// Serve starts listening on addr in plain HTTP. Blocks until the server
// is closed. Returns http.ErrServerClosed on graceful shutdown — callers
// should treat that as a clean exit.
//
// Feature 23 shipped ServeTLS as the preferred entry point; Serve stays
// available for explicit opt-outs (dev overlays that set
// HELION_REST_TLS=off). Production coordinators should always use
// ServeTLS so the dashboard traffic rides a TLS 1.3 + hybrid-KEM
// handshake end to end.
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("api.Server listen %s: %w", addr, err)
	}
	hsrv := s.buildHTTPServer(nil)
	s.httpSrvMu.Lock()
	s.httpSrv = hsrv
	s.httpSrvMu.Unlock()
	return hsrv.Serve(lis)
}

// ServeTLS starts listening on addr with the supplied TLS config. Blocks
// until the server is closed.
//
// cfg MUST already carry Certificates + ClientAuth + the hybrid-KEM
// curve preferences — callers build it via `bundle.CA.EnhancedTLSConfig`
// (see cmd/helion-coordinator/main.go and internal/pqcrypto/hybrid.go).
// ServeTLS intentionally does NOT populate any of those fields itself;
// passing a half-configured cfg is a programming error and returns
// right away rather than silently serving plaintext.
//
// Covers both REST routes and WebSocket upgrades — /ws/* handlers live on
// the same s.mux and ride the same listener, so a single TLS upgrade
// secures every dashboard → coordinator byte.
func (s *Server) ServeTLS(addr string, cfg *tls.Config) error {
	if cfg == nil || len(cfg.Certificates) == 0 {
		return fmt.Errorf("api.Server ServeTLS: cfg must carry at least one certificate")
	}
	lis, err := tls.Listen("tcp", addr, cfg)
	if err != nil {
		return fmt.Errorf("api.Server tls listen %s: %w", addr, err)
	}
	hsrv := s.buildHTTPServer(cfg)
	s.httpSrvMu.Lock()
	s.httpSrv = hsrv
	s.httpSrvMu.Unlock()
	return hsrv.Serve(lis)
}

// buildHTTPServer centralises the http.Server construction for Serve and
// ServeTLS so both paths share the same timeouts + handler registration.
// Pass a non-nil cfg to attach it to TLSConfig (only meaningful for the
// TLS path; a tls.Listen already enforces the same cfg on the wire, but
// setting it here also lets http.Server auto-upgrade to HTTP/2 over TLS
// and exposes the TLSConfig via request introspection if a middleware
// needs it).
//
// AUDIT L6 (fixed): IdleTimeout prevents keep-alive connections from being held
// open indefinitely, limiting the resource impact of slow or idle clients.
// AUDIT 2026-04-12-01/L1 (fixed): ReadHeaderTimeout limits how long the
// server waits for request headers, countering Slowloris-style attacks
// that trickle headers one byte at a time to hold connection slots open.
func (s *Server) buildHTTPServer(cfg *tls.Config) *http.Server {
	// Feature 27 — Handler() applies clientCertMiddleware when the
	// operator-cert tier is enabled. Applied at the server level
	// rather than per-route because cert enforcement is a server-
	// level decision, not a per-endpoint opt-in. Health endpoints
	// are exempted inside the middleware so load balancers without
	// client certs can still probe readiness.
	return &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         cfg,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
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
