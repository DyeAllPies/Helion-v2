// internal/cluster/registry_test.go
//
// Unit tests for the node Registry.
// Uses real proto types from github.com/DyeAllPies/Helion-v2/proto.
//
// Coverage:
//   ✓ Register: new node added and immediately healthy
//   ✓ Register: re-registration updates address without duplicating entry
//   ✓ HandleHeartbeat: updates LastSeen and RunningJobs atomically
//   ✓ HandleHeartbeat: heartbeat from unregistered node is rejected
//   ✓ HealthyNodes: returns only nodes with recent heartbeats
//   ✓ HealthyNodes: stale node excluded after staleAfter elapses
//   ✓ PruneStaleNodes: stale node identified; audit entry written
//   ✓ PruneStaleNodes: healthy node not included
//   ✓ Snapshot: includes all nodes regardless of health
//   ✓ Lookup: returns correct node; false for unknown ID
//   ✓ Len: correct count
//   ✓ Restore: loads persisted nodes as unhealthy
//   ✓ Restore + Heartbeat: node becomes healthy after heartbeat
//   ✓ Concurrent heartbeats: no data race (run with -race)
//   ✓ Concurrent Register + HandleHeartbeat: no data race
//   ✓ NopPersister: all methods return nil
//   ✓ MemPersister: SaveNode/LoadAllNodes/AppendAudit round-trip

package cluster_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb  "github.com/DyeAllPies/Helion-v2/proto"
)

// ── helpers ───────────────────────────────────────────────────────────────────

const testInterval = 100 * time.Millisecond

func newRegistry(t *testing.T) *cluster.Registry {
	t.Helper()
	return cluster.NewRegistry(cluster.NopPersister{}, testInterval, nil)
}

func newMemRegistry(t *testing.T) (*cluster.Registry, *cluster.MemPersister) {
	t.Helper()
	p := cluster.NewMemPersister()
	return cluster.NewRegistry(p, testInterval, nil), p
}

// registerNode sends a Register RPC using the real proto.RegisterRequest.
func registerNode(t *testing.T, r *cluster.Registry, nodeID, addr string) {
	t.Helper()
	_, err := r.Register(context.Background(), &pb.RegisterRequest{
		NodeId:  nodeID,
		Address: addr,
		// Certificate: empty for Phase 2
	})
	if err != nil {
		t.Fatalf("Register %q: %v", nodeID, err)
	}
}

// sendHeartbeat sends one heartbeat using the real proto.HeartbeatMessage.
// Note: HeartbeatMessage has no Address field and no Seq field.
// Timestamp is Unix nanoseconds (int64); 0 means "use wall clock".
func sendHeartbeat(t *testing.T, r *cluster.Registry, nodeID string, running int32) {
	t.Helper()
	err := r.HandleHeartbeat(context.Background(), &pb.HeartbeatMessage{
		NodeId:      nodeID,
		Timestamp:   time.Now().UnixNano(),
		RunningJobs: running,
	})
	if err != nil {
		t.Fatalf("HandleHeartbeat %q: %v", nodeID, err)
	}
}

// ── Registration ─────────────────────────────────────────────────────────────

func TestRegister_NewNode(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "node-1", "10.0.0.1:8080")

	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}
	n, ok := r.Lookup("node-1")
	if !ok {
		t.Fatal("Lookup: node not found after Register")
	}
	if n.Address != "10.0.0.1:8080" {
		t.Errorf("Address = %q, want %q", n.Address, "10.0.0.1:8080")
	}
	if !n.Healthy {
		t.Error("newly registered node should be healthy")
	}
}

func TestRegister_ReRegistration_UpdatesAddress(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "node-1", "10.0.0.1:8080")
	registerNode(t, r, "node-1", "10.0.0.1:9090") // restart on new port

	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (no duplicate)", r.Len())
	}
	n, _ := r.Lookup("node-1")
	if n.Address != "10.0.0.1:9090" {
		t.Errorf("Address = %q, want %q", n.Address, "10.0.0.1:9090")
	}
}

func TestRegister_Response_HasNodeId(t *testing.T) {
	r := newRegistry(t)
	resp, err := r.Register(context.Background(), &pb.RegisterRequest{
		NodeId: "node-1", Address: "10.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.NodeId != "node-1" {
		t.Errorf("Response.NodeId = %q, want %q", resp.NodeId, "node-1")
	}
}

// ── Heartbeat ────────────────────────────────────────────────────────────────

func TestHeartbeat_UpdatesLastSeen(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "node-1", "10.0.0.1:8080")

	before := time.Now()
	time.Sleep(time.Millisecond)
	sendHeartbeat(t, r, "node-1", 0)
	time.Sleep(time.Millisecond)
	after := time.Now()

	n, _ := r.Lookup("node-1")
	if n.LastSeen.Before(before) || n.LastSeen.After(after) {
		t.Errorf("LastSeen %v not in [%v, %v]", n.LastSeen, before, after)
	}
}

