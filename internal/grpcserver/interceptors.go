// internal/grpcserver/interceptors.go
//
// gRPC unary interceptors: revocation check and per-node rate limiting.

package grpcserver

import (
	"context"
	"log/slog"

	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
