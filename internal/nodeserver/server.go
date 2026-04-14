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
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	"github.com/DyeAllPies/Helion-v2/internal/staging"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements pb.NodeServiceServer.
type Server struct {
	pb.UnimplementedNodeServiceServer

	rt          runtime.Runtime
	stager      *staging.Stager    // optional; nil disables artifact staging
	client      *grpcclient.Client // coordinator gRPC client for callbacks
	nodeID      string
	runtimeName string // "go" or "rust" — reported in JobResult
	runningJobs atomic.Int32
	totalJobs   atomic.Int32
	log         *slog.Logger
}

// New returns a Server backed by rt.
// client and nodeID are used to call ReportResult and StreamLogs on the
// coordinator after each job completes.
// runtimeName is "go" or "rust" — included in every JobResult so the
// coordinator knows which backend executed the job.
// stager may be nil for nodes that should not participate in the ML
// artifact pipeline; Dispatch will reject jobs that carry artifact
// bindings when the stager is disabled.
func New(rt runtime.Runtime, stager *staging.Stager, client *grpcclient.Client, nodeID, runtimeName string, log *slog.Logger) *Server {
	return &Server{rt: rt, stager: stager, client: client, nodeID: nodeID, runtimeName: runtimeName, log: log}
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
	var lim runtime.ResourceLimits
	if req.Limits != nil {
		lim = runtime.ResourceLimits{
			MemoryBytes: req.Limits.MemoryBytes,
			CPUQuotaUS:  req.Limits.CpuQuotaUs,
			CPUPeriodUS: req.Limits.CpuPeriodUs,
		}
	}

	// Step 2 — stage artifact inputs and outputs. When the job carries
	// no bindings (legacy / non-ML path) we skip the stager entirely
	// so existing behaviour is unchanged. A stager-less node that
	// receives a job with bindings refuses it rather than executing
	// the command blind to its declared I/O.
	hasBindings := len(req.Inputs) > 0 || len(req.Outputs) > 0 || req.WorkingDir != ""
	var prepared *staging.Prepared
	if hasBindings {
		if s.stager == nil {
			msg := "node has no artifact stager; job declares inputs/outputs"
			s.log.Warn(msg, slog.String("job_id", req.JobId))
			s.reportResult(ctx, req.JobId, false, -1, msg, startedAt, time.Now(), nil)
			return &pb.DispatchAck{JobId: req.JobId, Accepted: false, Error: msg}, nil
		}
		job := jobFromDispatch(req)
		p, perr := s.stager.Prepare(ctx, job)
		if perr != nil {
			s.log.Error("staging prepare", slog.String("job_id", req.JobId), slog.Any("err", perr))
			s.reportResult(ctx, req.JobId, false, -1, perr.Error(), startedAt, time.Now(), nil)
			return &pb.DispatchAck{JobId: req.JobId, Accepted: false, Error: perr.Error()}, nil
		}
		prepared = p
	}

	runReq := runtime.RunRequest{
		JobID:          req.JobId,
		Command:        req.Command,
		Args:           req.Args,
		Env:            mergeEnv(req.Env, prepared),
		TimeoutSeconds: req.TimeoutSeconds,
		Limits:         lim,
	}
	if prepared != nil {
		runReq.WorkingDir = prepared.WorkingDir
	}
	result, err := s.rt.Run(ctx, runReq)
	finishedAt := time.Now()

	// Finalize runs on both success and failure so the working
	// directory is always cleaned up; it only uploads outputs when
	// the job exited zero and no KillReason fired.
	var outputURIs []staging.ResolvedOutput
	if prepared != nil {
		success := err == nil && result.ExitCode == 0 && result.KillReason == ""
		res, ferr := s.stager.Finalize(ctx, prepared, success)
		if ferr != nil {
			s.log.Error("staging finalize",
				slog.String("job_id", req.JobId), slog.Any("err", ferr))
			// Upload failure degrades the job to failed even if the
			// process exited zero — the caller cannot trust outputs
			// that did not make it into the artifact store.
			if err == nil {
				err = ferr
			}
		}
		outputURIs = res
	}

	if err != nil {
		s.log.Error("runtime error", slog.String("job_id", req.JobId), slog.Any("err", err))
		s.reportResult(ctx, req.JobId, false, -1, err.Error(), startedAt, finishedAt, nil)
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
	s.reportResult(ctx, req.JobId, success, result.ExitCode, errMsg, startedAt, finishedAt, outputURIs)

	return &pb.DispatchAck{
		JobId:    req.JobId,
		Accepted: true,
		Error:    errMsg,
	}, nil
}

// reportResult calls coordinator.ReportResult in a best-effort manner.
// Failures are logged but do not cause Dispatch to fail. outputs is
// the stager's resolved artifact URIs and is only populated on the
// success path — for failed/crashed jobs the slice is nil because
// Finalize skipped uploads.
func (s *Server) reportResult(
	ctx context.Context,
	jobID string,
	success bool,
	exitCode int32,
	errMsg string,
	startedAt, finishedAt time.Time,
	outputs []staging.ResolvedOutput,
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
		FinishedAt: timestamppb.New(finishedAt),
		Runtime:    s.runtimeName,
		Outputs:    artifactOutputsToProto(outputs),
	}
	if err := s.client.ReportResult(ctx, rr); err != nil {
		s.log.Warn("ReportResult failed", slog.String("job_id", jobID), slog.Any("err", err))
	}
}

