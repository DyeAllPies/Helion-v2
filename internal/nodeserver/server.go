// Package nodeserver implements the gRPC NodeService on the node agent side.
//
// The Server receives Dispatch RPCs from the coordinator, forwards them to
// the Runtime (Go subprocess or Rust via Unix socket), and returns a
// DispatchAck when the job finishes. Cancel and GetMetrics are also handled.
package nodeserver

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.NodeServiceServer.
type Server struct {
	pb.UnimplementedNodeServiceServer

	rt          runtime.Runtime
	runningJobs atomic.Int32
	totalJobs   atomic.Int32
	log         *slog.Logger
}

// New returns a Server backed by rt.
func New(rt runtime.Runtime, log *slog.Logger) *Server {
	return &Server{rt: rt, log: log}
}

// RunningJobs returns the count of currently-executing jobs.
// Used by the heartbeat loop to report load to the coordinator.
func (s *Server) RunningJobs() int32 { return s.runningJobs.Load() }

// Dispatch accepts a job, executes it via the Runtime, and blocks until
// the job completes. The DispatchAck is returned to the coordinator.
func (s *Server) Dispatch(ctx context.Context, req *pb.DispatchRequest) (*pb.DispatchAck, error) {
	if req.JobId == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id required")
	}

	s.runningJobs.Add(1)
	s.totalJobs.Add(1)
	defer s.runningJobs.Add(-1)

	s.log.Info("job dispatched",
		slog.String("job_id", req.JobId),
		slog.String("command", req.Command),
	)

	result, err := s.rt.Run(ctx, runtime.RunRequest{
		JobID:          req.JobId,
		Command:        req.Command,
		Args:           req.Args,
		Env:            req.Env,
		TimeoutSeconds: req.TimeoutSeconds,
	})
	if err != nil {
		s.log.Error("runtime error", slog.String("job_id", req.JobId), slog.Any("err", err))
		return &pb.DispatchAck{
			JobId:    req.JobId,
			Accepted: false,
			Error:    err.Error(),
		}, nil
	}

	s.log.Info("job finished",
		slog.String("job_id", req.JobId),
		slog.Int("exit_code", int(result.ExitCode)),
		slog.String("kill_reason", result.KillReason),
	)

	errField := result.Error
	if result.KillReason != "" && errField == "" {
		errField = result.KillReason
	}

	return &pb.DispatchAck{
		JobId:    req.JobId,
		Accepted: true,
		Error:    errField,
	}, nil
}

// Cancel terminates a running job.
func (s *Server) Cancel(_ context.Context, req *pb.CancelRequest) (*pb.Ack, error) {
	if req.JobId == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id required")
	}
	if err := s.rt.Cancel(req.JobId); err != nil {
		return nil, status.Errorf(codes.NotFound, "cancel %s: %v", req.JobId, err)
	}
	return &pb.Ack{Ok: true}, nil
}

// GetMetrics returns current node metrics.
func (s *Server) GetMetrics(_ context.Context, _ *pb.Empty) (*pb.NodeMetrics, error) {
	return &pb.NodeMetrics{
		RunningJobs: s.runningJobs.Load(),
		TotalJobs:   s.totalJobs.Load(),
	}, nil
}