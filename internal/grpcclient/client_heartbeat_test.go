// internal/grpcclient/client_heartbeat_test.go
//
// Tests for SendHeartbeats: cancel, nil args, shutdown command, server abort,
// and post-close error paths. Uses a fakeCoord for branch-coverage tests
// that need behaviours the real grpcserver doesn't expose.

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
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
)

func TestSendHeartbeats_CancelContext_ReturnsNil(t *testing.T) {
	addr, coordBundle := startTestServer(t)
	c := newClient(t, addr, coordBundle, "hb-node")

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	_, _ = c.Register(regCtx, "hb-node", "127.0.0.1:8080")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.SendHeartbeats(ctx, "hb-node", 20*time.Millisecond,
			func() int32 { return 0 }, nil, nil)
	}()

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
		done <- c.SendHeartbeats(ctx, "hb-nil-node", 20*time.Millisecond,
			nil, nil, func(_ *pb.HeartbeatAck) {})
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

type fakeCoord struct {
	pb.UnimplementedCoordinatorServiceServer
	hbHandler   func(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.HeartbeatAck]) error
	logsHandler func(stream grpc.ClientStreamingServer[pb.LogChunk, pb.Ack]) error
}

func (f *fakeCoord) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.HeartbeatAck]) error {
	if f.hbHandler != nil {
		return f.hbHandler(stream)
	}
	return nil
}

func (f *fakeCoord) StreamLogs(stream grpc.ClientStreamingServer[pb.LogChunk, pb.Ack]) error {
	if f.logsHandler != nil {
		return f.logsHandler(stream)
	}
	for {
		_, err := stream.Recv()
		if err != nil {
			return stream.SendAndClose(&pb.Ack{})
		}
	}
}

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

func TestSendHeartbeats_ServerSendsShutdown_ReturnsNil(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	fc := &fakeCoord{
		hbHandler: func(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.HeartbeatAck]) error {
			_ = stream.Send(&pb.HeartbeatAck{Command: pb.NodeCommand_NODE_COMMAND_SHUTDOWN})
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
		nil,
		func(ack *pb.HeartbeatAck) {
			if ack.Command == pb.NodeCommand_NODE_COMMAND_SHUTDOWN {
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

func TestStreamLogs_AcceptingServer_ReturnsNil(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	addr := startFakeCoord(t, coordBundle, &fakeCoord{})
	c := newClient(t, addr, coordBundle, "logs-ok-node")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.StreamLogs(ctx, "job-logs-ok", "logs-ok-node", []byte("out"), []byte("err")); err != nil {
		t.Errorf("StreamLogs: want nil, got %v", err)
	}
}

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
		func() int32 { return 0 }, nil, nil)
	if err == nil {
		t.Error("SendHeartbeats after Close: want error, got nil")
	}
}

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

func TestSendHeartbeats_ServerAborts_ReturnsError(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	fc := &fakeCoord{
		hbHandler: func(stream grpc.BidiStreamingServer[pb.HeartbeatMessage, pb.HeartbeatAck]) error {
			return errors.New("simulated server failure")
		},
	}
	addr := startFakeCoord(t, coordBundle, fc)
	c := newClient(t, addr, coordBundle, "abort-node")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = c.SendHeartbeats(ctx, "abort-node", 50*time.Millisecond,
		func() int32 { return 0 }, nil, nil)
	if err == nil {
		t.Error("SendHeartbeats: want non-nil error after server abort, got nil")
	}
}
