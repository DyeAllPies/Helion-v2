// internal/grpcserver/handlers.go
//
// RPC handler methods: Register, Heartbeat, ReportResult.

package grpcserver

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/logstore"

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
	stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.HeartbeatAck],
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
		ack := &pb.HeartbeatAck{Command: pb.NodeCommand_NODE_COMMAND_NONE}
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

	// A job's NodeID is pinned when the coordinator dispatches it. A
	// node reporting a result for a job that was dispatched to a
	// *different* node is either racing (very rare — dispatch loop is
	// single-threaded per job) or malicious (compromised node
	// attempting to poison another node's job with forged outputs).
	// Reject either way; the legitimate node will retry on its next
	// heartbeat if the job really was reassigned.
	if job.NodeID != "" && result.NodeId != "" && job.NodeID != result.NodeId {
		s.log.Warn("ReportResult node_id mismatch — rejecting",
			slog.String("job_id", result.JobId),
			slog.String("dispatched_to", job.NodeID),
			slog.String("reported_by", result.NodeId),
		)
		if s.audit != nil {
			// Security-violation channel so this surfaces on the
			// same SIEM/alert path as Seccomp / OOMKilled.
			if aerr := s.audit.LogSecurityViolation(ctx, result.NodeId, result.JobId, "node_id_mismatch"); aerr != nil {
				s.log.Warn("audit node_id mismatch failed", slog.Any("err", aerr))
			}
		}
		return nil, status.Errorf(codes.PermissionDenied,
			"job %s was dispatched to a different node", result.JobId)
	}

	// Prepare transition options
	var opts cluster.TransitionOptions
	opts.NodeID = result.NodeId
	opts.ExitCode = result.ExitCode
	opts.Runtime = result.Runtime
	opts.ResolvedOutputs = s.attestOutputs(result.JobId, result.NodeId, result.Outputs)

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

	// Check if the job should be retried before notifying workflows.
	// If retried, the job goes back to pending — skip workflow notification.
	if s.retryChecker != nil && s.retryChecker.RetryIfEligible(ctx, result.JobId) {
		s.log.Info("job retried",
			slog.String("job_id", result.JobId),
		)
		return &pb.Ack{Ok: true}, nil
	}

	// Notify workflow system of job completion so it can evaluate downstream
	// dependency eligibility and cascade failures.
	if s.onJobCompleted != nil {
		s.onJobCompleted(ctx, result.JobId, targetStatus)
	}

	return &pb.Ack{Ok: true}, nil
}

// Caps applied to ReportResult-supplied outputs. These mirror the
// submit-time caps in internal/api so a compromised node cannot
// poison the coordinator's persistence layer with oversized or
// malformed output records that a downstream workflow resolver
// (step 3) would later dereference as if trusted.
const (
	maxReportedOutputs   = 64
	maxReportedURILen    = 2048
	maxReportedNameLen   = 64
	maxReportedLocalPath = 512
)

// allowedOutputSchemes pins the URI schemes a node is permitted to
// report. Anything else (http, https, gs, ssh, ...) is dropped: the
// artifact store speaks only file:// and s3://, so a scheme outside
// this set would route a future consumer (step 3 workflow passing,
// step 6 registry) at a URL the coordinator never issued.
var allowedOutputSchemes = map[string]struct{}{
	"file": {},
	"s3":   {},
}

