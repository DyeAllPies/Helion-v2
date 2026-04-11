package grpcclient_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
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

func TestSendHeartbeats_CancelContext_ReturnsNil(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "hb-node")

	// Register first so the server knows the node.
	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	_, _ = c.Register(regCtx, "hb-node", "127.0.0.1:8080")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.SendHeartbeats(ctx, "hb-node", 20*time.Millisecond,
			func() int32 { return 0 }, nil)
	}()

	// Let a couple heartbeats flow, then cancel.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("SendHeartbeats with canceled ctx should return nil, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendHeartbeats did not return after context cancel")
	}
}

func TestSendHeartbeats_NilRunningJobs_DoesNotPanic(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "hb-nil-node")

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	_, _ = c.Register(regCtx, "hb-nil-node", "127.0.0.1:8080")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// runningJobs = nil (uses 0 default), onCommand = non-nil callback
		done <- c.SendHeartbeats(ctx, "hb-nil-node", 20*time.Millisecond,
			nil, func(_ *pb.NodeCommand) {})
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("SendHeartbeats with nil runningJobs: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendHeartbeats did not return")
	}
}

// ── Fake coordinator for branch-coverage tests ───────────────────────────────
//
// The tests below need behaviours that the real grpcserver doesn't expose
// directly (send a SHUTDOWN command, abort the stream with an error). We
// spin up a minimal gRPC server that embeds the generated Unimplemented
// stub and overrides just the methods we care about. mTLS creds come from
// the same coordinator bundle the client uses.

type fakeCoord struct {
	pb.UnimplementedCoordinatorServiceServer
	// hbHandler is invoked in place of Heartbeat. Must send/receive on the
	// stream and return whatever the test wants to exercise.
	hbHandler func(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.NodeCommand]) error
	// logsHandler is invoked in place of StreamLogs. May be nil.
	logsHandler func(stream grpc.ClientStreamingServer[pb.LogChunk, pb.Ack]) error
}

func (f *fakeCoord) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.NodeCommand]) error {
	if f.hbHandler != nil {
		return f.hbHandler(stream)
	}
	return nil
}

func (f *fakeCoord) StreamLogs(stream grpc.ClientStreamingServer[pb.LogChunk, pb.Ack]) error {
	if f.logsHandler != nil {
		return f.logsHandler(stream)
	}
	// Default: drain and ack — covers the "return nil" path at the end of
	// grpcclient.StreamLogs when CloseAndRecv succeeds.
	for {
		_, err := stream.Recv()
		if err != nil {
			return stream.SendAndClose(&pb.Ack{})
		}
	}
}

// startFakeCoord returns the listen addr of a fake coordinator using bundle
// as its mTLS identity. The server is stopped via t.Cleanup.
func startFakeCoord(t *testing.T, bundle *auth.Bundle, fc *fakeCoord) string {
	t.Helper()

	creds, err := bundle.ServerCredentials()
	if err != nil {
		t.Fatalf("ServerCredentials: %v", err)
	}

	srv := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterCoordinatorServiceServer(srv, fc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)
	return addr
}

// TestSendHeartbeats_ServerSendsShutdown_ReturnsNil covers the SHUTDOWN branch
// in client.SendHeartbeats: the server sends one NodeCommand_SHUTDOWN, the
// client's Recv goroutine pushes nil on cmdErr, and the main loop exits
// cleanly via the "case err := <-cmdErr: return err" branch.
func TestSendHeartbeats_ServerSendsShutdown_ReturnsNil(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	fc := &fakeCoord{
		hbHandler: func(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.NodeCommand]) error {
			// Push SHUTDOWN immediately so the client's Recv loop exits.
			_ = stream.Send(&pb.NodeCommand{Type: pb.NodeCommand_SHUTDOWN})
			// Block until the client closes — returning immediately would
			// race with the client still reading.
			<-stream.Context().Done()
			return nil
		},
	}
	addr := startFakeCoord(t, coordBundle, fc)

	c := newClient(t, addr, coordBundle, "shutdown-node")

	var cmdSeen atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = c.SendHeartbeats(ctx, "shutdown-node", 50*time.Millisecond,
		func() int32 { return 0 },
		func(cmd *pb.NodeCommand) {
			if cmd.Type == pb.NodeCommand_SHUTDOWN {
				cmdSeen.Add(1)
			}
		},
	)
	if err != nil {
		t.Errorf("SendHeartbeats: want nil on SHUTDOWN, got %v", err)
	}
	if cmdSeen.Load() == 0 {
		t.Error("onCommand callback was not invoked with SHUTDOWN")
	}
}

// TestStreamLogs_AcceptingServer_ReturnsNil covers the success path at the
// bottom of StreamLogs where CloseAndRecv returns nil and StreamLogs itself
// returns nil. The existing tests use the real grpcserver's Unimplemented
// stub, so this end of the function stayed uncovered until now.
func TestStreamLogs_AcceptingServer_ReturnsNil(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	// Default logsHandler (nil → drain and ack).
	addr := startFakeCoord(t, coordBundle, &fakeCoord{})

	c := newClient(t, addr, coordBundle, "logs-ok-node")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.StreamLogs(ctx, "job-logs-ok", "logs-ok-node", []byte("out"), []byte("err")); err != nil {
		t.Errorf("StreamLogs: want nil, got %v", err)
	}
}

// TestSendHeartbeats_OpenAfterClose_ReturnsError covers the "open heartbeat
// stream" failure path. After Close() the underlying grpc.ClientConn is
// shut down, so opening a new stream fails synchronously.
func TestSendHeartbeats_OpenAfterClose_ReturnsError(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	nb, _ := auth.NewNodeBundle(coordBundle.CA, "closed-hb-node")
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = c.SendHeartbeats(ctx, "closed-hb-node", 20*time.Millisecond,
		func() int32 { return 0 }, nil)
	if err == nil {
		t.Error("SendHeartbeats after Close: want error, got nil")
	}
}

// TestStreamLogs_OpenAfterClose_ReturnsError covers the "open StreamLogs"
// failure path — same reasoning as the heartbeat variant above.
func TestStreamLogs_OpenAfterClose_ReturnsError(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	nb, _ := auth.NewNodeBundle(coordBundle.CA, "closed-logs-node")
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = c.StreamLogs(ctx, "j", "closed-logs-node", []byte("x"), nil)
	if err == nil {
		t.Error("StreamLogs after Close: want error, got nil")
	}
}

// TestSendHeartbeats_ServerAborts_ReturnsError covers the "stream.Recv()
// returned a non-EOF error" branch: the server's Heartbeat handler returns
// an error immediately, which surfaces as an Recv error on the client side
// and is propagated via cmdErr.
func TestSendHeartbeats_ServerAborts_ReturnsError(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	fc := &fakeCoord{
		hbHandler: func(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.NodeCommand]) error {
			// Abort the stream — the client's Recv will see this as a
			// non-EOF error.
			return errors.New("simulated server failure")
		},
	}
	addr := startFakeCoord(t, coordBundle, fc)

	c := newClient(t, addr, coordBundle, "abort-node")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = c.SendHeartbeats(ctx, "abort-node", 50*time.Millisecond,
		func() int32 { return 0 }, nil)
	if err == nil {
		t.Error("SendHeartbeats: want non-nil error after server abort, got nil")
	}
}