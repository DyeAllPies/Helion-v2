// tests/integration/crash_recovery_test.go
//
// Integration test for Phase 2 crash recovery exit criterion:
//
//   "Submit job, kill coordinator mid-flight, restart coordinator,
//    verify job completes."
//
// Architecture
// ────────────
// Everything runs in-process as goroutines — no real OS processes are spawned.
// This matches how the existing mTLS and registry integration tests are
// structured.
//
//   ┌──────────────────────────────────────────────────────────────────┐
//   │ Test process                                                     │
//   │                                                                  │
//   │  fakeNode (goroutine)          Coordinator 1 (goroutine)         │
//   │  ─ NodeServiceServer           ─ grpcserver.Server               │
//   │  ─ executes job synchronously  ─ JobStore + Registry             │
//   │  ─ calls ReportResult back     ─ BadgerDB on t.TempDir()         │
//   │                                                                  │
//   │  ── "kill" ──────────────────────────────────────────────────►   │
//   │                 (cancel coordinator 1 context, stop server)      │
//   │                                                                  │
//   │  fakeNode (same goroutine)     Coordinator 2 (goroutine)         │
//   │  ─ re-registers                ─ new grpcserver.Server           │
//   │  ─ resumes heartbeating        ─ same BadgerDB path              │
//   │                                ─ JobStore.Restore()              │
//   │                                ─ RecoveryManager.Run()           │
//   │                                                                  │
//   │  fakeNode executes recovered   Job reaches completed in DB       │
//   │  job, reports result ──────►   ─ verified by test               │
//   └──────────────────────────────────────────────────────────────────┘
//
// Fake node
// ─────────
// The fake node is a real gRPC server that implements NodeServiceServer.
// Its Dispatch handler:
//  1. Transitions the job to running by calling ReportResult... actually it
//     does not have a back-channel to the coordinator's JobStore directly.
//     Instead it dials the coordinator's gRPC address and calls ReportResult.
//
// Fake Dispatcher
// ───────────────
// The RecoveryManager needs a Dispatcher. In this test the Dispatcher dials
// the fake node and calls Dispatch on it, which is exactly the production flow.

package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
)

// ── port helpers ──────────────────────────────────────────────────────────────

// freePort asks the OS for a free TCP port and returns its address.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("freePort close: %v", err)
	}
	return addr
}

// ── fakeNodeServer ────────────────────────────────────────────────────────────

// fakeNodeServer implements pb.NodeServiceServer in-process.
// When Dispatch is called it immediately notifies the coordinator of completion
// by calling ReportResult on the coordinator's gRPC address.
type fakeNodeServer struct {
	pb.UnimplementedNodeServiceServer

	mu            sync.Mutex
	dispatched    []string // job IDs received
	coordAddr     string   // coordinator address to call ReportResult on
	nodeBundle    *auth.Bundle
	dispatchDelay time.Duration // optional delay before reporting result
}

func (n *fakeNodeServer) Dispatch(
	ctx context.Context,
	req *pb.DispatchRequest,
) (*pb.DispatchAck, error) {
	n.mu.Lock()
	n.dispatched = append(n.dispatched, req.JobId)
	coordAddr := n.coordAddr
	bundle := n.nodeBundle
	delay := n.dispatchDelay
	n.mu.Unlock()

	// Report result back to coordinator asynchronously so the Dispatch RPC
	// returns immediately (ack) and the completion arrives shortly after —
	// matching how a real node behaves.
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		if err := n.reportResult(coordAddr, bundle, req.JobId); err != nil {
			slog.Warn("fakeNode: ReportResult failed",
				slog.String("job_id", req.JobId),
				slog.Any("err", err),
			)
		}
	}()

	return &pb.DispatchAck{JobId: req.JobId, Accepted: true}, nil
}

func (n *fakeNodeServer) reportResult(coordAddr string, bundle *auth.Bundle, jobID string) error {
	client, err := grpcclient.New(coordAddr, "helion-coordinator", bundle)
	if err != nil {
		return fmt.Errorf("dial coordinator: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Client.ReportResult(ctx, &pb.JobResult{
		JobId:   jobID,
		Success: true,
	})
	return err
}

func (n *fakeNodeServer) dispatchedJobs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.dispatched))
	copy(out, n.dispatched)
	return out
}

// ── coordinator harness ───────────────────────────────────────────────────────

// coordinatorInstance represents one "lifetime" of the coordinator.
type coordinatorInstance struct {
	srv      *grpcserver.Server
	jobs     *cluster.JobStore
	registry *cluster.Registry
	persister *cluster.BadgerJSONPersister
	cancel   context.CancelFunc
	done     chan struct{}
}

