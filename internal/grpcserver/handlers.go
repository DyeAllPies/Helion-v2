// internal/grpcserver/handlers.go
//
// RPC handler methods: Register, Heartbeat, ReportResult.

package grpcserver

import (
	"context"
	"io"
	"log/slog"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	opts.Runtime = result.Runtime

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

	// Notify workflow system of job completion so it can evaluate downstream
	// dependency eligibility and cascade failures.
	if s.onJobCompleted != nil {
		s.onJobCompleted(ctx, result.JobId, targetStatus)
	}

	return &pb.Ack{Ok: true}, nil
}
