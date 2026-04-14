// internal/grpcclient/client.go
//
// Client is a node agent's gRPC connection to the coordinator.
//
// Heartbeat
// ─────────
// SendHeartbeats opens the bidi-streaming Heartbeat RPC and sends one
// HeartbeatMessage per tick until ctx is cancelled.  It reads NodeCommand
// responses from the server; SHUTDOWN causes it to return early.
//
// The caller (node agent main loop) is responsible for calling this in a
// goroutine and cancelling ctx on shutdown.

package grpcclient

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
)

// Client is a node agent's gRPC connection to the coordinator.
type Client struct {
	conn   *grpc.ClientConn
	Client pb.CoordinatorServiceClient
}

// New dials the coordinator with mTLS credentials.
func New(addr, serverName string, bundle *auth.Bundle) (*Client, error) {
	creds, err := bundle.ClientCredentials(serverName)
	if err != nil {
		return nil, fmt.Errorf("client credentials: %w", err)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	return &Client{
		conn:   conn,
		Client: pb.NewCoordinatorServiceClient(conn),
	}, nil
}

// Register sends a registration request to the coordinator.
func (c *Client) Register(ctx context.Context, nodeID, address string) (*pb.RegisterResponse, error) {
	return c.Client.Register(ctx, &pb.RegisterRequest{
		NodeId:  nodeID,
		Address: address,
	})
}

// RegisterWithLabels is the label-aware variant of Register. Labels are
// forwarded to the coordinator's node_selector filter. Sanitisation
// (NUL/control rejection, size caps) lives on the coordinator side so
// a compromised node that bypasses this client cannot smuggle invalid
// labels through.
func (c *Client) RegisterWithLabels(ctx context.Context, nodeID, address string, labels map[string]string) (*pb.RegisterResponse, error) {
	return c.Client.Register(ctx, &pb.RegisterRequest{
		NodeId:  nodeID,
		Address: address,
		Labels:  labels,
	})
}

// NodeCapacity holds the resource capacity of this node, reported in heartbeats.
type NodeCapacity struct {
	CpuMillicores   uint32
	TotalMemBytes   uint64
	MaxSlots        uint32
	TotalGpus       uint32 // whole-GPU count (0 on CPU-only hosts)
}

// SendHeartbeats opens the Heartbeat bidi-stream and sends one message every
// interval until ctx is cancelled or the server sends a SHUTDOWN command.
//
// nodeID identifies this node.
// runningJobs is the current job count reported to the coordinator.
// capacity is the node's total resource capacity (reported every tick).
// onCommand is called for each NodeCommand received; may be nil.
//
// Returns nil on clean shutdown (ctx cancelled or SHUTDOWN received).
// Returns an error if the stream fails unexpectedly.
func (c *Client) SendHeartbeats(
	ctx context.Context,
	nodeID string,
	interval time.Duration,
	runningJobs func() int32, // called each tick to get current count
	capacity *NodeCapacity, // node resource capacity; may be nil
	onCommand func(*pb.HeartbeatAck), // called for each server ack; may be nil
) error {
	stream, err := c.Client.Heartbeat(ctx)
	if err != nil {
		return fmt.Errorf("open heartbeat stream: %w", err)
	}

	// Receive loop — runs in a goroutine so sends and receives are concurrent.
	cmdErr := make(chan error, 1)
	go func() {
		for {
			cmd, err := stream.Recv()
			if err == io.EOF || ctx.Err() != nil {
				cmdErr <- nil
				return
			}
			if err != nil {
				cmdErr <- err
				return
			}
			if onCommand != nil {
				onCommand(cmd)
			}
			if cmd.Command == pb.NodeCommand_NODE_COMMAND_SHUTDOWN {
				cmdErr <- nil
				return
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var seq uint64
	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return nil

		case err := <-cmdErr:
			return err

		case <-ticker.C:
			jobs := int32(0)
			if runningJobs != nil {
				jobs = runningJobs()
			}
			msg := &pb.HeartbeatMessage{
				NodeId:      nodeID,
				RunningJobs: jobs,
			}
			if capacity != nil {
				msg.CpuMillicores = capacity.CpuMillicores
				msg.TotalMemoryBytes = capacity.TotalMemBytes
				msg.MaxSlots = capacity.MaxSlots
				msg.TotalGpus = capacity.TotalGpus
			}
			// Increment seq locally — proto field unused by coordinator for now
			// but useful for debugging dropped messages.
			seq++
			_ = seq

			if err := stream.Send(msg); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("heartbeat send: %w", err)
			}
		}
	}
}

// ReportResult sends a job completion result to the coordinator.
func (c *Client) ReportResult(ctx context.Context, result *pb.JobResult) error {
	_, err := c.Client.ReportResult(ctx, result)
	return err
}

// StreamLogs sends captured stdout and stderr for a completed job to the
// coordinator.  Each non-empty slice is sent as one LogChunk.
func (c *Client) StreamLogs(ctx context.Context, jobID, nodeID string, stdout, stderr []byte) error {
	stream, err := c.Client.StreamLogs(ctx)
	if err != nil {
		return fmt.Errorf("open StreamLogs: %w", err)
	}

	var seq uint64
	send := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		seq++
		return stream.Send(&pb.LogChunk{
			JobId:  jobID,
			NodeId: nodeID,
			Data:   data,
			Seq:    seq,
		})
	}

	if err := send(stdout); err != nil {
		return fmt.Errorf("stream stdout: %w", err)
	}
	if err := send(stderr); err != nil {
		return fmt.Errorf("stream stderr: %w", err)
	}

	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("close StreamLogs: %w", err)
	}
	return nil
}

// Close tears down the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