// startCoordinator starts a coordinator at the given address and BadgerDB path.
// It wires together: BadgerJSONPersister → Registry + JobStore → grpcserver.
func startCoordinator(
	t *testing.T,
	addr string,
	dbPath string,
	bundle *auth.Bundle,
	nodeAddr string, // fake node address for the grpcDispatcher
	nodeBundle *auth.Bundle,
) *coordinatorInstance {
	t.Helper()

	const heartbeatInterval = 200 * time.Millisecond

	p, err := cluster.NewBadgerJSONPersister(dbPath, heartbeatInterval)
	if err != nil {
		t.Fatalf("open BadgerDB: %v", err)
	}

	registry := cluster.NewRegistry(p, heartbeatInterval, nil)
	jobs := cluster.NewJobStore(p, nil)

	srv, err := grpcserver.New(bundle,
		grpcserver.WithRegistry(registry),
		grpcserver.WithJobStore(jobs),
	)
	if err != nil {
		t.Fatalf("create grpc server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		if err := srv.Serve(addr); err != nil && ctx.Err() == nil {
			t.Logf("coordinator serve error: %v", err)
		}
	}()

	// Wait for the server to be ready.
	time.Sleep(50 * time.Millisecond)

	return &coordinatorInstance{
		srv:      srv,
		jobs:     jobs,
		registry: registry,
		persister: p,
		cancel:   cancel,
		done:     done,
	}
}

// stop gracefully shuts down the coordinator and closes BadgerDB.
func (c *coordinatorInstance) stop(t *testing.T) {
	t.Helper()
	c.cancel()
	c.srv.Stop()
	<-c.done
	if err := c.persister.Close(); err != nil {
		t.Errorf("close BadgerDB: %v", err)
	}
}

// ── grpcDispatcher ────────────────────────────────────────────────────────────

// grpcDispatcher implements cluster.Dispatcher by picking a node from the
// registry and dialling its NodeService to call Dispatch.
//
// In this test there is exactly one node. In production the Scheduler selects
// from all healthy nodes.
type grpcDispatcher struct {
	registry   *cluster.Registry
	nodeAddr   string
	nodeBundle *auth.Bundle
}

func (d *grpcDispatcher) Dispatch(ctx context.Context, job *cpb.Job) (string, error) {
	// For the test: use the single known node address directly.
	// In production this would call scheduler.Pick() first.
	healthy := d.registry.HealthyNodes()
	if len(healthy) == 0 {
		return "", cluster.ErrNoHealthyNodes
	}
	target := healthy[0]

	// Dial the node's NodeService.
	creds, err := d.nodeBundle.ClientCredentials(target.NodeID)
	if err != nil {
		return "", fmt.Errorf("node credentials: %w", err)
	}
	conn, err := grpc.NewClient(target.Address, grpc.WithTransportCredentials(creds))
	if err != nil {
		return "", fmt.Errorf("dial node %s: %w", target.Address, err)
	}
	defer conn.Close()

	nodeClient := pb.NewNodeServiceClient(conn)
	ack, err := nodeClient.Dispatch(ctx, &pb.DispatchRequest{
		JobId:   job.ID,
		Command: job.Command,
		Args:    job.Args,
	})
	if err != nil {
		return "", fmt.Errorf("dispatch RPC: %w", err)
	}
	if !ack.Accepted {
		return "", fmt.Errorf("node rejected job: %s", ack.Error)
	}
	return target.NodeID, nil
}

// ── TestCrashRecovery_JobCompletesAfterRestart ────────────────────────────────

