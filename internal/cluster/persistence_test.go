// internal/cluster/persistence_test.go
//
// Tests for BadgerJSONPersister — all methods at 0% coverage.
// Uses t.TempDir() for BadgerDB path so each test gets a clean DB.

package cluster_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// newBadgerPersister opens a BadgerJSONPersister backed by a temp directory.
func newBadgerPersister(t *testing.T) *cluster.BadgerJSONPersister {
	t.Helper()
	p, err := cluster.NewBadgerJSONPersister(t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("NewBadgerJSONPersister: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// ── NewBadgerJSONPersister ────────────────────────────────────────────────────

func TestNewBadgerJSONPersister_ValidPath_ReturnsNonNil(t *testing.T) {
	p, err := cluster.NewBadgerJSONPersister(t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil persister")
	}
	_ = p.Close()
}

// ── Close / Ping ─────────────────────────────────────────────────────────────

func TestBadgerPersister_Close_NoError(t *testing.T) {
	p, err := cluster.NewBadgerJSONPersister(t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestBadgerPersister_Ping_AfterOpen_NoError(t *testing.T) {
	p := newBadgerPersister(t)
	if err := p.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// ── SaveNode / LoadAllNodes ───────────────────────────────────────────────────

func TestBadgerPersister_SaveNode_LoadAllNodes_Roundtrip(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	n := &cpb.Node{
		NodeID:  "node-1",
		Address: "127.0.0.1:9090",
	}
	if err := p.SaveNode(ctx, n); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	nodes, err := p.LoadAllNodes(ctx)
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(nodes))
	}
	if nodes[0].NodeID != "node-1" {
		t.Errorf("want id=node-1, got %q", nodes[0].NodeID)
	}
}

func TestBadgerPersister_LoadAllNodes_Empty_ReturnsEmpty(t *testing.T) {
	p := newBadgerPersister(t)
	nodes, err := p.LoadAllNodes(context.Background())
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("want 0 nodes, got %d", len(nodes))
	}
}

func TestBadgerPersister_SaveNode_MultipleNodes(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	for i, addr := range []string{"127.0.0.1:9001", "127.0.0.1:9002", "127.0.0.1:9003"} {
		_ = p.SaveNode(ctx, &cpb.Node{NodeID: "n" + string(rune('1'+i)), Address: addr})
	}

	nodes, err := p.LoadAllNodes(ctx)
	if err != nil {
		t.Fatalf("LoadAllNodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("want 3 nodes, got %d", len(nodes))
	}
}

// ── SaveJob / LoadAllJobs ─────────────────────────────────────────────────────

func TestBadgerPersister_SaveJob_LoadAllJobs_Roundtrip(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	j := &cpb.Job{ID: "job-1", Command: "echo hello"}
	if err := p.SaveJob(ctx, j); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	if jobs[0].ID != "job-1" {
		t.Errorf("want id=job-1, got %q", jobs[0].ID)
	}
	if jobs[0].Command != "echo hello" {
		t.Errorf("want command='echo hello', got %q", jobs[0].Command)
	}
}

func TestBadgerPersister_LoadAllJobs_Empty_ReturnsEmpty(t *testing.T) {
	p := newBadgerPersister(t)
	jobs, err := p.LoadAllJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("want 0 jobs, got %d", len(jobs))
	}
}

func TestBadgerPersister_SaveJob_MultipleJobs(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	for _, id := range []string{"j1", "j2", "j3"} {
		_ = p.SaveJob(ctx, &cpb.Job{ID: id, Command: "ls"})
	}

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Errorf("want 3 jobs, got %d", len(jobs))
	}
}

func TestBadgerPersister_SaveJob_Overwrite_UpdatesRecord(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	_ = p.SaveJob(ctx, &cpb.Job{ID: "j1", Command: "original"})
	_ = p.SaveJob(ctx, &cpb.Job{ID: "j1", Command: "updated"})

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job after overwrite, got %d", len(jobs))
	}
	if jobs[0].Command != "updated" {
		t.Errorf("want command=updated, got %q", jobs[0].Command)
	}
}

// ── AppendAudit ───────────────────────────────────────────────────────────────

func TestBadgerPersister_AppendAudit_NoError(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	err := p.AppendAudit(ctx, "node_register", "system", "node-1", "node registered")
	if err != nil {
		t.Errorf("AppendAudit: %v", err)
	}
}

func TestBadgerPersister_AppendAudit_MultipleEntries(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	// Use distinct targets so keys (audit/{nano}-{target}) don't collide.
	_ = p.AppendAudit(ctx, "job_submit", "admin", "job-001", "submitted")
	_ = p.AppendAudit(ctx, "job_complete", "system", "job-002", "completed")
	_ = p.AppendAudit(ctx, "node_revoke", "admin", "node-003", "revoked")

	// Verify entries are scannable via the audit/ prefix.
	results, err := p.Scan(ctx, "audit/", 0)
	if err != nil {
		t.Fatalf("Scan audit/: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("want 3 audit entries, got %d", len(results))
	}
}

// ── Get / Put / PutWithTTL / Delete ──────────────────────────────────────────

func TestBadgerPersister_PutGet_Roundtrip(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	if err := p.Put(ctx, "key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := p.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Errorf("want 'value1', got %q", got)
	}
}

func TestBadgerPersister_Get_MissingKey_ReturnsError(t *testing.T) {
	p := newBadgerPersister(t)
	_, err := p.Get(context.Background(), "nonexistent-key")
	if err == nil {
		t.Error("expected error for missing key, got nil")
	}
}

func TestBadgerPersister_PutWithTTL_ValueIsReadable(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	if err := p.PutWithTTL(ctx, "ttl-key", []byte("ttl-value"), 10*time.Minute); err != nil {
		t.Fatalf("PutWithTTL: %v", err)
	}

	got, err := p.Get(ctx, "ttl-key")
	if err != nil {
		t.Fatalf("Get after PutWithTTL: %v", err)
	}
	if string(got) != "ttl-value" {
		t.Errorf("want 'ttl-value', got %q", got)
	}
}

func TestBadgerPersister_Delete_RemovesKey(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	_ = p.Put(ctx, "del-key", []byte("val"))

	if err := p.Delete(ctx, "del-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := p.Get(ctx, "del-key"); err == nil {
		t.Error("expected error after Delete, got nil")
	}
}

func TestBadgerPersister_Delete_NonexistentKey_NoError(t *testing.T) {
	p := newBadgerPersister(t)
	// Badger's Delete on a non-existent key is a no-op, not an error.
	if err := p.Delete(context.Background(), "ghost-key"); err != nil {
		t.Errorf("Delete non-existent: %v", err)
	}
}

// ── Scan ─────────────────────────────────────────────────────────────────────

func TestBadgerPersister_Scan_ReturnsMatchingPrefix(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	_ = p.Put(ctx, "ns/a", []byte("va"))
	_ = p.Put(ctx, "ns/b", []byte("vb"))
	_ = p.Put(ctx, "other/c", []byte("vc"))

	results, err := p.Scan(ctx, "ns/", 0)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("want 2 results, got %d", len(results))
	}
}

func TestBadgerPersister_Scan_WithLimit_Truncates(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = p.Put(ctx, "item/"+string(rune('a'+i)), []byte("v"))
	}

	results, err := p.Scan(ctx, "item/", 3)
	if err != nil {
		t.Fatalf("Scan with limit: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("want 3 results (limited), got %d", len(results))
	}
}

func TestBadgerPersister_Scan_NoMatch_ReturnsEmpty(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	_ = p.Put(ctx, "other/key", []byte("v"))

	results, err := p.Scan(ctx, "missing/", 0)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

// ── NopPersister job methods ──────────────────────────────────────────────────

func TestNopPersister_SaveJob_NoError(t *testing.T) {
	p := cluster.NopPersister{}
	err := p.SaveJob(context.Background(), &cpb.Job{ID: "j1", Command: "ls"})
	if err != nil {
		t.Errorf("NopPersister.SaveJob: %v", err)
	}
}

func TestNopPersister_LoadAllJobs_ReturnsNil(t *testing.T) {
	p := cluster.NopPersister{}
	jobs, err := p.LoadAllJobs(context.Background())
	if err != nil {
		t.Errorf("NopPersister.LoadAllJobs: %v", err)
	}
	if jobs != nil {
		t.Errorf("want nil, got %v", jobs)
	}
}

func TestBadgerPersister_Scan_ZeroLimit_ReturnsAll(t *testing.T) {
	p := newBadgerPersister(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = p.Put(ctx, "bulk/"+string(rune('a'+i)), []byte("v"))
	}

	results, err := p.Scan(ctx, "bulk/", 0)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 10 {
		t.Errorf("want 10 results with limit=0, got %d", len(results))
	}
}
