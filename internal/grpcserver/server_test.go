// internal/grpcserver/server_test.go
//
// Construction (New / options), Register RPC, and Serve lifecycle tests.
// RPC-specific tests live in interceptors_test.go, report_result_test.go,
// and heartbeat_test.go. Shared mocks live in testhelpers_test.go.

package grpcserver_test

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
)

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_WithNoOptions_Succeeds(t *testing.T) {
	bundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	srv, err := grpcserver.New(bundle)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestNew_WithAllOptions_Succeeds(t *testing.T) {
	bundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	rev := &mockRevocationChecker{revoked: map[string]bool{}}
	rl := &mockRateLimiter{rate: 10}
	al := &mockAuditLogger{}
	js := newMockJobStore()

	srv, err := grpcserver.New(bundle,
		grpcserver.WithRevocationChecker(rev),
		grpcserver.WithRateLimiter(rl),
		grpcserver.WithAuditLogger(al),
		grpcserver.WithJobStore(js),
	)
	if err != nil {
		t.Fatalf("grpcserver.New with all options: %v", err)
	}
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

// ── WithRegistry / WithLogger ──────────────────────────────────────────────────

func TestWithRegistry_DelegatesToRegistry(t *testing.T) {
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

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "reg-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.Register(ctx, "reg-node", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register with registry: %v", err)
	}
	if resp.NodeId != "reg-node" {
		t.Errorf("want reg-node, got %q", resp.NodeId)
	}
}

func TestWithLogger_DoesNotPanic(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	srv, err := grpcserver.New(coordBundle, grpcserver.WithLogger(slog.Default()))
	if err != nil {
		t.Fatalf("grpcserver.New with logger: %v", err)
	}
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

// ── Register ──────────────────────────────────────────────────────────────────

func TestRegister_NoRegistry_EchoesNodeID(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
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

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "echo-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.Register(ctx, "echo-node", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.NodeId != "echo-node" {
		t.Errorf("want echo-node, got %q", resp.NodeId)
	}
}

// ── Serve error path ──────────────────────────────────────────────────────────

func TestServe_InvalidAddr_ReturnsError(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}
	srv, err := grpcserver.New(coordBundle)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	// "bad-address" is not a valid TCP address — Listen should fail.
	if err := srv.Serve("bad-address"); err == nil {
		srv.Stop()
		t.Error("expected error for invalid listen address, got nil")
	}
}
