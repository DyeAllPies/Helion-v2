// internal/grpcserver/server.go
//
// Server is the coordinator's gRPC server.
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
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── RegistryIface ─────────────────────────────────────────────────────────────

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
}

// ── Server ────────────────────────────────────────────────────────────────────

// Server is the coordinator's gRPC server.
type Server struct {
	pb.UnimplementedCoordinatorServiceServer
	grpc              *grpc.Server
	registry          RegistryIface    // nil if not injected
	jobs              JobStoreIface    // nil if not injected
	rateLimiter       RateLimiterIface
	audit             AuditLoggerIface
	revocationChecker RevocationChecker // nil means no revocation enforcement
	log               *slog.Logger

	// Active heartbeat streams: nodeID → done channel.
	// CancelStream closes the channel; the Heartbeat loop detects it and returns
	// codes.Unauthenticated so the node's stream is terminated server-side.
	streamsMu sync.Mutex
	streams   map[string]chan struct{}
}

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

// WithLogger injects a structured logger.
func WithLogger(log *slog.Logger) Option {
	return func(s *Server) { s.log = log }
}

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

// revocationInterceptor returns a gRPC UnaryServerInterceptor that rejects
// RPCs from nodes whose ID appears in the revocation set.
//
// Node ID extraction strategy: the RegisterRequest carries an explicit NodeId
// field.  For other RPCs (Heartbeat, ReportResult) the node ID is embedded in
// the request proto.  We extract it via a type-switch so no reflection is
// needed in the hot path.
func (s *Server) revocationInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		nodeID := extractNodeID(req)
		if nodeID != "" && s.revocationChecker.IsRevoked(nodeID) {
			return nil, status.Errorf(codes.Unauthenticated,
				"node %s has been revoked — re-register with a new certificate", nodeID)
		}
		return handler(ctx, req)
	}
}

// rateLimitInterceptor returns a gRPC UnaryServerInterceptor that enforces
// per-node rate limits. Requests whose node ID exceeds the limit are rejected
// with ResourceExhausted and a rate_limit_hit audit event is written.
// Requests with no extractable node ID are passed through unchanged.
func (s *Server) rateLimitInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		nodeID := extractNodeID(req)
		if nodeID == "" {
			return handler(ctx, req)
		}
		if err := s.rateLimiter.Allow(ctx, nodeID); err != nil {
			if s.audit != nil {
				if aerr := s.audit.LogRateLimitHit(ctx, nodeID, s.rateLimiter.GetRate()); aerr != nil {
					slog.Warn("audit log failed", slog.String("node_id", nodeID), slog.Any("err", aerr))
				}
			}
			return nil, err // already a gRPC ResourceExhausted status
		}
		return handler(ctx, req)
	}
}

// extractNodeID pulls the node ID out of known request types.
// Returns "" for unknown types so the interceptor passes them through.
func extractNodeID(req interface{}) string {
	switch r := req.(type) {
	case *pb.RegisterRequest:
		return r.NodeId
	case *pb.JobResult:
		return r.NodeId
	default:
		return ""
	}
}

// CancelStream forcibly closes the active heartbeat stream for nodeID by
// closing its done channel.  The Heartbeat loop checks the channel on each
// iteration and returns codes.Unauthenticated when it is closed.
// Implements cluster.StreamRevoker so the Registry can wire it in at startup.
func (s *Server) CancelStream(nodeID string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if ch, ok := s.streams[nodeID]; ok {
		close(ch)
		delete(s.streams, nodeID)
	}
}

// registerStream stores a done channel for nodeID's heartbeat stream.
// If a prior channel exists (e.g. reconnected node), it is closed first.
func (s *Server) registerStream(nodeID string, ch chan struct{}) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if old, ok := s.streams[nodeID]; ok {
		close(old)
	}
	s.streams[nodeID] = ch
}

// unregisterStream removes nodeID's channel if it still matches ch.
// Guards against a race where CancelStream already deleted and a new stream
// re-registered under the same nodeID before this deferred cleanup runs.
func (s *Server) unregisterStream(nodeID string, ch chan struct{}) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if s.streams[nodeID] == ch {
		delete(s.streams, nodeID)
	}
}

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

// ── RPC handlers ──────────────────────────────────────────────────────────────

// Register handles node self-registration.
// Delegates to the Registry if one was injected; otherwise echoes NodeId.
func (s *Server) Register(
	ctx context.Context,
	req *pb.RegisterRequest,
) (*pb.RegisterResponse, error) {
	if s.registry != nil {
		return s.registry.Register(ctx, req)
	}
	// Fallback: no registry injected (mTLS handshake test path).
	return &pb.RegisterResponse{NodeId: req.NodeId}, nil
}

