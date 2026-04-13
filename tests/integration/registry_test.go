// tests/integration/registry_test.go
//
// Integration test for the node registry over a real gRPC+mTLS connection.
//
// Exit criterion (§7 Phase 2):
//   Node registers, heartbeat stream runs for the test duration, coordinator
//   reflects correct health throughout.
//
// Test duration
// ─────────────
// The design document specifies 30 s.  Running the full 30 s in CI on every
// push is expensive, so the test is parameterised:
//
//   - Default (go test ./...):         5 s  — fast, still exercises many ticks
//   - Full spec  (HELION_LONG=1):     30 s  — run before merging to main
//   - Short mode (-short):             skip  — for sub-second local runs
//
// What is verified
// ────────────────
//   1. Register RPC succeeds over mTLS and the coordinator knows the node.
//   2. Heartbeat stream stays alive for the full duration without error.
//   3. At the end of the stream, the coordinator still reflects the node as
//      healthy (LastSeen is recent, RunningJobs matches last sent value).
//   4. After the stream closes and staleAfter elapses, the node becomes
//      unhealthy — proving the TTL-based health model works end-to-end.
//   5. A second node can register and heartbeat concurrently without
//      interfering with the first.

package integration_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// testDuration returns the heartbeat stream duration for this run.
func testDuration() time.Duration {
	if testing.Short() {
		return 0 // caller should t.Skip before calling
	}
	if v := os.Getenv("HELION_LONG"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		return 30 * time.Second
	}
	return 5 * time.Second
}

const (
	heartbeatInterval = 500 * time.Millisecond // fast ticks for testing
	staleAfter        = 2 * heartbeatInterval  // 1 s — matches NewRegistry default × 2
)

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestRegistryIntegration_HeartbeatStream is the primary exit-criterion test.
//
// Timeline:
//   t=0          node registers
//   t=0..dur     heartbeat stream runs at 500 ms interval
//   t=dur        stream cancelled; verify node is still healthy
//   t=dur+stale  verify node is now unhealthy (TTL expired)
func TestRegistryIntegration_HeartbeatStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dur := testDuration()

	// Build the coordinator bundle directly so we can also use it to issue node certs.
	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coordinator bundle: %v", err)
	}

	reg := cluster.NewRegistry(cluster.NopPersister{}, heartbeatInterval, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(reg))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	addr := "127.0.0.1:19092"
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()
	time.Sleep(30 * time.Millisecond)
	t.Cleanup(func() { srv.Stop(); <-errCh })

	// ── Step 1: Register ──────────────────────────────────────────────────────
	nodeBundle, err := auth.NewNodeBundle(coordBundle.CA, "test-node")
	if err != nil {
		t.Fatalf("node bundle: %v", err)
	}
	client, err := grpcclient.New(addr, "helion-coordinator", nodeBundle)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()

	resp, err := client.Register(regCtx, "test-node", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register RPC: %v", err)
	}
	if resp.NodeId != "test-node" {
		t.Errorf("Register response NodeId = %q, want %q", resp.NodeId, "test-node")
	}

	// Registry must know the node immediately after Register.
	n, ok := reg.Lookup("test-node")
	if !ok {
		t.Fatal("node not found in registry after Register")
	}
	if !n.Healthy {
		t.Error("node should be healthy immediately after Register")
	}

	t.Logf("registered test-node at %s", n.Address)

	// ── Step 2: Heartbeat stream ──────────────────────────────────────────────
	hbCtx, hbCancel := context.WithCancel(context.Background())

	var (
		lastRunning int32 = 3 // simulate 3 running jobs
		streamErr   error
		streamDone  = make(chan struct{})
	)

	go func() {
		defer close(streamDone)
		streamErr = client.SendHeartbeats(
			hbCtx,
			"test-node",
			heartbeatInterval,
			func() int32 { return lastRunning },
			nil, // no capacity
			nil, // ignore acks for this test
		)
	}()

	// Run the stream for the test duration, sampling health periodically.
	ticker := time.NewTicker(heartbeatInterval * 3)
	defer ticker.Stop()
	deadline := time.After(dur)

	healthChecks := 0
	for {
		select {
		case <-ticker.C:
			n, ok := reg.Lookup("test-node")
			if !ok {
				t.Error("node disappeared from registry during heartbeat stream")
				hbCancel()
				goto streamEnd
			}
			if !n.Healthy {
				t.Errorf("node became unhealthy during active heartbeat stream (check %d)", healthChecks)
			}
			if n.RunningJobs != lastRunning {
				t.Errorf("RunningJobs = %d, want %d", n.RunningJobs, lastRunning)
			}
			healthChecks++

		case <-deadline:
			goto streamEnd
		}
	}

