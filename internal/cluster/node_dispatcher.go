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
func (d *GRPCNodeDispatcher) DispatchToNode(ctx context.Context, nodeAddr string, job *cpb.Job) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
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

	ack, err := client.Dispatch(dialCtx, req)
	if err != nil {
		return fmt.Errorf("Dispatch RPC to %s: %w", nodeAddr, err)
	}
	if !ack.Accepted {
		return fmt.Errorf("node %s rejected job %s: %s", nodeAddr, job.ID, ack.Error)
	}

	return nil
}
