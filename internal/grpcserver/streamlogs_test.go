// internal/grpcserver/streamlogs_test.go
//
// Tests for the StreamLogs RPC handler.

package grpcserver_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	"github.com/DyeAllPies/Helion-v2/internal/logstore"
)

func TestStreamLogs_StoresChunks(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	ls := logstore.NewMemLogStore()

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithLogStore(ls),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	addr := listenAndServe(t, srv)
	client := dialClient(t, addr, coordBundle, "log-node")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.StreamLogs(ctx, "log-job-1", "log-node",
		[]byte("stdout data"), []byte("stderr data"))
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	// Verify logs were stored.
	entries, err := ls.Get(ctx, "log-job-1")
	if err != nil {
		t.Fatalf("Get logs: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 log entries, got %d", len(entries))
	}
	if entries[0].Data != "stdout data" {
		t.Errorf("first entry = %q, want 'stdout data'", entries[0].Data)
	}
	if entries[1].Data != "stderr data" {
		t.Errorf("second entry = %q, want 'stderr data'", entries[1].Data)
	}
}

func TestStreamLogs_EmptyPayload_NoEntries(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	ls := logstore.NewMemLogStore()

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithLogStore(ls),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	addr := listenAndServe(t, srv)
	client := dialClient(t, addr, coordBundle, "log-empty-node")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.StreamLogs(ctx, "empty-log-job", "log-empty-node", nil, nil)
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	entries, _ := ls.Get(ctx, "empty-log-job")
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty payload, got %d", len(entries))
	}
}

// ── Test helpers ─────────────────────────────────────────────────────────────

func listenAndServe(t *testing.T, srv *grpcserver.Server) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)
	return addr
}

func dialClient(t *testing.T, addr string, coordBundle *auth.Bundle, nodeID string) *grpcclient.Client {
	t.Helper()
	nb, err := auth.NewNodeBundle(coordBundle.CA, nodeID)
	if err != nil {
		t.Fatalf("NewNodeBundle: %v", err)
	}
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
