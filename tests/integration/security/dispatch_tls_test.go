// tests/integration/security/dispatch_tls_test.go
//
// AUDIT 2026-04-12/T1: verifies that the dispatcher rejects a node presenting
// an unknown certificate (i.e. one not signed by the coordinator's CA).

package security

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// fakeNodeService is a minimal NodeService that accepts any Dispatch RPC.
type fakeNodeService struct {
	pb.UnimplementedNodeServiceServer
}

func (f *fakeNodeService) Dispatch(_ context.Context, _ *pb.DispatchRequest) (*pb.DispatchAck, error) {
	return &pb.DispatchAck{Accepted: true}, nil
}

// buildDispatchTLS builds a dispatch TLS config that mirrors the production
// configuration in cmd/helion-coordinator/main.go (AUDIT H1 fix):
// InsecureSkipVerify=true to skip hostname checks (each node has a unique CN),
// but VerifyConnection manually verifies the cert chain against the CA pool.
func buildDispatchTLS(bundle *auth.Bundle) (*tls.Config, error) {
	cfg, err := bundle.RawTLSConfig("helion-node")
	if err != nil {
		return nil, err
	}
	caPool := cfg.RootCAs
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("dispatch TLS: no peer certificates")
		}
		opts := x509.VerifyOptions{
			Roots:         caPool,
			Intermediates: x509.NewCertPool(),
		}
		for _, ic := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(ic)
		}
		if _, err := cs.PeerCertificates[0].Verify(opts); err != nil {
			return fmt.Errorf("dispatch TLS: cert not signed by coordinator CA: %w", err)
		}
		return nil
	}
	return cfg, nil
}

// TestDispatchTLS_VerifiesNodeCert verifies that the GRPCNodeDispatcher
// rejects a connection to a node whose TLS certificate was NOT issued by
// the coordinator's CA. This is the regression test for AUDIT H1.
func TestDispatchTLS_VerifiesNodeCert(t *testing.T) {
	// ── Set up coordinator (CA1) ─────────────────────────────────────────
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coordinator bundle: %v", err)
	}

	dispatchTLS, err := buildDispatchTLS(coordBundle)
	if err != nil {
		t.Fatalf("dispatch TLS config: %v", err)
	}
	dispatcher := cluster.NewGRPCNodeDispatcher(dispatchTLS)

	// ── Set up rogue node (CA2 — independent, unknown CA) ────────────────
	rogueBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("rogue bundle: %v", err)
	}

	rogueCert, err := tls.X509KeyPair(rogueBundle.CertPEM, rogueBundle.KeyPEM)
	if err != nil {
		t.Fatalf("rogue X509KeyPair: %v", err)
	}
	rogueTLS := &tls.Config{
		Certificates: []tls.Certificate{rogueCert},
	}

	lis, err := tls.Listen("tcp", "127.0.0.1:0", rogueTLS)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(rogueTLS)))
	pb.RegisterNodeServiceServer(gs, &fakeNodeService{})
	go gs.Serve(lis)
	defer gs.Stop()

	addr := lis.Addr().(*net.TCPAddr)

	// ── Dispatch to the rogue node — should fail TLS verification ────────
	job := &cpb.Job{
		ID:      "tls-test-job",
		Command: "echo",
		Args:    []string{"hello"},
	}

	err = dispatcher.DispatchToNode(context.Background(), addr.String(), job)
	if err == nil {
		t.Fatal("expected TLS verification error when dispatching to rogue node, got nil")
	}

	t.Logf("correctly rejected rogue node: %v", err)
}

// TestDispatchTLS_AcceptsValidNodeCert verifies that the dispatcher succeeds
// when connecting to a node whose certificate WAS issued by the coordinator's CA.
func TestDispatchTLS_AcceptsValidNodeCert(t *testing.T) {
	// ── Set up coordinator ───────────────────────────────────────────────
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coordinator bundle: %v", err)
	}

	// Issue a node cert from the same CA.
	nodeBundle, err := auth.NewNodeBundle(coordBundle.CA, "test-node")
	if err != nil {
		t.Fatalf("node bundle: %v", err)
	}

	dispatchTLS, err := buildDispatchTLS(coordBundle)
	if err != nil {
		t.Fatalf("dispatch TLS config: %v", err)
	}
	dispatcher := cluster.NewGRPCNodeDispatcher(dispatchTLS)

	// ── Start a gRPC node server with the valid cert ─────────────────────
	nodeCert, err := tls.X509KeyPair(nodeBundle.CertPEM, nodeBundle.KeyPEM)
	if err != nil {
		t.Fatalf("node X509KeyPair: %v", err)
	}
	nodeTLS := &tls.Config{
		Certificates: []tls.Certificate{nodeCert},
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(nodeTLS)))
	pb.RegisterNodeServiceServer(gs, &fakeNodeService{})
	go gs.Serve(lis)
	defer gs.Stop()

	addr := lis.Addr().(*net.TCPAddr)

	// ── Dispatch — should succeed ────────────────────────────────────────
	job := &cpb.Job{
		ID:      "tls-valid-job",
		Command: "echo",
		Args:    []string{"hello"},
	}

	err = dispatcher.DispatchToNode(context.Background(), addr.String(), job)
	if err != nil {
		t.Fatalf("expected dispatch to valid node to succeed, got: %v", err)
	}
}