func TestHeartbeat_UpdatesRunningJobs(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "node-1", "10.0.0.1:8080")
	sendHeartbeat(t, r, "node-1", 5)

	n, _ := r.Lookup("node-1")
	if n.RunningJobs != 5 {
		t.Errorf("RunningJobs = %d, want 5", n.RunningJobs)
	}
}

func TestHeartbeat_UnregisteredNodeRejected(t *testing.T) {
	r := newRegistry(t)
	// Heartbeat before Register must be rejected; no entry should be created.
	err := r.HandleHeartbeat(context.Background(), &pb.HeartbeatMessage{
		NodeId:      "orphan-node",
		RunningJobs: 0,
	})
	if err != cluster.ErrNodeNotRegistered {
		t.Fatalf("HandleHeartbeat unregistered node: got %v, want ErrNodeNotRegistered", err)
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d, want 0: unregistered heartbeat must not create an entry", r.Len())
	}
}

// ── Health ────────────────────────────────────────────────────────────────────

func TestHealthyNodes_ExcludesStale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based test in -short mode")
	}
	r := newRegistry(t)
	registerNode(t, r, "fresh", "10.0.0.1:8080")
	registerNode(t, r, "stale", "10.0.0.2:8080")

	// Wait past staleAfter without heartbeating "stale".
	time.Sleep(2*testInterval + 10*time.Millisecond)
	// Refresh "fresh".
	sendHeartbeat(t, r, "fresh", 0)

	healthy := r.HealthyNodes()
	if len(healthy) != 1 {
		t.Fatalf("HealthyNodes = %d, want 1", len(healthy))
	}
	if healthy[0].NodeID != "fresh" {
		t.Errorf("healthy node = %q, want %q", healthy[0].NodeID, "fresh")
	}
}

func TestSnapshot_IncludesAll(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "n1", "10.0.0.1:8080")
	registerNode(t, r, "n2", "10.0.0.2:8080")
	registerNode(t, r, "n3", "10.0.0.3:8080")

	if len(r.Snapshot()) != 3 {
		t.Errorf("Snapshot len = %d, want 3", len(r.Snapshot()))
	}
}

// ── PruneStaleNodes ───────────────────────────────────────────────────────────

func TestPruneStaleNodes_IdentifiesStale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based test in -short mode")
	}
	r, mem := newMemRegistry(t)
	registerNode(t, r, "stale-node", "10.0.0.9:8080")

	time.Sleep(2*testInterval + 10*time.Millisecond)

	stale := r.PruneStaleNodes(context.Background())
	if len(stale) != 1 {
		t.Fatalf("PruneStaleNodes = %d stale, want 1", len(stale))
	}
	if stale[0] != "stale-node" {
		t.Errorf("stale nodeID = %q, want %q", stale[0], "stale-node")
	}

	// Allow async audit goroutine to complete.
	time.Sleep(20 * time.Millisecond)

	mem.Mu()
	defer mem.MuUnlock()
	found := false
	for _, a := range mem.Audits {
		if a["event_type"] == "node.stale" {
			found = true
			break
		}
	}
	if !found {
		t.Error("PruneStaleNodes: no audit entry written")
	}
}

func TestPruneStaleNodes_SparesFreshNode(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "fresh", "10.0.0.1:8080")

	stale := r.PruneStaleNodes(context.Background())
	if len(stale) != 0 {
		t.Errorf("PruneStaleNodes immediately after register: got %v, want none", stale)
	}
}

// ── Lookup ────────────────────────────────────────────────────────────────────

func TestLookup_NotFound(t *testing.T) {
	r := newRegistry(t)
	_, ok := r.Lookup("ghost")
	if ok {
		t.Error("Lookup unknown node: got ok=true, want false")
	}
}

// ── Restore ───────────────────────────────────────────────────────────────────

