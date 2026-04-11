// internal/persistence/recovery_test.go
//
// Crash-recovery — the primary exit criterion for Phase 2.

package persistence_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TestCrashRecovery is the primary exit-criterion test for Phase 2.
//
// It simulates the coordinator restarting after a crash:
//
//  1. Open a Store, write three nodes and two in-flight jobs.
//  2. Close the Store — flushes BadgerDB's WAL to disk.
//  3. Open a brand-new Store instance at the SAME path.
//  4. Verify all five records are readable with correct values.
//
// This proves the scheduler can call List[*helionpb.Job](store, PrefixJobs)
// on startup and find all jobs that were in-flight when the coordinator died.
func TestCrashRecovery(t *testing.T) {
	dir := t.TempDir()

	// ── Phase A: write and close (simulates coordinator running then dying) ───
	{
		s1, err := persistence.Open(dir)
		if err != nil {
			t.Fatalf("CrashRecovery Open A: %v", err)
		}

		nodeAddrs := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}
		for _, addr := range nodeAddrs {
			if err := persistence.Put(s1, persistence.NodeKey(addr), sv(addr)); err != nil {
				t.Fatalf("CrashRecovery Put node %q: %v", addr, err)
			}
		}

		jobIDs := []string{"job-running-1", "job-dispatching-2"}
		for _, id := range jobIDs {
			if err := persistence.Put(s1, persistence.JobKey(id), sv(id)); err != nil {
				t.Fatalf("CrashRecovery Put job %q: %v", id, err)
			}
		}

		// Close flushes the WAL. After this point the coordinator could be
		// killed and the data survives.
		if err := s1.Close(); err != nil {
			t.Fatalf("CrashRecovery Close A: %v", err)
		}
	}

	// ── Phase B: reopen at same path, verify all records survive ─────────────
	s2, err := persistence.Open(dir) // new Store value, same directory
	if err != nil {
		t.Fatalf("CrashRecovery Open B: %v", err)
	}
	defer func() {
		if err := s2.Close(); err != nil {
			t.Errorf("CrashRecovery Close B: %v", err)
		}
	}()

	// Individual Gets — verifies exact values survived.
	for _, addr := range []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"} {
		got, err := persistence.Get[*wrapperspb.StringValue](s2, persistence.NodeKey(addr))
		if err != nil {
			t.Errorf("CrashRecovery Get node %q: %v", addr, err)
			continue
		}
		if got.Value != addr {
			t.Errorf("CrashRecovery node value = %q, want %q", got.Value, addr)
		}
	}

	for _, id := range []string{"job-running-1", "job-dispatching-2"} {
		got, err := persistence.Get[*wrapperspb.StringValue](s2, persistence.JobKey(id))
		if err != nil {
			t.Errorf("CrashRecovery Get job %q: %v", id, err)
			continue
		}
		if got.Value != id {
			t.Errorf("CrashRecovery job value = %q, want %q", got.Value, id)
		}
	}

	// List scan — this is the exact call the scheduler makes on startup to find
	// non-terminal jobs.
	nodes, err := persistence.List[*wrapperspb.StringValue](s2, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatalf("CrashRecovery List nodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("CrashRecovery List nodes: got %d, want 3", len(nodes))
	}

	jobs, err := persistence.List[*wrapperspb.StringValue](s2, []byte(persistence.PrefixJobs))
	if err != nil {
		t.Fatalf("CrashRecovery List jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("CrashRecovery List jobs: got %d, want 2", len(jobs))
	}
}
