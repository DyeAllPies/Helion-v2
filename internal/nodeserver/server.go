// Package nodeserver implements the gRPC NodeService on the node agent side.
//
// The Server receives Dispatch RPCs from the coordinator, forwards them to
// the Runtime (Go subprocess or Rust via Unix socket), and returns a
// DispatchAck when the job finishes.  After the job exits it also:
//   - streams stdout+stderr back to the coordinator via StreamLogs
//   - reports the final status via ReportResult
package nodeserver

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.NodeServiceServer.
type Server struct {
	pb.UnimplementedNodeServiceServer

	rt          runtime.Runtime
	client      *grpcclient.Client // coordinator gRPC client for callbacks
	nodeID      string
	runningJobs atomic.Int32
	totalJobs   atomic.Int32
	log         *slog.Logger
}

// New returns a Server backed by rt.
// client and nodeID are used to call ReportResult and StreamLogs on the
// coordinator after each job completes.
func New(rt runtime.Runtime, client *grpcclient.Client, nodeID string, log *slog.Logger) *Server {
	return &Server{rt: rt, client: client, nodeID: nodeID, log: log}
}

// RunningJobs returns the count of currently-executing jobs.
// Used by the heartbeat loop to report load to the coordinator.
func (s *Server) RunningJobs() int32 { return s.runningJobs.Load() }

// Dispatch accepts a job, executes it via the Runtime, streams logs, reports
// the result to the coordinator, then returns a DispatchAck.
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

	startedAt := time.Now()
	result, err := s.rt.Run(ctx, runtime.RunRequest{
		JobID:          req.JobId,
		Command:        req.Command,
		Args:           req.Args,
		Env:            req.Env,
		TimeoutSeconds: req.TimeoutSeconds,
	})
	finishedAt := time.Now()

	if err != nil {
		s.log.Error("runtime error", slog.String("job_id", req.JobId), slog.Any("err", err))
		s.reportResult(ctx, req.JobId, false, -1, err.Error(), startedAt, finishedAt)
		return &pb.DispatchAck{JobId: req.JobId, Accepted: false, Error: err.Error()}, nil
	}

	s.log.Info("job finished",
		slog.String("job_id", req.JobId),
		slog.Int("exit_code", int(result.ExitCode)),
		slog.String("kill_reason", result.KillReason),
	)

	// Stream stdout + stderr back to the coordinator.
	if s.client != nil && (len(result.Stdout) > 0 || len(result.Stderr) > 0) {
		if lerr := s.client.StreamLogs(ctx, req.JobId, s.nodeID, result.Stdout, result.Stderr); lerr != nil {
			s.log.Warn("StreamLogs failed", slog.String("job_id", req.JobId), slog.Any("err", lerr))
		}
	}

	// Combine error message: prefer kill_reason so the coordinator can
	// detect security violations ("Seccomp") from the error field.
	errMsg := result.KillReason
	if errMsg == "" {
		errMsg = result.Error
	}

	success := result.ExitCode == 0 && result.KillReason == ""
	s.reportResult(ctx, req.JobId, success, result.ExitCode, errMsg, startedAt, finishedAt)

	return &pb.DispatchAck{
		JobId:    req.JobId,
		Accepted: true,
		Error:    errMsg,
	}, nil
}

// reportResult calls coordinator.ReportResult in a best-effort manner.
// Failures are logged but do not cause Dispatch to fail.
func (s *Server) reportResult(
	ctx context.Context,
	jobID string,
	success bool,
	exitCode int32,
	errMsg string,
	startedAt, finishedAt time.Time,
) {
	if s.client == nil {
		return
	}
	rr := &pb.JobResult{
		JobId:      jobID,
		NodeId:     s.nodeID,
		Success:    success,
		ExitCode:   exitCode,
		Error:      errMsg,
		StartedAt:  startedAt.UnixNano(),
		FinishedAt: finishedAt.UnixNano(),
	}
	if err := s.client.ReportResult(ctx, rr); err != nil {
		s.log.Warn("ReportResult failed", slog.String("job_id", jobID), slog.Any("err", err))
	}
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
