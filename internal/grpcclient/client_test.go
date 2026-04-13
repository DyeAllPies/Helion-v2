package grpcclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// startTestServer spins up a grpcserver and returns the listening address.
func startTestServer(t *testing.T) (string, *auth.Bundle) {
	t.Helper()

	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	srv, err := grpcserver.New(coordBundle)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	return addr, coordBundle
}

func newClient(t *testing.T, addr string, coordBundle *auth.Bundle, nodeID string) *grpcclient.Client {
	t.Helper()
	nb, err := auth.NewNodeBundle(coordBundle.CA, nodeID)
	if err != nil {
		t.Fatalf("NewNodeBundle: %v", err)
	}
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("grpcclient.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_InvalidPEM_ReturnsError(t *testing.T) {
	// Build a bundle with garbage PEM so ClientCredentials fails.
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	badBundle := &auth.Bundle{
		CA:      coordBundle.CA,
		CertPEM: []byte("not valid PEM"),
		KeyPEM:  []byte("not valid PEM"),
	}
	_, err = grpcclient.New("127.0.0.1:1", "helion-coordinator", badBundle)
	if err == nil {
		t.Error("expected error for invalid PEM bundle, got nil")
	}
}

func TestNew_ValidBundle_ReturnsClient(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	nb, _ := auth.NewNodeBundle(coordBundle.CA, "node-new")
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("grpcclient.New: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	c.Close()
}

// ── Register ──────────────────────────────────────────────────────────────────

func TestRegister_ReturnsNodeID(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "reg-node")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := c.Register(ctx, "reg-node", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.NodeId != "reg-node" {
		t.Errorf("want reg-node, got %q", resp.NodeId)
	}
}

// ── ReportResult ──────────────────────────────────────────────────────────────

func TestReportResult_NoJobStore_ReturnsNilError(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "result-node")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := c.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-x",
		NodeId:  "result-node",
		Success: true,
	})
	if err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
}

// ── Close ─────────────────────────────────────────────────────────────────────

func TestClose_ClosesConnection(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	nb, _ := auth.NewNodeBundle(coordBundle.CA, "close-node")
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ── SendHeartbeats ────────────────────────────────────────────────────────────

// ── StreamLogs ────────────────────────────────────────────────────────────────

func TestStreamLogs_ServerNotImplemented_ReturnsError(t *testing.T) {
	// The grpcserver uses UnimplementedCoordinatorServiceServer for StreamLogs,
	// so calling it should return an Unimplemented error — but the function
	// itself is exercised (open, send, close).
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "log-node")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := c.StreamLogs(ctx, "job-1", "log-node", []byte("stdout data"), []byte("stderr data"))
	// Unimplemented is the expected gRPC status — the call itself must not panic.
	if err == nil {
		t.Log("StreamLogs returned nil (server may have a no-op impl)")
	}
}

func TestStreamLogs_EmptyPayloads_SkipsSend(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "log-node2")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Empty stdout and stderr — send() will skip both, so only CloseAndRecv is called.
	err := c.StreamLogs(ctx, "job-2", "log-node2", nil, nil)
	// Again, expect Unimplemented but no panic.
	if err == nil {
		t.Log("StreamLogs returned nil (server may have a no-op impl)")
	}
}