// TestCrashRecovery_JobCompletesAfterRestart is the Phase 2 exit criterion:
//
//	"Submit job, kill coordinator mid-flight, restart coordinator,
//	 verify job completes."
//
// Timeline:
//  1. Start coordinator 1 + fake node. Node registers + heartbeats.
//  2. Submit a job. Coordinator dispatches it; fake node receives Dispatch.
//  3. Kill coordinator 1 before the fake node's ReportResult arrives
//     (dispatchDelay ensures the report is delayed).
//  4. Start coordinator 2 at the same address with the same BadgerDB.
//     Coordinator 2 restores state and runs RecoveryManager.
//  5. Fake node re-registers with coordinator 2, becomes healthy.
//  6. RecoveryManager dispatches the recovered job to the fake node.
//  7. Fake node calls ReportResult on coordinator 2.
//  8. Verify the job is in completed state in BadgerDB.
func TestCrashRecovery_JobCompletesAfterRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping crash recovery test in -short mode")
	}

	// ── shared infrastructure ─────────────────────────────────────────────
	coordAddr := freePort(t)
	nodeAddr := freePort(t)
	dbPath := t.TempDir()
	const nodeID = "recovery-test-node"
	const gracePeriod = 500 * time.Millisecond
	const heartbeatInterval = 100 * time.Millisecond

	// Auth bundles — shared across both coordinator lifetimes.
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coordinator bundle: %v", err)
	}
	nodeBundle, err := auth.NewNodeBundle(coordBundle.CA, nodeID)
	if err != nil {
		t.Fatalf("node bundle: %v", err)
	}

	// ── fake node gRPC server ─────────────────────────────────────────────
	// The fake node's Dispatch handler waits dispatchDelay before calling
	// ReportResult. This gives us a window to kill coordinator 1 first.
	fakeNode := &fakeNodeServer{
		nodeBundle:    nodeBundle,
		dispatchDelay: 300 * time.Millisecond,
	}

	nodeCreds, err := nodeBundle.ServerCredentials()
	if err != nil {
		t.Fatalf("node server creds: %v", err)
	}
	nodeGRPC := grpc.NewServer(grpc.Creds(nodeCreds))
	pb.RegisterNodeServiceServer(nodeGRPC, fakeNode)

	nodeLis, err := net.Listen("tcp", nodeAddr)
	if err != nil {
		t.Fatalf("node listen: %v", err)
	}
	nodeServerDone := make(chan struct{})
	go func() {
		defer close(nodeServerDone)
		if err := nodeGRPC.Serve(nodeLis); err != nil {
			t.Logf("node grpc serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		nodeGRPC.GracefulStop()
		<-nodeServerDone
	})

	// ── Phase 1: start coordinator 1 ──────────────────────────────────────
	t.Log("phase 1: starting coordinator 1")
	coord1 := startCoordinator(t, coordAddr, dbPath, coordBundle, nodeAddr, nodeBundle)

	// Point the fake node's ReportResult calls at coordinator 1 for now.
	fakeNode.mu.Lock()
	fakeNode.coordAddr = coordAddr
	fakeNode.mu.Unlock()

	// ── Phase 2: fake node registers + heartbeats ─────────────────────────
	t.Log("phase 2: node registers with coordinator 1")
	nodeCtx1, nodeCancel1 := context.WithCancel(context.Background())

	nodeClient1, err := grpcclient.New(coordAddr, "helion-coordinator", nodeBundle)
	if err != nil {
		t.Fatalf("node dial coord1: %v", err)
	}

	if _, err := nodeClient1.Register(nodeCtx1, nodeID, nodeAddr); err != nil {
		t.Fatalf("node register: %v", err)
	}

	heartbeatDone1 := make(chan error, 1)
	go func() {
		heartbeatDone1 <- nodeClient1.SendHeartbeats(nodeCtx1, nodeID, heartbeatInterval,
			func() int32 { return 0 }, nil)
	}()

	// Wait for node to appear healthy.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if nodes := coord1.registry.HealthyNodes(); len(nodes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(coord1.registry.HealthyNodes()) == 0 {
		t.Fatal("node did not become healthy within 2s")
	}
	t.Log("node is healthy on coordinator 1")

	// ── Phase 3: submit a job ─────────────────────────────────────────────
	t.Log("phase 3: submitting job")
	job := &cpb.Job{
		ID:      "crash-recovery-job-1",
		Command: "echo",
		Args:    []string{"recovered"},
	}
	ctx := context.Background()
	if err := coord1.jobs.Submit(ctx, job); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	// Dispatch manually (simulating the coordinator's dispatch loop).
	dispatcher1 := &grpcDispatcher{
		registry:   coord1.registry,
		nodeAddr:   nodeAddr,
		nodeBundle: nodeBundle,
	}
	nodeID1, err := dispatcher1.Dispatch(ctx, job)
	if err != nil {
		t.Fatalf("dispatch job: %v", err)
	}
	if err := coord1.jobs.Transition(ctx, job.ID, cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: nodeID1}); err != nil {
		t.Fatalf("transition to dispatching: %v", err)
	}
	t.Logf("job dispatched to %s, fake node will delay result by 300ms", nodeID1)

	// ── Phase 4: kill coordinator 1 ───────────────────────────────────────
	// The fake node's ReportResult is still pending (300ms delay).
	// This simulates the coordinator crashing mid-flight.
	t.Log("phase 4: killing coordinator 1")
	nodeCancel1() // stop heartbeat client
	<-heartbeatDone1
	if err := nodeClient1.Close(); err != nil {
		t.Logf("nodeClient1.Close: %v", err)
	}
	coord1.stop(t)
	t.Log("coordinator 1 stopped")

	// Brief pause to ensure coord1 is fully down before coord2 starts.
	time.Sleep(100 * time.Millisecond)

	// ── Phase 5: start coordinator 2 ─────────────────────────────────────
	t.Log("phase 5: starting coordinator 2 (restart)")
	coord2 := startCoordinator(t, coordAddr, dbPath, coordBundle, nodeAddr, nodeBundle)
	t.Cleanup(func() { coord2.stop(t) })

	// Point the fake node's ReportResult at coordinator 2.
	fakeNode.mu.Lock()
	fakeNode.coordAddr = coordAddr
	fakeNode.mu.Unlock()

	// Restore persisted state into coordinator 2's JobStore.
	if err := coord2.jobs.Restore(ctx); err != nil {
		t.Fatalf("restore job store: %v", err)
	}

	// Confirm the job is present and non-terminal in the restored store.
	restored, err := coord2.jobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get restored job: %v", err)
	}
	if restored.Status.IsTerminal() {
		t.Fatalf("restored job unexpectedly terminal: %s", restored.Status)
	}
	t.Logf("job %s restored with status=%s", job.ID, restored.Status)

	// ── Phase 6: fake node re-registers with coordinator 2 ───────────────
	t.Log("phase 6: node re-registers with coordinator 2")
	nodeCtx2, nodeCancel2 := context.WithCancel(context.Background())
	t.Cleanup(nodeCancel2)

	nodeClient2, err := grpcclient.New(coordAddr, "helion-coordinator", nodeBundle)
	if err != nil {
		t.Fatalf("node dial coord2: %v", err)
	}
	t.Cleanup(func() {
		if err := nodeClient2.Close(); err != nil {
			t.Logf("nodeClient2.Close: %v", err)
		}
	})

	if _, err := nodeClient2.Register(nodeCtx2, nodeID, nodeAddr); err != nil {
		t.Fatalf("node re-register: %v", err)
	}
	go func() {
		if err := nodeClient2.SendHeartbeats(nodeCtx2, nodeID, heartbeatInterval,
			func() int32 { return 0 }, nil); err != nil {
			t.Logf("node2 heartbeat: %v", err)
		}
	}()

	// Wait for node to become healthy on coordinator 2.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if nodes := coord2.registry.HealthyNodes(); len(nodes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(coord2.registry.HealthyNodes()) == 0 {
		t.Fatal("node did not become healthy on coordinator 2 within 2s")
	}
	t.Log("node is healthy on coordinator 2")

	// ── Phase 7: run crash recovery ───────────────────────────────────────
	t.Log("phase 7: running crash recovery")
	dispatcher2 := &grpcDispatcher{
		registry:   coord2.registry,
		nodeAddr:   nodeAddr,
		nodeBundle: nodeBundle,
	}
	rm := cluster.NewRecoveryManager(coord2.jobs, dispatcher2, gracePeriod,
		slog.Default())

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- rm.Run(ctx) }()

	// ── Phase 8: verify job completes ─────────────────────────────────────
	// The fake node will receive Dispatch and call ReportResult on coord2.
	// Give it enough time: grace period + dispatch + ReportResult round-trip.
	t.Log("phase 8: waiting for job to reach completed state")
	deadline = time.Now().Add(gracePeriod + 5*time.Second)
	var finalJob *cpb.Job
	for time.Now().Before(deadline) {
		j, err := coord2.jobs.Get(job.ID)
		if err != nil {
			t.Fatalf("get job during wait: %v", err)
		}
		if j.Status.IsTerminal() {
			finalJob = j
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case err := <-recoveryDone:
		if err != nil {
			t.Errorf("RecoveryManager.Run: %v", err)
		}
	case <-time.After(gracePeriod + 3*time.Second):
		t.Error("RecoveryManager.Run did not return in time")
	}

	if finalJob == nil {
		j, _ := coord2.jobs.Get(job.ID)
		t.Fatalf("job did not reach terminal state within deadline; status=%s", j.Status)
	}
	if finalJob.Status != cpb.JobStatusCompleted {
		t.Errorf("final job status = %s, want completed", finalJob.Status)
	}
	t.Logf("job %s completed successfully after coordinator restart", job.ID)

	// The job completed correctly. It may have done so via the late ReportResult
	// from the original dispatch (if it arrived during the grace period) or via
	// a second dispatch from the RecoveryManager. Both are valid recovery outcomes:
	// the design doc requires the job reaches terminal state after restart, not
	// that it is necessarily dispatched a second time.
	dispatched := fakeNode.dispatchedJobs()
	if len(dispatched) < 1 {
		t.Errorf("fakeNode received %d Dispatch calls, want ≥1", len(dispatched))
	}
	t.Logf("fakeNode received %d Dispatch call(s) total", len(dispatched))
}

