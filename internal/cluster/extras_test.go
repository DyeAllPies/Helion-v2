// internal/cluster/extras_test.go
//
// Tests for cluster methods that were previously uncovered:
//   - JobStore.GetJobsByStatus, CountByStatus, CountTotal, LoadAllJobs
//   - Registry.CountTotal, CountHealthy, RevokeNode, IsRevoked
//   - policy.Error
package cluster_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// ── JobStore helpers ──────────────────────────────────────────────────────────

func newJobStore(t *testing.T) *cluster.JobStore {
	t.Helper()
	p := cluster.NewMemJobPersister()
	return cluster.NewJobStore(p, nil)
}

func submitJob(t *testing.T, s *cluster.JobStore, id string) {
	t.Helper()
	if err := s.Submit(context.Background(), newJob(id)); err != nil {
		t.Fatalf("Submit %s: %v", id, err)
	}
}

// ── JobStore.GetJobsByStatus ──────────────────────────────────────────────────

func TestGetJobsByStatus_ReturnsMatchingJobs(t *testing.T) {
	s := newJobStore(t)
	submitJob(t, s, "j1")
	submitJob(t, s, "j2")

	jobs, err := s.GetJobsByStatus(context.Background(), "PENDING")
	if err != nil {
		t.Fatalf("GetJobsByStatus: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("want 2 PENDING jobs, got %d", len(jobs))
	}
}

func TestGetJobsByStatus_UnknownStatus_ReturnsError(t *testing.T) {
	s := newJobStore(t)
	_, err := s.GetJobsByStatus(context.Background(), "BOGUS")
	if err == nil {
		t.Error("expected error for unknown status, got nil")
	}
}

func TestGetJobsByStatus_NoMatch_ReturnsEmpty(t *testing.T) {
	s := newJobStore(t)
	submitJob(t, s, "j1")

	jobs, err := s.GetJobsByStatus(context.Background(), "RUNNING")
	if err != nil {
		t.Fatalf("GetJobsByStatus: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("want 0 RUNNING jobs, got %d", len(jobs))
	}
}

func TestGetJobsByStatus_AllStatuses_Accepted(t *testing.T) {
	s := newJobStore(t)
	statuses := []string{"PENDING", "DISPATCHING", "RUNNING", "COMPLETED", "FAILED", "TIMEOUT", "LOST"}
	for _, st := range statuses {
		if _, err := s.GetJobsByStatus(context.Background(), st); err != nil {
			t.Errorf("GetJobsByStatus(%s) returned unexpected error: %v", st, err)
		}
	}
}

// ── JobStore.CountByStatus ────────────────────────────────────────────────────

func TestCountByStatus_CorrectCount(t *testing.T) {
	s := newJobStore(t)
	submitJob(t, s, "j1")
	submitJob(t, s, "j2")
	submitJob(t, s, "j3")

	n, err := s.CountByStatus(context.Background(), "PENDING")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if n != 3 {
		t.Errorf("want 3, got %d", n)
	}
}

func TestCountByStatus_UnknownStatus_ReturnsError(t *testing.T) {
	s := newJobStore(t)
	_, err := s.CountByStatus(context.Background(), "NONEXISTENT")
	if err == nil {
		t.Error("expected error for unknown status")
	}
}

// ── JobStore.CountTotal ───────────────────────────────────────────────────────

func TestCountTotal_CorrectCount(t *testing.T) {
	s := newJobStore(t)
	submitJob(t, s, "j1")
	submitJob(t, s, "j2")

	n, err := s.CountTotal(context.Background())
	if err != nil {
		t.Fatalf("CountTotal: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2, got %d", n)
	}
}

func TestCountTotal_Empty_ReturnsZero(t *testing.T) {
	s := newJobStore(t)
	n, err := s.CountTotal(context.Background())
	if err != nil {
		t.Fatalf("CountTotal: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0, got %d", n)
	}
}

// ── MemJobPersister.LoadAllJobs ───────────────────────────────────────────────

func TestMemJobPersister_LoadAllJobs(t *testing.T) {
	p := cluster.NewMemJobPersister()
	ctx := context.Background()

	job1 := &cpb.Job{ID: "job-1", Command: "ls"}
	job2 := &cpb.Job{ID: "job-2", Command: "echo"}
	_ = p.SaveJob(ctx, job1)
	_ = p.SaveJob(ctx, job2)

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("want 2 jobs, got %d", len(jobs))
	}
}

// ── Registry.CountTotal / CountHealthy ───────────────────────────────────────

func TestRegistry_CountTotal_MatchesLen(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	for _, id := range []string{"n1", "n2", "n3"} {
		_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: id, Address: "127.0.0.1:9090"})
	}

	n, err := r.CountTotal(ctx)
	if err != nil {
		t.Fatalf("CountTotal: %v", err)
	}
	if n != 3 {
		t.Errorf("want 3, got %d", n)
	}
}