// Heartbeat handles the bidi-streaming heartbeat RPC.
//
// The stream contract:
//   - Client sends HeartbeatMessage frames at its configured interval.
//   - Server sends back a NodeCommand after each message.
//   - Stream ends when the client closes it (io.EOF) or context is cancelled.
//
// If no registry is injected, the server still accepts the stream and sends
// NOOP commands — valid gRPC behaviour, useful for the mTLS test.
func (s *Server) Heartbeat(
	stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.NodeCommand],
) error {
	ctx := stream.Context()

	// doneCh is registered on the first message that carries a NodeId.
	// CancelStream closes it; the loop below checks it each iteration.
	var doneCh chan struct{}
	var streamNodeID string

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			// Client closed the send side — normal shutdown.
			return nil
		}
		if err != nil {
			// Context cancelled or network error.
			if ctx.Err() != nil {
				return nil
			}
			return status.Errorf(codes.Internal, "heartbeat recv: %v", err)
		}

		// Register the stream's done channel on the first message with a NodeId.
		if doneCh == nil && msg.NodeId != "" {
			streamNodeID = msg.NodeId
			doneCh = make(chan struct{})
			s.registerStream(streamNodeID, doneCh)
			defer s.unregisterStream(streamNodeID, doneCh)
		}

		// Check whether this stream has been revoked via CancelStream.
		if doneCh != nil {
			select {
			case <-doneCh:
				return status.Errorf(codes.Unauthenticated,
					"node %s has been revoked — stream terminated", streamNodeID)
			default:
			}
		}

		// Rate-limit per heartbeat message; streaming RPCs bypass unary interceptors.
		if s.rateLimiter != nil && msg.NodeId != "" {
			if err := s.rateLimiter.Allow(ctx, msg.NodeId); err != nil {
				if s.audit != nil {
					if aerr := s.audit.LogRateLimitHit(ctx, msg.NodeId, s.rateLimiter.GetRate()); aerr != nil {
						slog.Warn("audit log failed", slog.String("node_id", msg.NodeId), slog.Any("err", aerr))
					}
				}
				return err // ResourceExhausted — terminates the heartbeat stream
			}
		}

		// Delegate to registry if available.
		if s.registry != nil {
			if err := s.registry.HandleHeartbeat(ctx, msg); err != nil {
				if err == cluster.ErrNodeNotRegistered {
					// Terminate the stream: node must call Register first.
					return status.Errorf(codes.NotFound, "node not registered: call Register before sending heartbeats")
				}
				s.log.Error("heartbeat handler error",
					slog.String("node_id", msg.NodeId),
					slog.Any("err", err),
				)
				// For other errors, log and continue — don't kill the stream.
			}
		}

		// Send NOOP command back to acknowledge the heartbeat.
		ack := &pb.NodeCommand{Type: pb.NodeCommand_NOOP}
		if err := stream.Send(ack); err != nil {
			return status.Errorf(codes.Internal, "heartbeat send: %v", err)
		}
	}
}

// ReportResult handles job completion reports from nodes.
//
// When a node finishes executing a job (success or failure), it calls this
// RPC to notify the coordinator. The coordinator updates the job status in
// the JobStore and triggers any necessary state transitions.
func (s *Server) ReportResult(
	ctx context.Context,
	result *pb.JobResult,
) (*pb.Ack, error) {
	if s.jobs == nil {
		// No JobStore injected — return success but don't process.
		// This allows tests without full coordinator setup.
		return &pb.Ack{Ok: true}, nil
	}

	s.log.Info("job result reported",
		slog.String("job_id", result.JobId),
		slog.String("node_id", result.NodeId),
		slog.Bool("success", result.Success),
	)

	// Get current job state to determine transition path
	job, err := s.jobs.Get(result.JobId)
	if err != nil {
		s.log.Error("failed to get job",
			slog.String("job_id", result.JobId),
			slog.Any("err", err),
		)
		return nil, status.Errorf(codes.NotFound, "job not found: %v", err)
	}

	// Prepare transition options
	var opts cluster.TransitionOptions
	opts.NodeID = result.NodeId
	opts.ExitCode = result.ExitCode

	// If job is in dispatching state, transition to running first
	// (required by the state machine: dispatching → running → completed)
	if job.Status == cpb.JobStatusDispatching {
		if err := s.jobs.Transition(ctx, result.JobId, cpb.JobStatusRunning, opts); err != nil {
			s.log.Error("failed to transition job to running",
				slog.String("job_id", result.JobId),
				slog.Any("err", err),
			)
			return nil, status.Errorf(codes.Internal, "transition to running failed: %v", err)
		}
	}

	// Now transition to final state
	var targetStatus cpb.JobStatus
	if result.Success {
		targetStatus = cpb.JobStatusCompleted
	} else {
		targetStatus = cpb.JobStatusFailed
		if result.Error != "" {
			opts.ErrMsg = result.Error
		}
	}

	if err := s.jobs.Transition(ctx, result.JobId, targetStatus, opts); err != nil {
		s.log.Error("failed to transition job to final state",
			slog.String("job_id", result.JobId),
			slog.String("target_status", targetStatus.String()),
			slog.Any("err", err),
		)
		return nil, status.Errorf(codes.Internal, "transition failed: %v", err)
	}

	// Audit security violations (Seccomp, OOMKilled) reported by the node.
	if s.audit != nil && result.Error != "" {
		switch result.Error {
		case "Seccomp", "OOMKilled":
			if aerr := s.audit.LogSecurityViolation(ctx, result.NodeId, result.JobId, result.Error); aerr != nil {
				s.log.Warn("audit security violation failed", slog.Any("err", aerr))
			}
		}
	}

	return &pb.Ack{Ok: true}, nil
}
