// internal/cluster/prune_loop_test.go
//
// Unit tests for Registry.RunPruneLoop.
//
// Coverage:
//   ✓ Node stops heartbeating → loop marks it unhealthy after 2× interval
//   ✓ Healthy node is never pruned while heartbeating
//   ✓ Loop exits cleanly on context cancellation
//   ✓ Multiple nodes: only the stale one is pruned
//
// These tests use a short heartbeatInterval (50 ms) so they run fast.
// They are skipped under -short so CI can use -short for the fast path.

package cluster_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// pruneInterval is the heartbeat interval used across all prune loop tests.
// staleAfter = 2 × pruneInterval, so nodes go stale after 100 ms of silence.
const pruneInterval = 50 * time.Millisecond

// newPruneRegistry returns a Registry with the short pruneInterval.
func newPruneRegistry(t *testing.T) *cluster.Registry {
	t.Helper()
	return cluster.NewRegistry(cluster.NopPersister{}, pruneInterval, nil)
}

// ── TestPruneLoop_MarksNodeUnhealthy ─────────────────────────────────────────
//
// Phase 2 exit criterion:
//   "Node stops sending heartbeat; coordinator marks unhealthy after
//    expected interval."
//
// Timeline:
//  1. Register a node and send one heartbeat → healthy.
//  2. Start RunPruneLoop in a goroutine.
//  3. Stop heartbeating (do nothing).
//  4. Wait 3× staleAfter (= 6× heartbeatInterval) for the loop to tick
//     at least twice and detect the stale node.
//  5. Assert node is unhealthy.

func TestPruneLoop_MarksNodeUnhealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based prune loop test in -short mode")
	}

	r := newPruneRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register and heartbeat once so the node starts healthy.
	registerNode(t, r, "prune-node", "10.0.0.1:8080")
	sendHeartbeat(t, r, "prune-node", 0)

	n, _ := r.Lookup("prune-node")
	if !n.Healthy {
		t.Fatal("node should be healthy after registration + heartbeat")
	}

	// Start the prune loop.
	go r.RunPruneLoop(ctx)

	// staleAfter = 2 × pruneInterval = 100 ms.
	// Wait 3 × staleAfter to give the loop several ticks to detect staleness.
	staleAfter := 2 * pruneInterval
	time.Sleep(3 * staleAfter)

	n, ok := r.Lookup("prune-node")
	if !ok {
		t.Fatal("node disappeared from registry")
	}
	if n.Healthy {
		t.Errorf("node should be unhealthy after %v of no heartbeats, but is still healthy",
			3*staleAfter)
	}
}

// ── TestPruneLoop_SparesFreshNode ────────────────────────────────────────────
//
// A node that keeps heartbeating must never be marked stale.

func TestPruneLoop_SparesFreshNode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based prune loop test in -short mode")
	}

	r := newPruneRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerNode(t, r, "live-node", "10.0.0.2:8080")
	sendHeartbeat(t, r, "live-node", 0)

	go r.RunPruneLoop(ctx)

	// Send heartbeats continuously for 6 ticks, then check.
	staleAfter := 2 * pruneInterval
	deadline := time.Now().Add(3 * staleAfter)
	for time.Now().Before(deadline) {
		sendHeartbeat(t, r, "live-node", 0)
		time.Sleep(pruneInterval / 2)
	}

	n, ok := r.Lookup("live-node")
	if !ok {
		t.Fatal("live node disappeared from registry")
	}
	if !n.Healthy {
		t.Error("node that kept heartbeating should still be healthy")
	}
}

// ── TestPruneLoop_ExitsOnContextCancel ───────────────────────────────────────
//
// RunPruneLoop must return promptly when ctx is cancelled.

func TestPruneLoop_ExitsOnContextCancel(t *testing.T) {
	r := newPruneRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.RunPruneLoop(ctx)
		close(done)
	}()

	// Let it spin for one tick then cancel.
	time.Sleep(pruneInterval + 10*time.Millisecond)
	cancel()

	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("RunPruneLoop did not exit within 2s of context cancellation")
	}
}

// ── TestPruneLoop_OnlyStaleNodePruned ────────────────────────────────────────
//
// With two nodes — one heartbeating, one silent — only the silent one goes stale.

func TestPruneLoop_OnlyStaleNodePruned(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based prune loop test in -short mode")
	}

	r := newPruneRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerNode(t, r, "active", "10.0.0.1:8080")
	registerNode(t, r, "silent", "10.0.0.2:8080")
	sendHeartbeat(t, r, "active", 0)
	sendHeartbeat(t, r, "silent", 0)

	go r.RunPruneLoop(ctx)

	staleAfter := 2 * pruneInterval
	deadline := time.Now().Add(3 * staleAfter)
	for time.Now().Before(deadline) {
		// Only keep "active" alive.
		sendHeartbeat(t, r, "active", 0)
		time.Sleep(pruneInterval / 2)
	}

	active, _ := r.Lookup("active")
	silent, _ := r.Lookup("silent")

	if !active.Healthy {
		t.Error("active node (kept heartbeating) should still be healthy")
	}
	if silent.Healthy {
		t.Error("silent node (stopped heartbeating) should be unhealthy")
	}
}

// ── TestPruneLoop_RecoversAfterHeartbeatResumes ───────────────────────────────
//
// A node that goes stale but then resumes heartbeating becomes healthy again.
// This verifies the prune loop doesn't permanently blacklist nodes.

func TestPruneLoop_RecoversAfterHeartbeatResumes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based prune loop test in -short mode")
	}

	r := newPruneRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerNode(t, r, "recovering", "10.0.0.3:8080")
	sendHeartbeat(t, r, "recovering", 0)

	go r.RunPruneLoop(ctx)

	// Let the node go stale.
	staleAfter := 2 * pruneInterval
	time.Sleep(3 * staleAfter)

	n, _ := r.Lookup("recovering")
	if n.Healthy {
		t.Fatal("expected node to be stale before resume")
	}

	// Resume heartbeating — node should become healthy again.
	sendHeartbeat(t, r, "recovering", 0)
	time.Sleep(10 * time.Millisecond) // allow the atomic write to propagate

	n, _ = r.Lookup("recovering")
	if !n.Healthy {
		t.Error("node should be healthy again after resuming heartbeats")
	}
}

// ── TestPruneLoop_RegisterRPC ────────────────────────────────────────────────
//
// Register() calls storeLastSeen, so a freshly registered node should survive
// at least one prune cycle without sending an explicit heartbeat.

func TestPruneLoop_RegisterKeepsNodeHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sleep-based prune loop test in -short mode")
	}

	r := newPruneRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.RunPruneLoop(ctx)

	// Register without sending a heartbeat.
	// Register() calls storeLastSeen(now) so the node starts healthy.
	_, err := r.Register(context.Background(), &pb.RegisterRequest{
		NodeId:  "just-registered",
		Address: "10.0.0.9:8080",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Immediately after registration the node should be healthy.
	n, ok := r.Lookup("just-registered")
	if !ok {
		t.Fatal("node not found after Register")
	}
	if !n.Healthy {
		t.Error("freshly registered node should be healthy before any prune tick")
	}

	// After 3× staleAfter with no heartbeat it should go stale.
	staleAfter := 2 * pruneInterval
	time.Sleep(3 * staleAfter)

	n, _ = r.Lookup("just-registered")
	if n.Healthy {
		t.Error("node with no heartbeats should be stale after 3× staleAfter")
	}
}
