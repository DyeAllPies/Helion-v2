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

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
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

// ── Server ────────────────────────────────────────────────────────────────────

// Server is the coordinator's gRPC server.
type Server struct {
	pb.UnimplementedCoordinatorServiceServer
	grpc     *grpc.Server
	registry RegistryIface // nil if not injected
	log      *slog.Logger
}

// Option is a functional option for New().
type Option func(*Server)

// WithRegistry injects a Registry into the server so that Register and
// Heartbeat RPCs are handled by real business logic.
func WithRegistry(r *cluster.Registry) Option {
	return func(s *Server) { s.registry = r }
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
