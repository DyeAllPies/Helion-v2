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
// JobStore injection
// ──────────────────
// The server accepts an optional cluster.JobStoreIface via WithJobStore().
// When injected, ReportResult translates the incoming *pb.JobResult into the
// correct internal state transition (completed, failed, or timeout) and
// delegates to the JobStore.
//
// When no JobStore is injected (mTLS handshake test, any test that doesn't
// care about job reporting), ReportResult returns a successful Ack — the RPC
// is acknowledged without side effects, which is valid gRPC behaviour.
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
//
// ReportResult mapping
// ────────────────────
// The proto JobResult uses Success bool + ExitCode int32 for outcomes.
// The internal state machine uses JobStatus (completed / failed / timeout).
// Mapping:
//   Success == true                   → JobStatusCompleted
//   Success == false && Error == "timeout" → JobStatusTimeout
//   Success == false (otherwise)      → JobStatusFailed

package grpcserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"

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

// ── JobStoreIface ─────────────────────────────────────────────────────────────

// JobStoreIface is the narrow interface the server needs from the JobStore.
// Only the transition method is needed here — the full JobStore API is not
// exposed to the gRPC layer.
type JobStoreIface interface {
	Transition(ctx context.Context, jobID string, to cpb.JobStatus, opts cluster.TransitionOptions) error
}

// ── Server ────────────────────────────────────────────────────────────────────

// Server is the coordinator's gRPC server.
type Server struct {
	pb.UnimplementedCoordinatorServiceServer
	grpc     *grpc.Server
	registry RegistryIface  // nil if not injected
	jobStore JobStoreIface  // nil if not injected
	log      *slog.Logger
}

// Option is a functional option for New().
type Option func(*Server)

// WithRegistry injects a Registry into the server so that Register and
// Heartbeat RPCs are handled by real business logic.
func WithRegistry(r *cluster.Registry) Option {
	return func(s *Server) { s.registry = r }
}

// WithJobStore injects a JobStore into the server so that ReportResult RPCs
// are translated into state transitions and persisted.
func WithJobStore(js *cluster.JobStore) Option {
	return func(s *Server) { s.jobStore = js }
}

// WithLogger injects a structured logger.
func WithLogger(log *slog.Logger) Option {
	return func(s *Server) { s.log = log }
}

// New creates a gRPC server wired with mTLS from the provided auth bundle.
// Existing callers (e.g. TestMTLSHandshake) pass no options and continue to
// work — Register returns a minimal echo response, Heartbeat and ReportResult
// are no-ops that return valid gRPC responses.
func New(bundle *auth.Bundle, opts ...Option) (*Server, error) {
	creds, err := bundle.ServerCredentials()
	if err != nil {
		return nil, fmt.Errorf("server credentials: %w", err)
	}

	g := grpc.NewServer(grpc.Creds(creds))
	s := &Server{
		grpc: g,
		log:  slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}

	pb.RegisterCoordinatorServiceServer(g, s)
	return s, nil
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

		// Delegate to registry if available.
		if s.registry != nil {
			if err := s.registry.HandleHeartbeat(ctx, msg); err != nil {
				s.log.Error("heartbeat handler error",
					slog.String("node_id", msg.NodeId),
					slog.Any("err", err),
				)
				// Log and continue — don't kill the stream for a handler error.
			}
		}

		// Send NOOP command back to acknowledge the heartbeat.
		ack := &pb.NodeCommand{Type: pb.NodeCommand_NOOP}
		if err := stream.Send(ack); err != nil {
			return status.Errorf(codes.Internal, "heartbeat send: %v", err)
		}
	}
}

// ReportResult handles job completion reports from node agents.
//
// The proto JobResult encodes outcomes as Success bool + ExitCode + Error.
// This method translates that into the internal state machine:
//
//	Success == true                          → JobStatusCompleted
//	Success == false && error contains "timeout" → JobStatusTimeout
//	Success == false (all other errors)      → JobStatusFailed
//
// If no JobStore is injected, the RPC is acknowledged without side effects —
// this preserves backward compatibility with callers that don't inject a store.
func (s *Server) ReportResult(
	ctx context.Context,
	result *pb.JobResult,
) (*pb.Ack, error) {
	if s.jobStore == nil {
		// No store injected — acknowledge without state change.
		return &pb.Ack{}, nil
	}

	var toStatus cpb.JobStatus
	switch {
	case result.Success:
		toStatus = cpb.JobStatusCompleted
	case strings.Contains(strings.ToLower(result.Error), "timeout"):
		toStatus = cpb.JobStatusTimeout
	default:
		toStatus = cpb.JobStatusFailed
	}

	opts := cluster.TransitionOptions{
		NodeID:   result.NodeId,
		ExitCode: result.ExitCode,
		ErrMsg:   result.Error,
	}

	if err := s.jobStore.Transition(ctx, result.JobId, toStatus, opts); err != nil {
		s.log.Error("ReportResult: transition failed",
			slog.String("job_id", result.JobId),
			slog.String("node_id", result.NodeId),
			slog.String("to", toStatus.String()),
			slog.Any("err", err),
		)
		// Return Internal so the node agent knows the coordinator did not
		// record the result and can retry.
		return nil, status.Errorf(codes.Internal, "record job result: %v", err)
	}

	s.log.Info("job result recorded",
		slog.String("job_id", result.JobId),
		slog.String("node_id", result.NodeId),
		slog.String("status", toStatus.String()),
		slog.Int("exit_code", int(result.ExitCode)),
	)

	return &pb.Ack{}, nil
}
