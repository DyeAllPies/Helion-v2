// internal/grpcserver/report_result_test.go
//
// Tests for the ReportResult RPC — success/failure transitions, missing jobs,
// security-violation audit logging, and internal transition failures.

package grpcserver_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	js := newMockJobStore()

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

// TestReportResult_DispatchingJob_TransitionsToRunningThenCompleted covers
// the DISPATCHING → RUNNING → COMPLETED path (result reported before the
// scheduler has transitioned the job to RUNNING).
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

// TestReportResult_AttestsOutputs_DropsForgedEntries is the full-RPC
// counterpart to the attestOutputs unit tests in security_test.go.
// attestOutputs itself is well-tested in isolation (shape + scheme +
// prefix checks), but its call site — the ReportResult handler —
// wires it in via one line:
//
//	opts.ResolvedOutputs = s.attestOutputs(...)
//
// A regression that removed or bypassed that line (e.g. "assigns
// result.Outputs directly for performance") would slip past every
// unit test because the validator itself would still work. This
// test catches that class of regression by submitting a
// ReportResult with one legitimate output + one forged-prefix
// output, then asserting that the persisted Job's ResolvedOutputs
// contains **only the legitimate entry**. If the handler ever stops
// passing the slice through attestation, the bogus URI will reach
// the JobStore and this test fails.
func TestReportResult_AttestsOutputs_DropsForgedEntries(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	// Running → Completed path; job pinned to a specific node.
	js.jobs["legit-job"] = &cpb.Job{
		ID:     "legit-job",
		Status: cpb.JobStatusRunning,
		NodeID: "worker-node",
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

	// Craft two output entries:
	//  - LEGIT: well-formed, s3 URI under jobs/legit-job/out.bin —
	//           matches the job_id-prefix rule.
	//  - FORGED: well-formed scheme but points at a URI that names
	//            a DIFFERENT job's prefix. attestOutputs must drop
	//            this, or a compromised node could inject foreign
	//            artifact pointers into another job's record.
	outputs := []*pb.ArtifactOutput{
		{
			Name:      "LEGIT",
			Uri:       "s3://helion/jobs/legit-job/out.bin",
			LocalPath: "out.bin",
			Size:      42,
			Sha256:    "deadbeef",
		},
		{
			Name:      "FORGED",
			Uri:       "s3://helion/jobs/other-job/hijacked.bin",
			LocalPath: "hijacked.bin",
			Size:      99,
			Sha256:    "feedface",
		},
	}

	if err := client.ReportResult(ctx, &pb.JobResult{
		JobId:   "legit-job",
		NodeId:  "worker-node",
		Success: true,
		Outputs: outputs,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	// Observation point: the mockJobStore captured the last
	// Transition call's options, including ResolvedOutputs.
	// Exactly one entry must remain — the one whose URI matches
	// the job's own prefix.
	got := js.lastTransitionOpts.ResolvedOutputs
	if len(got) != 1 {
		t.Fatalf("want 1 attested output after drop, got %d: %+v", len(got), got)
	}
	if got[0].Name != "LEGIT" {
		t.Errorf("attested output name: got %q, want %q", got[0].Name, "LEGIT")
	}
	if got[0].URI != "s3://helion/jobs/legit-job/out.bin" {
		t.Errorf("attested URI mutated: %q", got[0].URI)
	}

	// Job record reflects only the clean output (confirms the
	// mock mirrored persistence correctly, so the test isn't
	// passing just because nothing was recorded).
	persisted, _ := js.Get("legit-job")
	if len(persisted.ResolvedOutputs) != 1 {
		t.Errorf("persisted Job.ResolvedOutputs: got %d, want 1", len(persisted.ResolvedOutputs))
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
