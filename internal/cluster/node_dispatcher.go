// internal/cluster/node_dispatcher.go
//
// GRPCNodeDispatcher implements NodeDispatcher by dialing the target node
// over gRPC (with mTLS) and calling the NodeService.Dispatch RPC.

package cluster

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// minDispatchRPCTimeout is the floor timeout for a Dispatch RPC when
// the job hasn't declared a TimeoutSeconds. It also covers dial +
// connection setup. Kept at the original 10 s so any job with no
// declared timeout behaves like it did before feature 21.
const minDispatchRPCTimeout = 10 * time.Second

// dispatchRPCBuffer is the slack added on top of job.TimeoutSeconds
// for the Dispatch RPC: the node's Dispatch handler runs the job
// synchronously then has to upload outputs, stream stdout/stderr,
// and ReportResult before returning the ACK. 30 s covers those on
// every pipeline we've profiled (iris, MNIST, GPU tests).
const dispatchRPCBuffer = 30 * time.Second

// dispatchRPCTimeout picks the Dispatch RPC timeout for a job. Batch
// jobs with a declared TimeoutSeconds get TimeoutSeconds + buffer —
// the node Dispatch handler blocks on rt.Run until the subprocess
// exits, so a fixed short timeout would cancel any job whose runtime
// exceeds it (this is how MNIST ingest + train used to get killed at
// the 10 s mark). Service jobs ACK immediately from a goroutine, so
// the floor is fine. Jobs with no TimeoutSeconds also use the floor.
func dispatchRPCTimeout(job *cpb.Job) time.Duration {
	if job.Service != nil {
		return minDispatchRPCTimeout
	}
	if job.TimeoutSeconds <= 0 {
		return minDispatchRPCTimeout
	}
	return time.Duration(job.TimeoutSeconds)*time.Second + dispatchRPCBuffer
}

// GRPCNodeDispatcher dispatches jobs to nodes via gRPC.
type GRPCNodeDispatcher struct {
	tlsCfg *tls.Config // coordinator's client TLS config for dialing nodes
}

// NewGRPCNodeDispatcher creates a dispatcher with the given TLS config.
// The TLS config should be the coordinator's client credentials (from the auth bundle).
func NewGRPCNodeDispatcher(tlsCfg *tls.Config) *GRPCNodeDispatcher {
	return &GRPCNodeDispatcher{tlsCfg: tlsCfg}
}

// bindingsToProto lifts the persisted cpb.ArtifactBinding slice onto the
// wire-format pb.ArtifactBinding. nil in -> nil out, so a job with no
// inputs / outputs sends an absent (not empty) repeated field.
func bindingsToProto(bs []cpb.ArtifactBinding) []*pb.ArtifactBinding {
	if len(bs) == 0 {
		return nil
	}
	out := make([]*pb.ArtifactBinding, len(bs))
	for i, b := range bs {
		out[i] = &pb.ArtifactBinding{
			Name:      b.Name,
			Uri:       b.URI,
			LocalPath: b.LocalPath,
			Sha256:    b.SHA256,
		}
	}
	return out
}

// DispatchToNode sends a job to the node at nodeAddr via gRPC Dispatch RPC.
// See dispatchRPCTimeout above for the timeout-derivation reasoning.
func (d *GRPCNodeDispatcher) DispatchToNode(ctx context.Context, nodeAddr string, job *cpb.Job) error {
	rpcCtx, cancel := context.WithTimeout(ctx, dispatchRPCTimeout(job))
	defer cancel()

	creds := credentials.NewTLS(d.tlsCfg)
	conn, err := grpc.NewClient(nodeAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("dial node %s: %w", nodeAddr, err)
	}
	defer conn.Close()

	client := pb.NewNodeServiceClient(conn)

	req := &pb.DispatchRequest{
		JobId:          job.ID,
		Command:        job.Command,
		Args:           job.Args,
		Env:            job.Env,
		TimeoutSeconds: job.TimeoutSeconds,
		WorkingDir:     job.WorkingDir,
		Inputs:         bindingsToProto(job.Inputs),
		Outputs:        bindingsToProto(job.Outputs),
		NodeSelector:   job.NodeSelector,
		Gpus:           job.Resources.GPUs,
	}
	// Feature 17 — forward the service block so the node-side runtime
	// can skip timeout enforcement and start its health prober.
	if job.Service != nil {
		req.Service = &pb.ServiceSpec{
			Port:            job.Service.Port,
			HealthPath:      job.Service.HealthPath,
			HealthInitialMs: job.Service.HealthInitialMS,
		}
	}

	ack, err := client.Dispatch(rpcCtx, req)
	if err != nil {
		return fmt.Errorf("Dispatch RPC to %s: %w", nodeAddr, err)
	}
	if !ack.Accepted {
		return fmt.Errorf("node %s rejected job %s: %s", nodeAddr, job.ID, ack.Error)
	}

	return nil
}
