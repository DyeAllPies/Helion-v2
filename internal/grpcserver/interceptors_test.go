// internal/grpcserver/interceptors_test.go
//
// Tests for the revocation and rate-limit gRPC interceptors.

package grpcserver_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── Revocation interceptor ────────────────────────────────────────────────────

func TestRevocationInterceptor_RevokedNode_ReturnsUnauthenticated(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	rev := &mockRevocationChecker{revoked: map[string]bool{"revoked-node": true}}
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRevocationChecker(rev))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "revoked-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.Register(ctx, "revoked-node", "127.0.0.1:8080")
	if err == nil {
		t.Fatal("expected Unauthenticated error for revoked node, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %T", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("want Unauthenticated, got %v", st.Code())
	}
}

func TestRevocationInterceptor_AllowedNode_Passes(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	rev := &mockRevocationChecker{revoked: map[string]bool{}}
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRevocationChecker(rev))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "allowed-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.Register(ctx, "allowed-node", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register should succeed for non-revoked node: %v", err)
	}
	if resp.NodeId != "allowed-node" {
		t.Errorf("want allowed-node, got %q", resp.NodeId)
	}
}

// TestRevocationInterceptor_ReportResult_RevokedNode_ReturnsUnauthenticated
// exercises the extractNodeID branch for *pb.JobResult with a revoked node.
func TestRevocationInterceptor_ReportResult_RevokedNode_ReturnsUnauthenticated(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	js := newMockJobStore()
	rev := &mockRevocationChecker{revoked: map[string]bool{"revoked-reporter": true}}

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithRevocationChecker(rev),
		grpcserver.WithJobStore(js),
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

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "revoked-reporter")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-1",
		NodeId:  "revoked-reporter",
		Success: true,
	})
	if err == nil {
		t.Fatal("expected Unauthenticated for revoked node, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Errorf("want Unauthenticated, got %v", st.Code())
	}
}

// ── Rate-limit interceptor ────────────────────────────────────────────────────

func TestRateLimitInterceptor_BlockedNode_ReturnsResourceExhausted(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	rl := &mockRateLimiter{
		rate:    5,
		blocked: map[string]bool{"flood-node": true},
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

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "flood-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.Register(ctx, "flood-node", "127.0.0.1:8080")
	if err == nil {
		t.Fatal("expected ResourceExhausted for rate-limited node, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %T", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("want ResourceExhausted, got %v", st.Code())
	}
	if al.rateLimitHits == 0 {
		t.Error("expected at least one rate_limit_hit audit event")
	}
}

// TestRateLimitInterceptor_EmptyNodeID_PassesThrough verifies that an empty
// node ID (extractNodeID returns "") bypasses the rate limiter entirely.
func TestRateLimitInterceptor_EmptyNodeID_PassesThrough(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	// Block everything — empty NodeId should still bypass.
	rl := &mockRateLimiter{
		rate:    1,
		blocked: map[string]bool{"": true},
	}

	srv, err := grpcserver.New(coordBundle, grpcserver.WithRateLimiter(rl))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "empty-id-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.Register(ctx, "", "127.0.0.1:8080")
	if err != nil {
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.ResourceExhausted {
			t.Error("empty NodeId should bypass rate limiter, got ResourceExhausted")
		}
	}
}

// TestRateLimitInterceptor_ReportResult_ExtractsNodeID covers the
// *pb.JobResult case in extractNodeID by calling ReportResult through
// a server with a rate limiter that allows all nodes.
func TestRateLimitInterceptor_ReportResult_ExtractsNodeID(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	rl := &mockRateLimiter{rate: 100}
	js := newMockJobStore()

	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithRateLimiter(rl),
		grpcserver.WithJobStore(js),
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

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "rl-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Job may not be found (no job in store), but we just need extractNodeID called.
	_ = client.ReportResult(ctx, &pb.JobResult{
		JobId:  "no-such-job",
		NodeId: "rl-node",
	})
}