// artifactOutputsToProto lifts the stager's ResolvedOutput slice onto
// the wire-format pb.ArtifactOutput. nil in -> nil out so a job with
// no outputs sends an absent (not empty) repeated field.
func artifactOutputsToProto(src []staging.ResolvedOutput) []*pb.ArtifactOutput {
	if len(src) == 0 {
		return nil
	}
	out := make([]*pb.ArtifactOutput, len(src))
	for i, o := range src {
		out[i] = &pb.ArtifactOutput{
			Name:      o.Name,
			Uri:       string(o.URI),
			Size:      o.Size,
			Sha256:    o.SHA256,
			LocalPath: o.LocalPath,
		}
	}
	return out
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

// jobFromDispatch lifts the wire-format DispatchRequest onto the
// internal cpb.Job shape that staging.Prepare expects. Only the fields
// the stager actually reads are populated.
func jobFromDispatch(req *pb.DispatchRequest) *cpb.Job {
	job := &cpb.Job{ID: req.JobId, WorkingDir: req.WorkingDir}
	for _, b := range req.Inputs {
		job.Inputs = append(job.Inputs, cpb.ArtifactBinding{
			Name: b.Name, URI: b.Uri, LocalPath: b.LocalPath, SHA256: b.Sha256,
		})
	}
	for _, b := range req.Outputs {
		job.Outputs = append(job.Outputs, cpb.ArtifactBinding{
			Name: b.Name, URI: b.Uri, LocalPath: b.LocalPath,
		})
	}
	return job
}

// mergeEnv returns a new env map with the stager's additions layered on
// top of whatever the caller supplied. Stager keys (HELION_INPUT_*,
// HELION_OUTPUT_*) take precedence so a malicious job cannot shadow
// them by sending a same-named entry in req.Env.
func mergeEnv(base map[string]string, p *staging.Prepared) map[string]string {
	if p == nil || len(p.EnvAdditions) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(p.EnvAdditions))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range p.EnvAdditions {
		out[k] = v
	}
	return out
}

// GetMetrics returns current node metrics.
func (s *Server) GetMetrics(_ context.Context, _ *pb.Empty) (*pb.NodeMetrics, error) {
	return &pb.NodeMetrics{
		RunningJobs: s.runningJobs.Load(),
		TotalJobs:   s.totalJobs.Load(),
	}, nil
}