func TestRegistry_CountHealthy_CountsRecentHeartbeats(t *testing.T) {
	r := cluster.NewRegistry(cluster.NopPersister{}, 500*time.Millisecond, nil)
	ctx := context.Background()

	// Register two nodes and send heartbeats for both.
	for _, id := range []string{"h1", "h2"} {
		_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: id, Address: "127.0.0.1:9090"})
		_ = r.HandleHeartbeat(ctx, &pb.HeartbeatMessage{NodeId: id})
	}

	n, err := r.CountHealthy(ctx)
	if err != nil {
		t.Fatalf("CountHealthy: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 healthy, got %d", n)
	}
}

// ── Registry.RevokeNode / IsRevoked ──────────────────────────────────────────

func TestRevokeNode_MarksRevoked(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: "bad-node", Address: "127.0.0.1:9090"})

	if err := r.RevokeNode(ctx, "bad-node", "compromised"); err != nil {
		t.Fatalf("RevokeNode: %v", err)
	}

	if !r.IsRevoked("bad-node") {
		t.Error("expected bad-node to be revoked")
	}
}

func TestRevokeNode_RemovesFromActiveNodes(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: "to-revoke", Address: "127.0.0.1:9090"})
	if r.Len() != 1 {
		t.Fatalf("expected 1 node before revocation")
	}

	_ = r.RevokeNode(ctx, "to-revoke", "test")
	if r.Len() != 0 {
		t.Errorf("expected 0 nodes after revocation, got %d", r.Len())
	}
}

func TestIsRevoked_UnknownNode_ReturnsFalse(t *testing.T) {
	r := newRegistry(t)
	if r.IsRevoked("nonexistent") {
		t.Error("non-existent node should not be revoked")
	}
}

// ── ErrNoHealthyNodes.Error ───────────────────────────────────────────────────

// ── MarkLost missing branches ─────────────────────────────────────────────────

func TestMarkLost_NotFound_ReturnsError(t *testing.T) {
	s := newJobStore(t)
	err := s.MarkLost(context.Background(), "nonexistent", "test")
	if err == nil {
		t.Error("expected ErrJobNotFound, got nil")
	}
}

func TestMarkLost_AlreadyTerminal_IsIdempotent(t *testing.T) {
	s := newJobStore(t)
	ctx := context.Background()
	submitJob(t, s, "j-done")
	_ = s.Transition(ctx, "j-done", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "n1"})
	_ = s.Transition(ctx, "j-done", cpb.JobStatusRunning, cluster.TransitionOptions{})
	_ = s.Transition(ctx, "j-done", cpb.JobStatusCompleted, cluster.TransitionOptions{})

	// MarkLost on a completed job should be a no-op (not an error).
	if err := s.MarkLost(ctx, "j-done", "late arrival"); err != nil {
		t.Errorf("MarkLost on terminal job: %v", err)
	}

	// Status should still be COMPLETED.
	j, _ := s.Get("j-done")
	if j.Status != cpb.JobStatusCompleted {
		t.Errorf("want COMPLETED, got %s", j.Status.String())
	}
}

