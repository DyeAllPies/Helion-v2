package grpcserver_test

import (
	"context"
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

// TestReportResult_NodeIDMismatch_Rejects covers the ML-threat-model
// defence against a compromised node reporting a result for a job
// that was dispatched to a different node. The stored Job.NodeID is
// pinned at dispatch time; any mismatched ReportResult is rejected
// with PermissionDenied and logged as a security violation.
func TestReportResult_NodeIDMismatch_Rejects(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	// Job was dispatched to node-A.
	js.jobs["job-pinned"] = &cpb.Job{
		ID:     "job-pinned",
		NodeID: "node-A",
		Status: cpb.JobStatusRunning,
	}

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()
	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	// A different node (node-B) attempts to report the job as completed.
	nb, _ := auth.NewNodeBundle(coordBundle.CA, "node-B")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-pinned",
		NodeId:  "node-B",
		Success: true,
	})
	if err == nil {
		t.Fatal("expected error on mismatched node_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	// The legitimate job record must not have been mutated.
	if js.jobs["job-pinned"].Status == cpb.JobStatusCompleted {
		t.Fatal("rejected ReportResult should not have transitioned the job")
	}
}

// TestReportResult_NodeIDMatches_Accepts guards the happy path: same
// node that was dispatched to reports results, transition succeeds.
func TestReportResult_NodeIDMatches_Accepts(t *testing.T) {
	coordBundle, _ := auth.NewCoordinatorBundle()
	js := newMockJobStore()
	js.jobs["job-ok"] = &cpb.Job{
		ID:     "job-ok",
		NodeID: "node-A",
		Status: cpb.JobStatusRunning,
	}

	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(js))
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()
	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "node-A")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err = client.ReportResult(ctx, &pb.JobResult{
		JobId:   "job-ok",
		NodeId:  "node-A",
		Success: true,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if js.jobs["job-ok"].Status != cpb.JobStatusCompleted {
		t.Fatalf("expected Completed, got %v", js.jobs["job-ok"].Status)
	}
}