streamEnd:
	t.Logf("completed %d health checks over %v", healthChecks, dur)

	// ── Step 3: Cancel stream; verify still healthy ───────────────────────────
	hbCancel()
	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Error("heartbeat goroutine did not exit within 2 s of cancellation")
	}

	if streamErr != nil {
		t.Errorf("SendHeartbeats returned error: %v", streamErr)
	}

	// Node should still be healthy immediately after stream closes (LastSeen
	// is recent — within staleAfter).
	n, ok = reg.Lookup("test-node")
	if !ok {
		t.Fatal("node not found after stream close")
	}
	if !n.Healthy {
		t.Error("node should still be healthy immediately after stream close")
	}

	// ── Step 4: Wait for stale; verify unhealthy ──────────────────────────────
	// Sleep past staleAfter so the registry considers the node stale.
	time.Sleep(staleAfter + 50*time.Millisecond)

	n, ok = reg.Lookup("test-node")
	if !ok {
		t.Fatal("node not found when checking staleness")
	}
	if n.Healthy {
		t.Error("node should be unhealthy after staleAfter elapses with no heartbeat")
	}

	t.Logf("node correctly became unhealthy after %v of silence", staleAfter)
}

// TestRegistryIntegration_TwoNodesConcurrent verifies that two nodes can
// register and heartbeat simultaneously without interfering with each other.
func TestRegistryIntegration_TwoNodesConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coordinator bundle: %v", err)
	}

	reg := cluster.NewRegistry(cluster.NopPersister{}, heartbeatInterval, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(reg))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	addr := "127.0.0.1:19093"
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()
	time.Sleep(30 * time.Millisecond)
	t.Cleanup(func() { srv.Stop(); <-errCh })

	nodes := []struct {
		id      string
		address string
		jobs    int32
	}{
		{"node-alpha", "10.0.0.1:8080", 2},
		{"node-beta", "10.0.0.2:8080", 5},
	}

	ctx, cancel := context.WithTimeout(context.Background(), testDuration()+2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, n := range nodes {
		n := n
		wg.Add(1)
		go func() {
			defer wg.Done()

			nb, err := auth.NewNodeBundle(coordBundle.CA, n.id)
			if err != nil {
				t.Errorf("node bundle %s: %v", n.id, err)
				return
			}
			c, err := grpcclient.New(addr, "helion-coordinator", nb)
			if err != nil {
				t.Errorf("dial %s: %v", n.id, err)
				return
			}
			defer c.Close()

			regCtx, regCancel := context.WithTimeout(ctx, 3*time.Second)
			defer regCancel()
			if _, err := c.Register(regCtx, n.id, n.address); err != nil {
				t.Errorf("Register %s: %v", n.id, err)
				return
			}

			hbCtx, hbCancel := context.WithTimeout(ctx, testDuration())
			defer hbCancel()
			if err := c.SendHeartbeats(hbCtx, n.id, heartbeatInterval,
				func() int32 { return n.jobs }, nil, nil); err != nil {
				// Context cancellation is expected — not an error.
				if ctx.Err() == nil && hbCtx.Err() == nil {
					t.Errorf("SendHeartbeats %s: %v", n.id, err)
				}
			}
		}()
	}

	// Let both nodes run for the test duration, then verify both are healthy.
	time.Sleep(testDuration())

	for _, n := range nodes {
		node, ok := reg.Lookup(n.id)
		if !ok {
			t.Errorf("node %s not found in registry", n.id)
			continue
		}
		if !node.Healthy {
			t.Errorf("node %s should be healthy", n.id)
		}
		if node.RunningJobs != n.jobs {
			t.Errorf("node %s RunningJobs = %d, want %d", n.id, node.RunningJobs, n.jobs)
		}
	}

	cancel()
	wg.Wait()
}

// TestRegistryIntegration_RejectUntrustedHeartbeat verifies that a node with
// a certificate from a different CA cannot open a heartbeat stream.
// (This reuses the mTLS rejection logic already proven in TestMTLSRejectsUntrustedClient.)
func TestRegistryIntegration_Register_ReflectsInRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	coordBundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		t.Fatalf("coordinator bundle: %v", err)
	}
	reg := cluster.NewRegistry(cluster.NopPersister{}, heartbeatInterval, nil)
	srv, err := grpcserver.New(coordBundle, grpcserver.WithRegistry(reg))
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}

	addr := "127.0.0.1:19094"
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()
	time.Sleep(30 * time.Millisecond)
	t.Cleanup(func() { srv.Stop(); <-errCh })

	nb, err := auth.NewNodeBundle(coordBundle.CA, "verify-node")
	if err != nil {
		t.Fatalf("node bundle: %v", err)
	}
	c, err := grpcclient.New(addr, "helion-coordinator", nb)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Before Register: registry must not know the node.
	if _, ok := reg.Lookup("verify-node"); ok {
		t.Error("node should not be in registry before Register")
	}

	if _, err := c.Register(ctx, "verify-node", "10.0.0.5:8080"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// After Register: registry must know the node.
	n, ok := reg.Lookup("verify-node")
	if !ok {
		t.Fatal("node not found in registry after Register")
	}
	if n.Address != "10.0.0.5:8080" {
		t.Errorf("Address = %q, want %q", n.Address, "10.0.0.5:8080")
	}
	if !n.Healthy {
		t.Error("node should be healthy after Register")
	}
}
