// internal/persistence/store_test.go
//
// Test coverage targets (exit criteria from §7 Phase 2):
//
//   ✓ Put + Get round-trip (single key)
//   ✓ Put overwrites an existing key
//   ✓ Get on missing key returns ErrNotFound
//   ✓ Delete removes a key; subsequent Get returns ErrNotFound
//   ✓ Delete on a non-existent key is a no-op (no error)
//   ✓ PutWithTTL: value readable before expiry, ErrNotFound after expiry
//   ✓ List returns all values under a prefix in key order
//   ✓ List on an empty prefix returns an empty slice (not an error)
//   ✓ List does not bleed across prefix boundaries
//   ✓ PutRaw / GetRaw round-trip
//   ✓ GetRaw on missing key returns ErrNotFound
//   ✓ PutRaw and Put coexist without interference
//   ✓ AppendAudit: two events returned in chronological order
//   ✓ AppendAudit: audit keys do not appear in the nodes scan
//   ✓ AuditKey: earlier timestamp sorts before later timestamp (byte order)
//   ✓ CRASH RECOVERY: write N records, Close, reopen, verify all N present
//   ✓ RunGC on an idle store returns no error
//
// Each test gets its own t.TempDir() so there is no shared state between tests.
// The crash-recovery test deliberately creates two independent Store values at
// the same path to prove the on-disk format survives a close/reopen cycle.
//
// Proto types used in tests
// ─────────────────────────
// Until the generated helionpb stubs from Phase 1 are wired into the module,
// the tests use *wrapperspb.StringValue — a real, fully-registered proto.Message
// from google.golang.org/protobuf/types/known/wrapperspb.  It serialises with
// proto.Marshal / proto.Unmarshal exactly as the real helionpb types will.
//
// Migration: replace *wrapperspb.StringValue with *helionpb.Node / *helionpb.Job
// at each call site.  The Store API does not change.

package persistence_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ============================================================================
// Helpers
// ============================================================================

// openFresh opens a Store in a unique temp directory and registers a Cleanup
// that closes it after the test.
func openFresh(t *testing.T) *persistence.Store {
	t.Helper()
	s, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// sv is a shorthand for wrapperspb.String — our stand-in proto value.
func sv(v string) *wrapperspb.StringValue { return wrapperspb.String(v) }

// ============================================================================
// Basic CRUD
// ============================================================================

func TestPutGet(t *testing.T) {
	s := openFresh(t)

	want := sv("10.0.0.1:8080")
	key := persistence.NodeKey(want.Value)

	if err := persistence.Put(s, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !proto.Equal(want, got) {
		t.Errorf("Get = %q, want %q", got.Value, want.Value)
	}
}

func TestPutOverwrite(t *testing.T) {
	s := openFresh(t)
	key := persistence.NodeKey("10.0.0.1:8080")

	if err := persistence.Put(s, key, sv("first")); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := persistence.Put(s, key, sv("second")); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	got, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Value != "second" {
		t.Errorf("Get after overwrite = %q, want %q", got.Value, "second")
	}
}

func TestGetMissingKey(t *testing.T) {
	s := openFresh(t)
	_, err := persistence.Get[*wrapperspb.StringValue](s, persistence.NodeKey("ghost:9999"))
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Get missing key: got %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := openFresh(t)
	key := persistence.NodeKey("10.0.0.2:8080")

	if err := persistence.Put(s, key, sv("alive")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := persistence.Delete(s, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

func TestDeleteNonExistent(t *testing.T) {
	s := openFresh(t)
	if err := persistence.Delete(s, persistence.NodeKey("nobody:0")); err != nil {
		t.Errorf("Delete non-existent key: %v", err)
	}
}

// ============================================================================
// TTL
// ============================================================================

// TestPutWithTTL verifies a value is readable before its TTL elapses and
// returns ErrNotFound afterwards.
//
// BadgerDB's TTL has 1-second resolution.  We set TTL=1s and sleep 2s.
// Skip with -short for fast CI runs.
func TestPutWithTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TTL sleep test in -short mode")
	}

	s := openFresh(t)
	key := persistence.TokenKey("jti-ttl-test")

	if err := persistence.PutWithTTL(s, key, sv("ephemeral"), 1*time.Second); err != nil {
		t.Fatalf("PutWithTTL: %v", err)
	}

	// Readable immediately.
	got, err := persistence.Get[*wrapperspb.StringValue](s, key)
	if err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	if got.Value != "ephemeral" {
		t.Errorf("Get before expiry = %q, want %q", got.Value, "ephemeral")
	}

	time.Sleep(2 * time.Second)

	_, err = persistence.Get[*wrapperspb.StringValue](s, key)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Get after expiry: got %v, want ErrNotFound", err)
	}
}

// ============================================================================
// List (prefix scan)
// ============================================================================

func TestList(t *testing.T) {
	s := openFresh(t)

	addrs := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}
	for _, addr := range addrs {
		if err := persistence.Put(s, persistence.NodeKey(addr), sv(addr)); err != nil {
			t.Fatalf("Put node %q: %v", addr, err)
		}
	}
	// Noise: a job entry must NOT appear in the nodes scan.
	if err := persistence.Put(s, persistence.JobKey("job-001"), sv("job-001")); err != nil {
		t.Fatalf("Put job: %v", err)
	}

	nodes, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != len(addrs) {
		t.Fatalf("List: got %d nodes, want %d", len(nodes), len(addrs))
	}

	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		seen[n.Value] = true
	}
	for _, addr := range addrs {
		if !seen[addr] {
			t.Errorf("List: missing %q", addr)
		}
	}
}