// attestOutputs is the coordinator's trust boundary for node-attested
// artifact metadata. Reported entries are validated — shape, scheme,
// and per-job key-prefix — and silently dropped if they violate any
// rule. The coordinator logs each drop so operators can notice a
// misbehaving (or compromised) node without failing the job's
// terminal transition — the process still ran, only its declared
// outputs are untrustworthy.
//
// Prefix attestation is the key rule for the ML threat model: the
// stager always writes outputs under `jobs/<job_id>/<local_path>`, so
// any reported URI whose path lacks a `/jobs/<job_id>/` segment must
// have been fabricated by the node. Without this check a compromised
// node could report `s3://bucket/jobs/<other-job-id>/<stolen>` and
// step-3 workflow resolution would happily pipe another job's output
// (or a path entirely outside the jobs/ tree) into a downstream job.
func (s *Server) attestOutputs(jobID, nodeID string, src []*pb.ArtifactOutput) []cpb.ArtifactOutput {
	if len(src) == 0 {
		return nil
	}
	// Bound the slice before iterating so a node cannot force the
	// coordinator to iterate an unbounded list.
	if len(src) > maxReportedOutputs {
		s.log.Warn("ReportResult outputs exceed cap; truncating",
			slog.String("job_id", jobID),
			slog.String("node_id", nodeID),
			slog.Int("count", len(src)), slog.Int("cap", maxReportedOutputs))
		src = src[:maxReportedOutputs]
	}
	out := make([]cpb.ArtifactOutput, 0, len(src))
	for _, o := range src {
		if o == nil {
			continue
		}
		if reason := validateReportedOutput(o); reason != "" {
			s.log.Warn("dropping invalid reported output",
				slog.String("job_id", jobID),
				slog.String("node_id", nodeID),
				slog.String("name", o.Name),
				slog.String("reason", reason))
			continue
		}
		if !uriBelongsToJob(o.Uri, jobID, o.LocalPath) {
			s.log.Warn("dropping reported output with mismatched job prefix",
				slog.String("job_id", jobID),
				slog.String("node_id", nodeID),
				slog.String("name", o.Name),
				slog.String("uri", o.Uri))
			if s.audit != nil {
				// Prefix mismatch is either a bug or a node trying to
				// register someone else's artifact as its own output.
				// Either way it's security-violation-worthy.
				if aerr := s.audit.LogSecurityViolation(context.Background(), nodeID, jobID, "output_prefix_mismatch"); aerr != nil {
					s.log.Warn("audit prefix mismatch failed", slog.Any("err", aerr))
				}
			}
			continue
		}
		out = append(out, cpb.ArtifactOutput{
			Name:      o.Name,
			URI:       o.Uri,
			Size:      o.Size,
			SHA256:    o.Sha256,
			LocalPath: o.LocalPath,
		})
	}
	return out
}

// uriBelongsToJob returns true iff the reported URI ends with the
// exact key the stager would have minted for (jobID, localPath) — the
// invariant `jobs/<job_id>/<local_path>` enforced in
// internal/staging.Stager.upload. localPath has already passed the
// submit-time validator (no "..", no absolute path, no NUL), so a
// suffix match is a rigorous attestation: any URI pointing somewhere
// else — another job's key space, a path outside the store, a host
// scheme the coordinator never issued — fails here.
func uriBelongsToJob(uri, jobID, localPath string) bool {
	if jobID == "" || localPath == "" {
		return false
	}
	suffix := "/jobs/" + jobID + "/" + localPath
	return strings.HasSuffix(uri, suffix)
}

// outputsFromProto is the package-level convenience used by tests that
// don't need the logging path. Production code goes through the
// method form on *Server.
func outputsFromProto(src []*pb.ArtifactOutput) []cpb.ArtifactOutput {
	if len(src) == 0 {
		return nil
	}
	out := make([]cpb.ArtifactOutput, 0, len(src))
	for _, o := range src {
		if o == nil {
			continue
		}
		if validateReportedOutput(o) != "" {
			continue
		}
		out = append(out, cpb.ArtifactOutput{
			Name:      o.Name,
			URI:       o.Uri,
			Size:      o.Size,
			SHA256:    o.Sha256,
			LocalPath: o.LocalPath,
		})
	}
	return out
}

// validateReportedOutput enforces the coordinator-side trust boundary
// on node-attested artifact metadata. Returns the reason string when
// the entry should be dropped; empty string when it's accepted.
func validateReportedOutput(o *pb.ArtifactOutput) string {
	if o.Name == "" || len(o.Name) > maxReportedNameLen {
		return "name length"
	}
	for i := 0; i < len(o.Name); i++ {
		c := o.Name[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return "name charset"
		}
	}
	if o.Uri == "" || len(o.Uri) > maxReportedURILen {
		return "uri length"
	}
	// Scheme check: cheap, no url.Parse allocation for simple prefix
	// matching on the two allowed schemes.
	if !(hasPrefix(o.Uri, "file://") || hasPrefix(o.Uri, "s3://")) {
		return "uri scheme"
	}
	scheme := "file"
	if hasPrefix(o.Uri, "s3://") {
		scheme = "s3"
	}
	if _, ok := allowedOutputSchemes[scheme]; !ok {
		return "uri scheme"
	}
	// NUL and control bytes must not slip through into persistence.
	for i := 0; i < len(o.Uri); i++ {
		if b := o.Uri[i]; b == 0 || b < 0x20 || b == 0x7f {
			return "uri control bytes"
		}
	}
	if len(o.LocalPath) > maxReportedLocalPath {
		return "local_path length"
	}
	if o.Size < 0 {
		return "negative size"
	}
	return ""
}