func TestRestore_LoadsNodes(t *testing.T) {
	p := cluster.NewMemPersister()
	ctx := context.Background()
	_ = p.SaveNode(ctx, &cpb.Node{NodeID: "n1", Address: "10.0.0.1:8080"})
	_ = p.SaveNode(ctx, &cpb.Node{NodeID: "n2", Address: "10.0.0.2:8080"})

	r := cluster.NewRegistry(p, testInterval, nil)
	if err := r.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if r.Len() != 2 {
		t.Fatalf("Len after Restore = %d, want 2", r.Len())
	}
}

func TestRestore_RestoredNodesAreUnhealthy(t *testing.T) {
	p := cluster.NewMemPersister()
	ctx := context.Background()
	_ = p.SaveNode(ctx, &cpb.Node{NodeID: "n1", Address: "10.0.0.1:8080", Healthy: true})

	r := cluster.NewRegistry(p, testInterval, nil)
	_ = r.Restore(ctx)

	n, ok := r.Lookup("n1")
	if !ok {
		t.Fatal("Lookup after Restore: not found")
	}
	if n.Healthy {
		t.Error("restored node should be unhealthy until it heartbeats")
	}
}

func TestRestore_ThenHeartbeat_BecomesHealthy(t *testing.T) {
	p := cluster.NewMemPersister()
	ctx := context.Background()
	_ = p.SaveNode(ctx, &cpb.Node{NodeID: "n1", Address: "10.0.0.1:8080"})

	r := cluster.NewRegistry(p, testInterval, nil)
	_ = r.Restore(ctx)

	sendHeartbeat(t, r, "n1", 0)

	n, _ := r.Lookup("n1")
	if !n.Healthy {
		t.Error("node should be healthy after post-restore heartbeat")
	}
}

// ── MemPersister ──────────────────────────────────────────────────────────────

func TestMemPersister_RoundTrip(t *testing.T) {
	p := cluster.NewMemPersister()
	ctx := context.Background()
	want := &cpb.Node{NodeID: "n1", Address: "10.0.0.1:8080", Healthy: true}
	_ = p.SaveNode(ctx, want)

	nodes, err := p.LoadAllNodes(ctx)
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != want.NodeID {
		t.Errorf("LoadAllNodes = %v, want [{NodeID: n1}]", nodes)
	}
}

func TestMemPersister_AppendAudit(t *testing.T) {
	p := cluster.NewMemPersister()
	_ = p.AppendAudit(context.Background(), "test.event", "actor", "target", "detail")

	p.Mu()
	defer p.MuUnlock()
	if len(p.Audits) != 1 {
		t.Fatalf("Audits len = %d, want 1", len(p.Audits))
	}
	if p.Audits[0]["event_type"] != "test.event" {
		t.Errorf("event_type = %q, want %q", p.Audits[0]["event_type"], "test.event")
	}
}

// ── NopPersister ──────────────────────────────────────────────────────────────

func TestNopPersister_NoErrors(t *testing.T) {
	p := cluster.NopPersister{}
	ctx := context.Background()
	if err := p.SaveNode(ctx, &cpb.Node{}); err != nil {
		t.Errorf("SaveNode: %v", err)
	}
	if _, err := p.LoadAllNodes(ctx); err != nil {
		t.Errorf("LoadAllNodes: %v", err)
	}
	if err := p.AppendAudit(ctx, "", "", "", ""); err != nil {
		t.Errorf("AppendAudit: %v", err)
	}
}

// ── Concurrency (run with -race) ──────────────────────────────────────────────

func TestConcurrentHeartbeats(t *testing.T) {
	r := newRegistry(t)
	registerNode(t, r, "node-1", "10.0.0.1:8080")

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			sendHeartbeat(t, r, "node-1", int32(i%5))
		}(i)
	}
	wg.Wait()

	n, ok := r.Lookup("node-1")
	if !ok {
		t.Fatal("node-1 not found after concurrent heartbeats")
	}
	if !n.Healthy {
		t.Error("node-1 should be healthy after concurrent heartbeats")
	}
}

func TestConcurrentRegisterAndHeartbeat(t *testing.T) {
	r := newRegistry(t)
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = r.Register(context.Background(), &pb.RegisterRequest{
				NodeId: "shared-node", Address: "10.0.0.1:8080",
			})
		}()
		go func() {
			defer wg.Done()
			_ = r.HandleHeartbeat(context.Background(), &pb.HeartbeatMessage{
				NodeId:    "shared-node",
				Timestamp: time.Now().UnixNano(),
			})
		}()
	}
	wg.Wait()

	if r.Len() != 1 {
		t.Errorf("Len after concurrent register+heartbeat = %d, want 1", r.Len())
	}
}

// ── SetCertPinner / SetCertVerifier / SetStreamRevoker ────────────────────────