// ── Get missing branch ────────────────────────────────────────────────────────

func TestGet_NotFound_ReturnsError(t *testing.T) {
	s := newJobStore(t)
	_, err := s.Get("no-such-job")
	if err == nil {
		t.Error("expected error for unknown job, got nil")
	}
}

func TestErrNoHealthyNodes_Error_NonEmpty(t *testing.T) {
	msg := cluster.ErrNoHealthyNodes.Error()
	if msg == "" {
		t.Error("ErrNoHealthyNodes.Error() should return a non-empty message")
	}
}

func TestRevokeNode_DoesNotAffectOtherNodes(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	for _, id := range []string{"keep", "revoke-me"} {
		_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: id, Address: "127.0.0.1:9090"})
	}

	_ = r.RevokeNode(ctx, "revoke-me", "test")

	if r.IsRevoked("keep") {
		t.Error("revoking one node should not affect others")
	}
}

// ── Submit persist-failure path ───────────────────────────────────────────────

// failingSaveJobPersister wraps MemJobPersister but always fails SaveJob.
type failingSaveJobPersister struct {
	inner *cluster.MemJobPersister
}

// failOnNthSaveJobPersister fails SaveJob on the Nth call; all others succeed.
type failOnNthSaveJobPersister struct {
	mu       sync.Mutex
	inner    *cluster.MemJobPersister
	calls    int
	failOn   int
}

func (p *failOnNthSaveJobPersister) SaveJob(ctx context.Context, j *cpb.Job) error {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == p.failOn {
		return errors.New("simulated disk full")
	}
	return p.inner.SaveJob(ctx, j)
}
func (p *failOnNthSaveJobPersister) LoadAllJobs(ctx context.Context) ([]*cpb.Job, error) {
	return p.inner.LoadAllJobs(ctx)
}
func (p *failOnNthSaveJobPersister) AppendAudit(ctx context.Context, eventType, actor, target, detail string) error {
	return p.inner.AppendAudit(ctx, eventType, actor, target, detail)
}

func (p *failingSaveJobPersister) SaveJob(_ context.Context, _ *cpb.Job) error {
	return errors.New("disk full")
}
func (p *failingSaveJobPersister) LoadAllJobs(ctx context.Context) ([]*cpb.Job, error) {
	return p.inner.LoadAllJobs(ctx)
}
func (p *failingSaveJobPersister) AppendAudit(ctx context.Context, eventType, actor, target, detail string) error {
	return p.inner.AppendAudit(ctx, eventType, actor, target, detail)
}

func TestSubmit_PersistFails_ReturnsError(t *testing.T) {
	p := &failingSaveJobPersister{inner: cluster.NewMemJobPersister()}
	s := cluster.NewJobStore(p, nil)

	err := s.Submit(context.Background(), &cpb.Job{ID: "j-fail", Command: "ls"})
	if err == nil {
		t.Error("expected error when persist fails, got nil")
	}

	// The job must not remain in the store after a failed Submit.
	_, getErr := s.Get("j-fail")
	if getErr == nil {
		t.Error("expected job to be absent after failed Submit, but Get succeeded")
	}
}

// ── Restore LoadAllJobs failure ───────────────────────────────────────────────

// failOnLoadAllJobsPersister wraps MemJobPersister but fails LoadAllJobs.
type failOnLoadAllJobsPersister struct {
	inner *cluster.MemJobPersister
}

func (p *failOnLoadAllJobsPersister) SaveJob(ctx context.Context, j *cpb.Job) error {
	return p.inner.SaveJob(ctx, j)
}
func (p *failOnLoadAllJobsPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error) {
	return nil, errors.New("storage unavailable")
}
func (p *failOnLoadAllJobsPersister) AppendAudit(ctx context.Context, eventType, actor, target, detail string) error {
	return p.inner.AppendAudit(ctx, eventType, actor, target, detail)
}

