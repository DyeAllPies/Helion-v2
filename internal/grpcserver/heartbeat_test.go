// internal/grpcserver/heartbeat_test.go
//
// Tests for the Heartbeat streaming RPC and the server-side CancelStream API.

package grpcserver_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
)

// ── Heartbeat ─────────────────────────────────────────────────────────────────

func TestHeartbeat_SendAndCancel_ReturnsNil(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	registry := cluster.NewRegistry(cluster.NopPersister{}, time.Second, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(registry))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "hb-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	_, _ = client.Register(regCtx, "hb-node", "127.0.0.1:8080")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.SendHeartbeats(ctx, "hb-node", 20*time.Millisecond,
			func() int32 { return 0 }, nil)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("SendHeartbeats returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendHeartbeats did not return after cancel")
	}
}

func TestHeartbeat_NoRegistry_AcceptsAndResponds(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	// No registry injected — Heartbeat still accepts the stream.
	srv, err := grpcserver.New(coordBundle)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "hb-noregistry")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.SendHeartbeats(ctx, "hb-noregistry", 20*time.Millisecond,
			func() int32 { return 0 }, nil)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("SendHeartbeats (no registry): %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendHeartbeats did not return")
	}
}

func TestHeartbeat_RateLimited_TerminatesStream(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	rl := &mockRateLimiter{
		rate:    5,
		blocked: map[string]bool{"rl-hb-node": true},
	}
	al := &mockAuditLogger{}

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithRateLimiter(rl),
		grpcserver.WithAuditLogger(al),
	)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "rl-hb-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.SendHeartbeats(ctx, "rl-hb-node", 20*time.Millisecond,
		func() int32 { return 0 }, nil)
	if err == nil {
		t.Error("expected rate-limit error from heartbeat stream, got nil")
	}
}

// ── CancelStream ──────────────────────────────────────────────────────────────

// TestCancelStream_TerminatesActiveHeartbeat verifies that calling CancelStream
// on a node with an active heartbeat stream causes the stream to terminate.
func TestCancelStream_TerminatesActiveHeartbeat(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	registry := cluster.NewRegistry(cluster.NopPersister{}, time.Second, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(registry))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "cancel-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	_, _ = client.Register(regCtx, "cancel-node", "127.0.0.1:8080")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.SendHeartbeats(ctx, "cancel-node", 20*time.Millisecond,
			func() int32 { return 0 }, nil)
	}()

	// Let the heartbeat stream establish.
	time.Sleep(80 * time.Millisecond)

	// Cancel the stream server-side.
	srv.CancelStream("cancel-node")

	select {
	case streamErr := <-done:
		if streamErr == nil {
			t.Error("expected SendHeartbeats to return an error after CancelStream, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendHeartbeats did not return after CancelStream")
	}
}

// TestHeartbeat_WithRegistry_UnregisteredNode_TerminatesStream covers the
// cluster.ErrNodeNotRegistered branch in Heartbeat: when the node sends a
// heartbeat without first calling Register, the server detects it via the
// registry and closes the stream with codes.NotFound.
func TestHeartbeat_WithRegistry_UnregisteredNode_TerminatesStream(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	registry := cluster.NewRegistry(cluster.NopPersister{}, time.Second, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(registry))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "unreg-hb")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Skip Register — send heartbeats directly. The server will reject the
	// first one with NotFound.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.SendHeartbeats(ctx, "unreg-hb", 20*time.Millisecond,
		func() int32 { return 0 }, nil)
	if err == nil {
		t.Error("expected error from unregistered heartbeat stream, got nil")
	}
}

// TestHeartbeat_SecondStreamReplaces_ClosesFirst covers the "close old" branch
// in registerStream: when a node reopens a heartbeat stream, the server must
// close the channel for the previous one to unblock CancelStream.
func TestHeartbeat_SecondStreamReplaces_ClosesFirst(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	registry := cluster.NewRegistry(cluster.NopPersister{}, time.Second, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(registry))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "dual-stream")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	_, _ = client.Register(regCtx, "dual-stream", "127.0.0.1:1")

	// Start the first stream — let it send a few heartbeats so the stream
	// is registered server-side, then cancel it.
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() {
		done1 <- client.SendHeartbeats(ctx1, "dual-stream", 20*time.Millisecond,
			func() int32 { return 0 }, nil)
	}()
	time.Sleep(80 * time.Millisecond)

	// Start the second stream concurrently — the server's registerStream
	// should close the first entry's channel before replacing it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() {
		done2 <- client.SendHeartbeats(ctx2, "dual-stream", 20*time.Millisecond,
			func() int32 { return 0 }, nil)
	}()
	time.Sleep(80 * time.Millisecond)

	cancel1()
	cancel2()
	<-done1
	<-done2
}

// TestCancelStream_NoopOnMissingNode verifies that CancelStream with an
// unknown nodeID does not panic.
func TestCancelStream_NoopOnMissingNode(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	srv, err := grpcserver.New(coordBundle)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}
	// Should not panic.
	srv.CancelStream("ghost-node")
}