type stubStreamRevoker struct {
	cancelled []string
}

func (s *stubStreamRevoker) CancelStream(nodeID string) {
	s.cancelled = append(s.cancelled, nodeID)
}

type stubCertVerifier struct {
	err error
}

func (v *stubCertVerifier) VerifyNodeCertMLDSA(derBytes []byte) error {
	return v.err
}

func TestSetStreamRevoker_CalledOnRevoke(t *testing.T) {
	r := newRegistry(t)
	revoker := &stubStreamRevoker{}
	r.SetStreamRevoker(revoker)

	registerNode(t, r, "stream-node", "10.0.0.1:8080")
	if err := r.RevokeNode(context.Background(), "stream-node", "test revocation"); err != nil {
		t.Fatalf("RevokeNode: %v", err)
	}

	if len(revoker.cancelled) == 0 || revoker.cancelled[0] != "stream-node" {
		t.Errorf("CancelStream not called with stream-node; got %v", revoker.cancelled)
	}
}

func TestSetCertPinner_StoresFingerprintOnFirstRegister(t *testing.T) {
	r := newRegistry(t)
	pinner := cluster.NewMemCertPinner()
	r.SetCertPinner(pinner)

	ctx := context.Background()
	derBytes := []byte("fake-der-cert-for-pinning-test")

	_, err := r.Register(ctx, &pb.RegisterRequest{
		NodeId:      "pinned-node",
		Address:     "10.0.0.1:8080",
		Certificate: derBytes,
	})
	if err != nil {
		t.Fatalf("Register with cert: %v", err)
	}

	// Pin should now be stored.
	fp, err := pinner.GetPin(ctx, "pinned-node")
	if err != nil {
		t.Fatalf("GetPin after Register: %v", err)
	}
	expected := cluster.CertFingerprint(derBytes)
	if fp != expected {
		t.Errorf("stored pin %q != expected %q", fp, expected)
	}
}

func TestSetCertPinner_RejectsMismatchedFingerprint(t *testing.T) {
	r := newRegistry(t)
	pinner := cluster.NewMemCertPinner()
	r.SetCertPinner(pinner)

	ctx := context.Background()

	// First registration pins cert-A.
	_, err := r.Register(ctx, &pb.RegisterRequest{
		NodeId:      "cert-node",
		Address:     "10.0.0.1:8080",
		Certificate: []byte("cert-a-der"),
	})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Second registration with a different cert must fail.
	_, err = r.Register(ctx, &pb.RegisterRequest{
		NodeId:      "cert-node",
		Address:     "10.0.0.1:8080",
		Certificate: []byte("cert-b-der"),
	})
	if err == nil {
		t.Fatal("expected fingerprint mismatch error, got nil")
	}
}

func TestSetCertPinner_SameFingerprintAllowed(t *testing.T) {
	r := newRegistry(t)
	pinner := cluster.NewMemCertPinner()
	r.SetCertPinner(pinner)

	ctx := context.Background()
	cert := []byte("same-cert-der")

	for i := 0; i < 2; i++ {
		if _, err := r.Register(ctx, &pb.RegisterRequest{
			NodeId:      "same-cert-node",
			Address:     "10.0.0.1:8080",
			Certificate: cert,
		}); err != nil {
			t.Fatalf("Register attempt %d: %v", i+1, err)
		}
	}
}

func TestSetCertVerifier_RejectsOnVerificationFailure(t *testing.T) {
	r := newRegistry(t)
	verifier := &stubCertVerifier{err: fmt.Errorf("ML-DSA sig invalid")}
	r.SetCertVerifier(verifier)

	ctx := context.Background()
	_, err := r.Register(ctx, &pb.RegisterRequest{
		NodeId:      "bad-sig-node",
		Address:     "10.0.0.1:8080",
		Certificate: []byte("some-der-bytes"),
	})
	if err == nil {
		t.Fatal("expected ML-DSA verification error, got nil")
	}
}

func TestSetCertVerifier_AcceptsOnVerificationSuccess(t *testing.T) {
	r := newRegistry(t)
	verifier := &stubCertVerifier{err: nil}
	r.SetCertVerifier(verifier)

	ctx := context.Background()
	_, err := r.Register(ctx, &pb.RegisterRequest{
		NodeId:      "good-sig-node",
		Address:     "10.0.0.1:8080",
		Certificate: []byte("some-der-bytes"),
	})
	if err != nil {
		t.Fatalf("Register with valid ML-DSA sig: %v", err)
	}
}