// hasPrefix mirrors strings.HasPrefix without the allocation-free
// concern (strings.HasPrefix is already alloc-free). Inlined for
// readability at the call site; if this function list grows, replace
// with strings.HasPrefix.
func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// ReportServiceEvent accepts feature-17 readiness transitions from a
// node's service prober. Updates the in-memory ServiceRegistry (which
// backs GET /api/services/{id}) and writes an audit record.
//
// The node_id on the event is validated against the dispatched job —
// a node reporting a service event for a job pinned to a different
// node is rejected for the same reason ReportResult is: in legitimate
// traffic this never happens, and permitting it would let a
// compromised node override the upstream mapping for jobs it does
// not own.
func (s *Server) ReportServiceEvent(
	ctx context.Context,
	evt *pb.ServiceEvent,
) (*pb.Ack, error) {
	if evt.JobId == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id required")
	}
	if s.services == nil {
		// Feature 17 not wired on this coordinator — accept the RPC
		// so the node's prober isn't stuck in a retry loop, but drop
		// the payload. Audit the receipt so operators can see that
		// an unexpected service event arrived.
		s.log.Warn("ReportServiceEvent received but registry is not wired — dropping",
			slog.String("job_id", evt.JobId),
			slog.String("node_id", evt.NodeId))
		return &pb.Ack{Ok: true}, nil
	}

	// Reject cross-node poisoning attempts.
	if s.jobs != nil {
		job, err := s.jobs.Get(evt.JobId)
		if err == nil && job.NodeID != "" && evt.NodeId != "" && job.NodeID != evt.NodeId {
			s.log.Warn("ReportServiceEvent node_id mismatch — rejecting",
				slog.String("job_id", evt.JobId),
				slog.String("dispatched_to", job.NodeID),
				slog.String("reported_by", evt.NodeId))
			if s.audit != nil {
				if aerr := s.audit.LogSecurityViolation(ctx, evt.NodeId, evt.JobId, "service_event_node_id_mismatch"); aerr != nil {
					s.log.Warn("audit node mismatch failed", slog.Any("err", aerr))
				}
			}
			return nil, status.Errorf(codes.PermissionDenied,
				"service event for job %s was reported by the wrong node", evt.JobId)
		}
	}

	occurred := time.Now()
	if evt.OccurredAt != nil {
		occurred = evt.OccurredAt.AsTime()
	}
	s.services.Upsert(cpb.ServiceEndpoint{
		JobID:       evt.JobId,
		NodeID:      evt.NodeId,
		NodeAddress: evt.NodeAddress,
		Port:        evt.Port,
		HealthPath:  evt.HealthPath,
		Ready:       evt.Ready,
		UpdatedAt:   occurred,
	})

	eventName := "service.ready"
	if !evt.Ready {
		eventName = "service.unhealthy"
	}
	s.log.Info(eventName,
		slog.String("job_id", evt.JobId),
		slog.String("node_id", evt.NodeId),
		slog.Uint64("port", uint64(evt.Port)),
		slog.Uint64("consecutive_failures", uint64(evt.ConsecutiveFailures)))
	if s.audit != nil {
		if aerr := s.audit.LogServiceEvent(ctx, evt.NodeId, evt.JobId, evt.Ready,
			evt.Port, evt.HealthPath, evt.ConsecutiveFailures); aerr != nil {
			s.log.Warn("audit service event failed",
				slog.String("event", eventName), slog.Any("err", aerr))
		}
	}
	return &pb.Ack{Ok: true}, nil
}

// StreamLogs receives log chunks from nodes and stores them for later
// retrieval via GET /jobs/{id}/logs.
func (s *Server) StreamLogs(stream grpc.ClientStreamingServer[pb.LogChunk, pb.Ack]) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.Ack{Ok: true})
		}
		if err != nil {
			return err
		}

		if s.logStore != nil && len(chunk.Data) > 0 {
			entry := logstore.LogEntry{
				JobID: chunk.JobId,
				Seq:   chunk.Seq,
				Data:  string(chunk.Data),
			}
			if err := s.logStore.Append(stream.Context(), entry); err != nil {
				s.log.Warn("failed to store log chunk",
					slog.String("job_id", chunk.JobId),
					slog.Any("err", err))
			}
		}
	}
}
