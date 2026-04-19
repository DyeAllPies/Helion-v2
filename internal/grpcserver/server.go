// internal/grpcserver/server.go
//
// Server is the coordinator's gRPC server.
//
// AUDIT 2026-04-12-01/L3 (fixed): split from 469-line monolith into:
//   server.go        — struct, interfaces, New(), options, Serve(), Stop()
//   interceptors.go  — revocation and rate-limit unary interceptors
//   handlers.go      — Register, Heartbeat, ReportResult RPC handlers
//   streams.go       — active heartbeat stream bookkeeping
//
// Registry injection
// ──────────────────
// The server accepts an optional *cluster.Registry via WithRegistry().
// When no registry is injected (e.g. the existing mTLS handshake test),
// Register returns a minimal response and Heartbeat is a no-op — both are
// still valid gRPC responses, so the mTLS test continues to pass unchanged.
//
// When a registry is injected, Register and Heartbeat delegate fully to it.
//
// Heartbeat stream
// ────────────────
// The proto defines Heartbeat as a bidi-streaming RPC:
//   rpc Heartbeat(stream HeartbeatMessage) returns (stream NodeCommand)
//
// The server reads HeartbeatMessage frames from the client stream and calls
// registry.HandleHeartbeat() for each one.  It sends back a NodeCommand
// (NOOP by default) after each message.  The stream stays open until the
// node closes it or the context is cancelled.

package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/logstore"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
)

// ── Interfaces ───────────────────────────────────────────────────────────────

// RegistryIface is the narrow interface the server needs from the Registry.
// Using an interface keeps grpcserver decoupled from internal/cluster and
// makes the server testable with a simple stub.
type RegistryIface interface {
	Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error)
	HandleHeartbeat(ctx context.Context, msg *pb.HeartbeatMessage) error
}

// RevocationChecker is satisfied by *cluster.Registry.
// Injecting via interface keeps grpcserver free of a cluster import cycle.
type RevocationChecker interface {
	IsRevoked(nodeID string) bool
}

// JobStoreIface is the interface for job operations.
type JobStoreIface interface {
	Submit(ctx context.Context, j *cpb.Job) error
	Get(jobID string) (*cpb.Job, error)
	Transition(ctx context.Context, jobID string, to cpb.JobStatus, opts cluster.TransitionOptions) error
}

// RateLimiterIface is the interface for rate limiting.
type RateLimiterIface interface {
	Allow(ctx context.Context, nodeID string) error
	GetRate() float64
}

// AuditLoggerIface is the interface for audit logging.
type AuditLoggerIface interface {
	LogJobSubmit(ctx context.Context, actor, jobID, command string) error
	LogRateLimitHit(ctx context.Context, nodeID string, limit float64) error
	LogSecurityViolation(ctx context.Context, nodeID, jobID, violation string) error
	// LogServiceEvent records a feature-17 readiness transition
	// (service.ready / service.unhealthy). Emitted from
	// ReportServiceEvent on edge triggers only, so one event per
	// state flip per job.
	LogServiceEvent(ctx context.Context, nodeID, jobID string, ready bool, port uint32, healthPath string, consecutiveFailures uint32) error
}

// JobCompletionCallback is called after a job reaches a terminal state.
// Used by the workflow system to check dependency eligibility and cascade
// failures. The callback receives the job ID and its final status.
type JobCompletionCallback func(ctx context.Context, jobID string, status cpb.JobStatus)

// RetryChecker decides whether a failed/timed-out job should be retried.
// Returns true if the job was retried (transitioned back to pending).
type RetryChecker interface {
	RetryIfEligible(ctx context.Context, jobID string) bool
}

// ── Server ───────────────────────────────────────────────────────────────────

// Server is the coordinator's gRPC server.
type Server struct {
	pb.UnimplementedCoordinatorServiceServer
	grpc              *grpc.Server
	registry          RegistryIface    // nil if not injected
	jobs              JobStoreIface    // nil if not injected
	rateLimiter       RateLimiterIface
	audit             AuditLoggerIface
	revocationChecker RevocationChecker     // nil means no revocation enforcement
	onJobCompleted    JobCompletionCallback // nil means no workflow integration
	retryChecker      RetryChecker          // nil means no retry support
	logStore          logstore.Store        // nil means logs are discarded
	services          *cluster.ServiceRegistry // nil means feature-17 service lookup is not wired

	// Feature 28 — analytics event bus. Optional; nil means no
	// analytics mirroring of service-probe transitions or log
	// chunks. Set via WithEventBus.
	eventBus *events.Bus

	log               *slog.Logger

	// Active heartbeat streams: nodeID → done channel.
	// CancelStream closes the channel; the Heartbeat loop detects it and returns
	// codes.Unauthenticated so the node's stream is terminated server-side.
	streamsMu sync.Mutex
	streams   map[string]chan struct{}
}

