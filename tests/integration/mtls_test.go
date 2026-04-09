// tests/integration/mtls_test.go
package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

func TestMTLSHandshake(t *testing.T) {
	// --- Coordinator side ---
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("create coordinator bundle: %v", err)
	}

	srv, err := grpcserver.New(coordBundle)
	if err != nil {
		t.Fatalf("create grpc server: %v", err)
	}

	addr := "127.0.0.1:19090"
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()
	defer srv.Stop()

	time.Sleep(50 * time.Millisecond)

	// --- Node side ---
	nodeBundle, err := auth.NewNodeBundle(coordBundle.CA, "test-node-1")
	if err != nil {
		t.Fatalf("create node bundle: %v", err)
	}

	client, err := grpcclient.New(addr, "helion-coordinator", nodeBundle)
	if err != nil {
		t.Fatalf("dial coordinator: %v", err)
	}
	defer client.Close()

	// --- Handshake verified by a successful RPC ---
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.Register(ctx, "test-node-1", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register RPC failed (mTLS handshake or RPC error): %v", err)
	}

	if resp.NodeId != "test-node-1" {
		t.Errorf("expected node_id=test-node-1, got %s", resp.NodeId)
	}

	t.Logf("mTLS handshake succeeded — node_id=%s", resp.NodeId)
}

func TestMTLSRejectsUntrustedClient(t *testing.T) {
	// --- Coordinator with its own CA ---
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("create coordinator bundle: %v", err)
	}

	srv, err := grpcserver.New(coordBundle)
	if err != nil {
		t.Fatalf("create grpc server: %v", err)
	}

	addr := "127.0.0.1:19091"
	go func() { srv.Serve(addr) }()
	defer srv.Stop()

	time.Sleep(50 * time.Millisecond)

	// --- Rogue node with a *different* CA ---
	rogueCA, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("create rogue CA: %v", err)
	}
	rogueBundle, err := auth.NewNodeBundle(rogueCA, "rogue-node")
	if err != nil {
		t.Fatalf("create rogue bundle: %v", err)
	}

	client, err := grpcclient.New(addr, "helion-coordinator", rogueBundle)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.Register(ctx, "rogue-node", "127.0.0.1:9999")
	if err == nil {
		t.Fatal("expected mTLS rejection but RPC succeeded — untrusted client was accepted")
	}

	t.Logf("correctly rejected untrusted client: %v", err)
}