// ── MarkLost persist-failure path ────────────────────────────────────────────

func TestMarkLost_PersistFails_ReturnsError(t *testing.T) {
	// failOn=2: Submit succeeds (save #1), MarkLost fails (save #2).
	p := &failOnNthSaveJobPersister{inner: cluster.NewMemJobPersister(), failOn: 2}
	s := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	if err := s.Submit(ctx, &cpb.Job{ID: "ml-fail", Command: "ls"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := s.MarkLost(ctx, "ml-fail", "test"); err == nil {
		t.Error("expected error when MarkLost persist fails, got nil")
	}
}

// ── Restore LoadAllJobs failure ───────────────────────────────────────────────

func TestRestore_LoadFails_ReturnsError(t *testing.T) {
	p := &failOnLoadAllJobsPersister{inner: cluster.NewMemJobPersister()}
	s := cluster.NewJobStore(p, nil)

	err := s.Restore(context.Background())
	if err == nil {
		t.Error("expected error when LoadAllJobs fails, got nil")
	}
}

// ── resetToPending persist-error path via RecoveryManager ────────────────────

// TestResetToPending_PersistFails_RecoveryReturnsError exercises the
// error return inside resetToPending when SaveJob fails during recovery.
//
// SaveJob call order for this test:
//  1. Submit(PENDING)           → Save #1 — succeeds
//  2. Transition→DISPATCHING    → Save #2 — succeeds
//  3. Transition→RUNNING        → Save #3 — succeeds
//  4. resetToPending(PENDING)   → Save #4 — FAILS → resetToPending returns error
func TestResetToPending_PersistFails_RecoveryReturnsError(t *testing.T) {
	p := &failOnNthSaveJobPersister{inner: cluster.NewMemJobPersister(), failOn: 4}
	s := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	if err := s.Submit(ctx, &cpb.Job{ID: "rtp-j1", Command: "ls"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := s.Transition(ctx, "rtp-j1", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "n1"}); err != nil {
		t.Fatalf("Transition→DISPATCHING: %v", err)
	}
	if err := s.Transition(ctx, "rtp-j1", cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
		t.Fatalf("Transition→RUNNING: %v", err)
	}

	// Recovery manager finds a RUNNING job and calls resetToPending (Save #4 → fails).
	// RecoveryManager logs the error and continues; Run itself returns nil.
	// The best-effort rollback in resetToPending sets the status back to DISPATCHING.
	d := &mockDispatcher{nodeID: "n2"}
	rm := cluster.NewRecoveryManager(s, d, time.Millisecond, nil)

	// Run should not return an error (recovery handles per-job errors internally).
	if err := rm.Run(ctx); err != nil {
		t.Fatalf("RecoveryManager.Run: %v", err)
	}

	// Verify the persister hit exactly 4 calls (Submit, →DISPATCH, →RUNNING, resetToPending).
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	if calls != 4 {
		t.Errorf("expected 4 SaveJob calls, got %d", calls)
	}
}

// ── Registry.Close ───────────────────────────────────────────────────────────

func TestRegistry_Close_ReturnsWithoutBlocking(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	// Register a node (triggers background persist via auditWG).
	_, _ = r.Register(ctx, &pb.RegisterRequest{NodeId: "close-n1", Address: "127.0.0.1:9090"})

	start := time.Now()
	r.Close(1 * time.Second)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("Close blocked too long: %v", elapsed)
	}
}

func TestRegistry_Close_NoNodes_ReturnsImmediately(t *testing.T) {
	r := newRegistry(t)
	start := time.Now()
	r.Close(100 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Close with no nodes blocked too long: %v", elapsed)
	}
}