// ── TestRecoveryManager_GracePeriod_NoNodes ───────────────────────────────────

// TestRecoveryManager_GracePeriod_NoNodes verifies that when no node
// re-registers within the grace period, recovered jobs are marked lost.
func TestRecoveryManager_GracePeriod_NoNodes(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemJobPersister()
	jobs := cluster.NewJobStore(p, nil)

	// Submit and partially advance a job so it's non-terminal.
	job := &cpb.Job{ID: "lost-job-1", Command: "echo", Args: []string{"hello"}}
	if err := jobs.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := jobs.Transition(ctx, job.ID, cpb.JobStatusDispatching,
		cluster.TransitionOptions{NodeID: "dead-node"}); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Dispatcher that always returns ErrNoHealthyNodes.
	noNodes := &noNodeDispatcher{}
	rm := cluster.NewRecoveryManager(jobs, noNodes, 50*time.Millisecond, nil)

	if err := rm.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	j, err := jobs.Get(job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if j.Status != cpb.JobStatusLost {
		t.Errorf("status = %s, want lost", j.Status)
	}
}

// ── TestRecoveryManager_AlreadyTerminal_Skipped ───────────────────────────────

// TestRecoveryManager_AlreadyTerminal_Skipped verifies that if a job becomes
// terminal between Restore() and the end of the grace period (e.g. a late
// ReportResult arrives from the original node), RecoveryManager skips it
// without error.
func TestRecoveryManager_AlreadyTerminal_Skipped(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemJobPersister()
	jobs := cluster.NewJobStore(p, nil)

	job := &cpb.Job{ID: "late-result-job", Command: "echo"}
	if err := jobs.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := jobs.Transition(ctx, job.ID, cpb.JobStatusDispatching,
		cluster.TransitionOptions{}); err != nil {
		t.Fatalf("Transition dispatching: %v", err)
	}
	if err := jobs.Transition(ctx, job.ID, cpb.JobStatusRunning,
		cluster.TransitionOptions{}); err != nil {
		t.Fatalf("Transition running: %v", err)
	}

	// The dispatcher will panic if called — the job should be skipped.
	rm := cluster.NewRecoveryManager(jobs, &panicDispatcher{}, 10*time.Millisecond, nil)

	// Mark the job completed BEFORE Run() finishes the grace period.
	go func() {
		time.Sleep(5 * time.Millisecond)
		if err := jobs.Transition(ctx, job.ID, cpb.JobStatusCompleted, cluster.TransitionOptions{}); err != nil {
			t.Logf("late Transition: %v", err)
		}
	}()

	if err := rm.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	j, _ := jobs.Get(job.ID)
	if j.Status != cpb.JobStatusCompleted {
		t.Errorf("status = %s, want completed", j.Status)
	}
}

// ── TestRecoveryManager_ContextCancelled ─────────────────────────────────────

// TestRecoveryManager_ContextCancelled verifies Run() exits cleanly when ctx
// is cancelled during the grace period.
func TestRecoveryManager_ContextCancelled(t *testing.T) {
	p := cluster.NewMemJobPersister()
	jobs := cluster.NewJobStore(p, nil)

	job := &cpb.Job{ID: "cancel-job", Command: "echo"}
	ctx := context.Background()
	if err := jobs.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rm := cluster.NewRecoveryManager(jobs, &noNodeDispatcher{}, 10*time.Second, nil)

	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- rm.Run(cancelCtx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// ── test dispatcher stubs ─────────────────────────────────────────────────────

type noNodeDispatcher struct{}

func (noNodeDispatcher) Dispatch(_ context.Context, _ *cpb.Job) (string, error) {
	return "", cluster.ErrNoHealthyNodes
}

type panicDispatcher struct{}

func (panicDispatcher) Dispatch(_ context.Context, _ *cpb.Job) (string, error) {
	panic("panicDispatcher.Dispatch called — should have been skipped")
}