// ── Options ──────────────────────────────────────────────────────────────────

// Option is a functional option for New().
type Option func(*Server)

// WithRegistry injects a Registry into the server so that Register and
// Heartbeat RPCs are handled by real business logic.
func WithRegistry(r *cluster.Registry) Option {
	return func(s *Server) { s.registry = r }
}

// WithJobStore injects a JobStore for job dispatch.
func WithJobStore(jobs JobStoreIface) Option {
	return func(s *Server) { s.jobs = jobs }
}

// WithRateLimiter injects a rate limiter for Phase 4 security.
func WithRateLimiter(limiter RateLimiterIface) Option {
	return func(s *Server) { s.rateLimiter = limiter }
}

// WithAuditLogger injects an audit logger for Phase 4 security.
func WithAuditLogger(audit AuditLoggerIface) Option {
	return func(s *Server) { s.audit = audit }
}

// WithRevocationChecker injects a revocation checker for Phase 4 security.
// On every incoming unary RPC the interceptor calls IsRevoked(nodeID); if
// the node has been revoked it returns codes.Unauthenticated immediately.
func WithRevocationChecker(rc RevocationChecker) Option {
	return func(s *Server) { s.revocationChecker = rc }
}

// WithJobCompletionCallback injects a callback invoked after any job reaches a
// terminal state. Used to trigger workflow dependency evaluation.
func WithJobCompletionCallback(cb JobCompletionCallback) Option {
	return func(s *Server) { s.onJobCompleted = cb }
}

// WithRetryChecker injects a retry checker that evaluates whether a
// failed/timed-out job should be retried based on its retry policy.
func WithRetryChecker(rc RetryChecker) Option {
	return func(s *Server) { s.retryChecker = rc }
}

// WithLogStore injects a log store for persisting job stdout/stderr.
func WithLogStore(ls logstore.Store) Option {
	return func(s *Server) { s.logStore = ls }
}

// WithServiceRegistry injects a feature-17 service-endpoint registry.
// ReportServiceEvent RPCs from nodes populate entries here; the HTTP
// handler at GET /api/services/{id} reads them. Absent on deployments
// that haven't opted into inference services — the RPC handler then
// accepts events but does nothing, so nodes that erroneously emit
// them don't fail.
func WithServiceRegistry(sr *cluster.ServiceRegistry) Option {
	return func(s *Server) { s.services = sr }
}

// WithEventBus injects the analytics event bus so feature-28
// publishers in ReportServiceEvent + StreamLogs can mirror
// gRPC-arriving data into the analytics pipeline.
func WithEventBus(bus *events.Bus) Option {
	return func(s *Server) { s.eventBus = bus }
}

// WithLogger injects a structured logger.
func WithLogger(log *slog.Logger) Option {
	return func(s *Server) { s.log = log }
}

// ── Constructor ──────────────────────────────────────────────────────────────

// New creates a gRPC server wired with mTLS from the provided auth bundle.
// Existing callers (e.g. TestMTLSHandshake) pass no options and continue to
// work — Register returns a minimal echo response, Heartbeat is a no-op.
func New(bundle *auth.Bundle, opts ...Option) (*Server, error) {
	creds, err := bundle.ServerCredentials()
	if err != nil {
		return nil, fmt.Errorf("server credentials: %w", err)
	}

	s := &Server{
		log:     slog.Default(),
		streams: make(map[string]chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}

	// Build gRPC server options; always include transport credentials.
	grpcOpts := []grpc.ServerOption{grpc.Creds(creds)}

	// Phase 4: chain revocation and rate-limit interceptors for all unary RPCs.
	// Order matters: revocation runs first so a revoked node is rejected before
	// its request consumes a rate-limit token.
	var interceptors []grpc.UnaryServerInterceptor
	if s.revocationChecker != nil {
		interceptors = append(interceptors, s.revocationInterceptor())
	}
	if s.rateLimiter != nil {
		interceptors = append(interceptors, s.rateLimitInterceptor())
	}
	if len(interceptors) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(interceptors...))
	}

	g := grpc.NewServer(grpcOpts...)
	s.grpc = g

	pb.RegisterCoordinatorServiceServer(g, s)
	return s, nil
}

// ── Lifecycle ────────────────────────────────────────────────────────────────

// Serve starts listening on the given address. Blocks until stopped.
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	return s.grpc.Serve(lis)
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	s.grpc.GracefulStop()
}
