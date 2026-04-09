// internal/grpcclient/client.go
package grpcclient

import (
	"context"
	"fmt"

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

// Close tears down the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
