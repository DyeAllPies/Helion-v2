// internal/grpcserver/server.go
package grpcserver

import (
	"context"
	"fmt"
	"net"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
)

// Server is the coordinator's gRPC server.
type Server struct {
	pb.UnimplementedCoordinatorServiceServer
	grpc *grpc.Server
}

// New creates a gRPC server wired with mTLS from the provided auth bundle.
func New(bundle *auth.Bundle) (*Server, error) {
	creds, err := bundle.ServerCredentials()
	if err != nil {
		return nil, fmt.Errorf("server credentials: %w", err)
	}

	g := grpc.NewServer(grpc.Creds(creds))
	s := &Server{grpc: g}
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

// Register handles node self-registration — stub for now.
func (s *Server) Register(
	_ context.Context,
	req *pb.RegisterRequest,
) (*pb.RegisterResponse, error) {
	return &pb.RegisterResponse{
		NodeId: req.NodeId,
	}, nil
}
