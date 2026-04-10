package grpcserver_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"log/slog"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── mock implementations ──────────────────────────────────────────────────────

type mockRevocationChecker struct {
	revoked map[string]bool
}

func (m *mockRevocationChecker) IsRevoked(nodeID string) bool {
	return m.revoked[nodeID]
}

type mockRateLimiter struct {
	rate     float64
	blocked  map[string]bool
	hitCount map[string]int
}

func (m *mockRateLimiter) Allow(_ context.Context, nodeID string) error {
	if m.hitCount == nil {
		m.hitCount = make(map[string]int)
	}
	m.hitCount[nodeID]++
	if m.blocked[nodeID] {
		return status.Errorf(codes.ResourceExhausted, "rate limit exceeded for %s (%.1f rps)", nodeID, m.rate)
	}
	return nil
}

func (m *mockRateLimiter) GetRate() float64 { return m.rate }

type mockAuditLogger struct {
	rateLimitHits      int
	securityViolations int
}

func (m *mockAuditLogger) LogJobSubmit(_ context.Context, _, _, _ string) error { return nil }
func (m *mockAuditLogger) LogRateLimitHit(_ context.Context, _ string, _ float64) error {
	m.rateLimitHits++
	return nil
}
func (m *mockAuditLogger) LogSecurityViolation(_ context.Context, _, _, _ string) error {
	m.securityViolations++
	return nil
}

type mockJobStore struct {
	jobs     map[string]*cpb.Job
	submitErr error
	getErr    error
	transErr  error
}

func newMockJobStore() *mockJobStore {
	return &mockJobStore{jobs: make(map[string]*cpb.Job)}
}

func (m *mockJobStore) Submit(_ context.Context, j *cpb.Job) error {
	if m.submitErr != nil {
		return m.submitErr
	}
	m.jobs[j.ID] = j
	return nil
}

func (m *mockJobStore) Get(jobID string) (*cpb.Job, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, errors.New("not found")
	}
	return j, nil
}

func (m *mockJobStore) Transition(_ context.Context, jobID string, to cpb.JobStatus, _ cluster.TransitionOptions) error {
	if m.transErr != nil {
		return m.transErr
	}
	if j, ok := m.jobs[jobID]; ok {
		j.Status = to
	}
	return nil
}

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

	// Node is NOT in the revoked set.
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

// ── ReportResult ──────────────────────────────────────────────────────────────

func TestReportResult_NoJobStore_ReturnsAck(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	srv, _ := grpcserver.New(coordBundle)

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "result-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-1",
		NodeId:  "result-node",
		Success: true,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
}

func TestReportResult_JobNotFound_ReturnsNotFound(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore() // empty — job not found

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "result-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "nonexistent-job",
		NodeId:  "result-node",
		Success: true,
	})
	if err == nil {
		t.Fatal("expected NotFound error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Errorf("want NotFound, got %v", st.Code())
	}
}

func TestReportResult_SuccessfulJob_TransitionsToCompleted(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	js.jobs["job-ok"] = &cpb.Job{ID: "job-ok", Status: cpb.JobStatusRunning}

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "worker-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-ok",
		NodeId:  "worker-node",
		Success: true,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if js.jobs["job-ok"].Status != cpb.JobStatusCompleted {
		t.Errorf("expected COMPLETED, got %v", js.jobs["job-ok"].Status)
	}
}

func TestReportResult_FailedJob_TransitionsToFailed(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	js.jobs["job-fail"] = &cpb.Job{ID: "job-fail", Status: cpb.JobStatusRunning}

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "worker-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-fail",
		NodeId:  "worker-node",
		Success: false,
		Error:   "process crashed",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if js.jobs["job-fail"].Status != cpb.JobStatusFailed {
		t.Errorf("expected FAILED, got %v", js.jobs["job-fail"].Status)
	}
}

func TestReportResult_SecurityViolation_AuditLogged(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	js.jobs["job-seccomp"] = &cpb.Job{ID: "job-seccomp", Status: cpb.JobStatusRunning}
	al := &mockAuditLogger{}

	srv, _ := grpcserver.New(coordBundle,
		grpcserver.WithJobStore(js),
		grpcserver.WithAuditLogger(al),
	)

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "seccomp-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-seccomp",
		NodeId:  "seccomp-node",
		Success: false,
		Error:   "Seccomp",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if al.securityViolations == 0 {
		t.Error("expected security violation to be audit logged")
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

// ── extractNodeID default branch via ReportResult + revocation ────────────────

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

	// SendHeartbeats should return an error because the server rate-limits us.
	err = client.SendHeartbeats(ctx, "rl-hb-node", 20*time.Millisecond,
		func() int32 { return 0 }, nil)
	// The stream terminates with ResourceExhausted; SendHeartbeats returns non-nil.
	if err == nil {
		t.Error("expected rate-limit error from heartbeat stream, got nil")
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

// ── rateLimitInterceptor: empty nodeID passes through ────────────────────────

func TestRateLimitInterceptor_EmptyNodeID_PassesThrough(t *testing.T) {
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("NewCoordinatorBundle: %v", err)
	}

	// Use a rate limiter that blocks everything — but empty NodeId bypasses it.
	rl := &mockRateLimiter{
		rate:    1,
		blocked: map[string]bool{"": true}, // block empty string too, shouldn't matter
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

	// Register with empty NodeId — extractNodeID returns "" → rateLimiter bypass.
	_, err = client.Register(ctx, "", "127.0.0.1:8080")
	// The call may succeed or fail (empty NodeId in registry), but it must NOT be
	// rejected with ResourceExhausted.
	if err != nil {
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.ResourceExhausted {
			t.Error("empty NodeId should bypass rate limiter, got ResourceExhausted")
		}
	}
}

// ── ReportResult: DISPATCHING → RUNNING transition path ─────────────────────

func TestReportResult_DispatchingJob_TransitionsToRunningThenCompleted(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	js.jobs["job-dispatch"] = &cpb.Job{ID: "job-dispatch", Status: cpb.JobStatusDispatching}

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "worker-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Reporting result from DISPATCHING state should transition through RUNNING → COMPLETED.
	if err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-dispatch",
		NodeId:  "worker-node",
		Success: true,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if js.jobs["job-dispatch"].Status != cpb.JobStatusCompleted {
		t.Errorf("expected COMPLETED, got %v", js.jobs["job-dispatch"].Status)
	}
}

func TestReportResult_FinalTransitionFails_ReturnsInternal(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := &mockJobStore{
		jobs:     map[string]*cpb.Job{"job-x": {ID: "job-x", Status: cpb.JobStatusRunning}},
		transErr: errors.New("simulated store failure"),
	}

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "worker-node")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-x",
		NodeId:  "worker-node",
		Success: true,
	})
	if err == nil {
		t.Fatal("expected Internal error from failing transition, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("want Internal, got %v", st.Code())
	}
}

// ── extractNodeID *pb.JobResult branch ───────────────────────────────────────

// TestRateLimitInterceptor_ReportResult_ExtractsNodeID covers the
// *pb.JobResult case in extractNodeID by calling ReportResult through
// a server with a rate limiter that allows all nodes.
func TestRateLimitInterceptor_ReportResult_ExtractsNodeID(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	rl := &mockRateLimiter{rate: 100} // allows all
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

	// ReportResult with rate limiter in place — exercises JobResult branch in extractNodeID.
	// Job may not be found (no job in store) but that's fine — we just need extractNodeID called.
	_ = client.ReportResult(ctx, &pb.JobResult{
		JobId:  "no-such-job",
		NodeId: "rl-node",
	})
}