func TestListEmptyPrefix(t *testing.T) {
	s := openFresh(t)
	result, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatalf("List on empty prefix: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("List on empty prefix: got %d results, want 0", len(result))
	}
}

func TestListPrefixIsolation(t *testing.T) {
	s := openFresh(t)

	if err := persistence.Put(s, persistence.NodeKey("n1"), sv("n1")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, persistence.JobKey("j1"), sv("j1")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, persistence.JobKey("j2"), sv("j2")); err != nil {
		t.Fatal(err)
	}

	nodes, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes scan: got %d, want 1", len(nodes))
	}

	jobs, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixJobs))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Errorf("jobs scan: got %d, want 2", len(jobs))
	}
}

// ============================================================================
// Raw bytes (certs/)
// ============================================================================

func TestPutRawGetRaw(t *testing.T) {
	s := openFresh(t)

	der := []byte{0x30, 0x82, 0x01, 0x0A, 0x02, 0x82}
	key := persistence.CertKey("node-001")

	if err := s.PutRaw(key, der); err != nil {
		t.Fatalf("PutRaw: %v", err)
	}
	got, err := s.GetRaw(key)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(got) != string(der) {
		t.Errorf("GetRaw = %x, want %x", got, der)
	}
}

func TestGetRawMissing(t *testing.T) {
	s := openFresh(t)
	_, err := s.GetRaw(persistence.CertKey("nobody"))
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("GetRaw missing key: got %v, want ErrNotFound", err)
	}
}

func TestRawAndProtoCoexist(t *testing.T) {
	s := openFresh(t)

	rawKey := persistence.CertKey("node-raw")
	protoKey := persistence.NodeKey("10.0.0.1:8080")

	if err := s.PutRaw(rawKey, []byte("cert-der")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, protoKey, sv("10.0.0.1:8080")); err != nil {
		t.Fatal(err)
	}

	raw, err := s.GetRaw(rawKey)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(raw) != "cert-der" {
		t.Errorf("GetRaw = %q, want %q", raw, "cert-der")
	}

	got, err := persistence.Get[*wrapperspb.StringValue](s, protoKey)
	if err != nil {
		t.Fatalf("Get proto: %v", err)
	}
	if got.Value != "10.0.0.1:8080" {
		t.Errorf("Get proto = %q, want %q", got.Value, "10.0.0.1:8080")
	}
}

// ============================================================================
// Audit log
// ============================================================================

func TestAppendAuditOrdering(t *testing.T) {
	s := openFresh(t)

	if err := persistence.AppendAudit(s, "evt-1", sv("node.registered")); err != nil {
		t.Fatalf("AppendAudit e1: %v", err)
	}
	// Guarantee distinct nanosecond timestamps.
	time.Sleep(time.Millisecond)
	if err := persistence.AppendAudit(s, "evt-2", sv("job.dispatched")); err != nil {
		t.Fatalf("AppendAudit e2: %v", err)
	}

	events, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixAudit))
	if err != nil {
		t.Fatalf("List audit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("List audit: got %d, want 2", len(events))
	}
	if events[0].Value != "node.registered" {
		t.Errorf("events[0] = %q, want %q", events[0].Value, "node.registered")
	}
	if events[1].Value != "job.dispatched" {
		t.Errorf("events[1] = %q, want %q", events[1].Value, "job.dispatched")
	}
}

func TestAuditKeyOrdering(t *testing.T) {
	earlier := persistence.AuditKey(1_000_000_000, "a")
	later := persistence.AuditKey(2_000_000_000, "b")
	if string(earlier) >= string(later) {
		t.Errorf("AuditKey ordering violated:\n  earlier: %q\n  later:   %q",
			earlier, later)
	}
}

func TestAuditPrefixIsolation(t *testing.T) {
	s := openFresh(t)

	if err := persistence.AppendAudit(s, "evt", sv("event")); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Put(s, persistence.NodeKey("n1"), sv("n1")); err != nil {
		t.Fatal(err)
	}

	nodes, err := persistence.List[*wrapperspb.StringValue](s, []byte(persistence.PrefixNodes))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes after AppendAudit: got %d, want 1", len(nodes))
	}
}

// ============================================================================
// Crash-recovery — primary exit criterion
// ============================================================================

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

		// Close flushes the WAL.  After this point the coordinator could be
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

// ============================================================================
// GC
// ============================================================================

// TestRunGCNoError verifies RunGC on an idle store does not return an error.
// BadgerDB returns ErrNoRewrite when there is nothing to rewrite; the Store
// wrapper translates that to nil.
func TestRunGCNoError(t *testing.T) {
	s := openFresh(t)
	if err := s.RunGC(0.5); err != nil {
		t.Errorf("RunGC: %v", err)
	}
}
