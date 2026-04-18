// internal/grpcserver/report_service_event_test.go
//
// Tests for feature-17 ReportServiceEvent: readiness upsert, cross-node
// poison rejection, and the registry-not-wired soft-accept path.

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
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newServiceEventHarness spins up a gRPC server with a fresh
// coordinator bundle + node bundle, returning the addr, the registry
// it populates, the audit mock, and a stop func.
func newServiceEventHarness(t *testing.T, js *mockJobStore, nodeID string) (string, *auth.Bundle, *cluster.ServiceRegistry, *mockAuditLogger, func()) {
	t.Helper()
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coord bundle: %v", err)
	}
	sr := cluster.NewServiceRegistry()
	al := &mockAuditLogger{}
	srv, err := grpcserver.New(coordBundle,
		grpcserver.WithJobStore(js),
		grpcserver.WithServiceRegistry(sr),
		grpcserver.WithAuditLogger(al),
	)
	if err != nil {
		t.Fatalf("grpc server: %v", err)
	}

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	time.Sleep(40 * time.Millisecond)

	nodeBundle, err := auth.NewNodeBundle(coordBundle.CA, nodeID)
	if err != nil {
		t.Fatalf("node bundle: %v", err)
	}
	return addr, nodeBundle, sr, al, srv.Stop
}

func TestReportServiceEvent_UpsertsRegistry(t *testing.T) {
	js := newMockJobStore()
	js.jobs["svc-1"] = &cpb.Job{ID: "svc-1", NodeID: "node-a"}

	addr, nb, sr, al, stop := newServiceEventHarness(t, js, "node-a")
	t.Cleanup(stop)

	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.ReportServiceEvent(ctx, &pb.ServiceEvent{
		JobId:       "svc-1",
		NodeId:      "node-a",
		NodeAddress: "10.0.0.4:9090",
		Port:        8080,
		HealthPath:  "/healthz",
		Ready:       true,
	}); err != nil {
		t.Fatalf("ReportServiceEvent: %v", err)
	}

	ep, ok := sr.Get("svc-1")
	if !ok {
		t.Fatal("expected service entry, none found")
	}
	if !ep.Ready || ep.Port != 8080 {
		t.Fatalf("unexpected entry: %+v", ep)
	}
	// LogServiceEvent must fire on every accepted event — it's the
	// compliance trail for readiness transitions. A regression that
	// dropped the `if s.audit != nil { s.audit.LogServiceEvent(...) }`
	// block at handlers.go:545-551 would break the audit trail
	// silently; the registry upsert above would still succeed.
	if al.serviceEvents != 1 {
		t.Errorf("audit LogServiceEvent: got %d calls, want 1", al.serviceEvents)
	}
}

func TestReportServiceEvent_CrossNodeMismatch_Rejected(t *testing.T) {
	js := newMockJobStore()
	// Job pinned to node-a, but we'll report from node-b.
	js.jobs["svc-1"] = &cpb.Job{ID: "svc-1", NodeID: "node-a"}

	addr, nb, sr, al, stop := newServiceEventHarness(t, js, "node-b")
	t.Cleanup(stop)

	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.ReportServiceEvent(ctx, &pb.ServiceEvent{
		JobId:  "svc-1",
		NodeId: "node-b",
		Port:   8080,
		Ready:  true,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", st.Code())
	}
	if _, ok := sr.Get("svc-1"); ok {
		t.Fatal("registry should not be populated after rejected event")
	}
	if al.securityViolations == 0 {
		t.Fatal("expected security-violation audit entry")
	}
}

// TestReportServiceEvent_EmptyJobId_Rejected pins the InvalidArgument
// branch at handlers.go:491. Without this check a malformed event
// (empty JobId) would proceed through the handler: cross-node check
// skipped (Get("") errors, condition short-circuits), registry's own
// guard silently drops the upsert, but LogServiceEvent would fire
// with an empty job_id field — audit-log noise with no corresponding
// registry entry. The InvalidArgument response is the handler's
// first-line defense and the only branch that rejects the event
// cleanly before any side effects.
func TestReportServiceEvent_EmptyJobId_Rejected(t *testing.T) {
	js := newMockJobStore()
	addr, nb, _, _, stop := newServiceEventHarness(t, js, "node-a")
	t.Cleanup(stop)

	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = client.ReportServiceEvent(ctx, &pb.ServiceEvent{
		JobId:  "", // the whole point
		NodeId: "node-a",
		Port:   8080,
		Ready:  true,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", st.Code())
	}
}

func TestReportServiceEvent_NilRegistry_SoftAccepts(t *testing.T) {
	// No WithServiceRegistry — handler should accept the RPC so the
	// node prober isn't stuck retrying, but drop the payload.
	coordBundle, _ := auth.NewCoordinatorBundle()
	srv, _ := grpcserver.New(coordBundle, grpcserver.WithJobStore(newMockJobStore()))

	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = srv.Serve(addr) }()
	t.Cleanup(srv.Stop)
	time.Sleep(40 * time.Millisecond)

	nb, _ := auth.NewNodeBundle(coordBundle.CA, "node-a")
	client, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.ReportServiceEvent(ctx, &pb.ServiceEvent{
		JobId:  "svc-1",
		NodeId: "node-a",
		Port:   8080,
		Ready:  true,
	}); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}
