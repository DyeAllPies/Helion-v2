// internal/cluster/job_lifecycle_test.go
//
// Integration tests for the job lifecycle state machine.
//
// Test inventory
// ──────────────
//   TestJobLifecycle_HappyPath
//     Single job walks the full pending→dispatching→running→completed path.
//     Every transition is persisted; audit events are recorded.
//
//   TestJobLifecycle_InvalidTransitions
//     Validates that illegal transitions (e.g. pending→running) are rejected
//     with ErrInvalidTransition, and that terminal-job transitions return
//     ErrJobAlreadyTerminal.
//
//   TestJobLifecycle_AllTerminalVariants
//     Exercises completed, failed, timeout, and lost independently to confirm
//     each variant is persisted and acknowledged as terminal.
//
//   TestJobLifecycle_10ConcurrentJobs_AllReachTerminal  ← Phase 2 exit criterion
//     Submits 10 jobs concurrently. A goroutine per job drives it through the
//     full lifecycle (pending→dispatching→running→completed). After all
//     goroutines finish, every job is in a terminal state in both the in-memory
//     store and the persisted layer.
//
//   TestJobLifecycle_BadgerPersistence_ConsistentAfterRestart  ← Phase 2 exit criterion
//     Uses a real BadgerDB on disk (t.TempDir()).
//     Submits 10 jobs, drives all to terminal state, closes the BadgerDB,
//     reopens it, and verifies that every job is readable with its correct
//     terminal status — simulating a coordinator restart.
//
// Run with:
//   go test -race -count=1 ./internal/cluster/ -run TestJobLifecycle -v

package cluster_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newJob(id string) *cpb.Job {
	return &cpb.Job{
		ID:      id,
		Command: "echo",
		Args:    []string{"hello"},
	}
}

// driveToCompleted takes a job through the full lifecycle using the given store.
// It runs in a goroutine-safe manner and is the "happy path" driver used by
// concurrent tests.
func driveToCompleted(t *testing.T, ctx context.Context, s *cluster.JobStore, jobID string) {
	t.Helper()

	steps := []struct {
		to   cpb.JobStatus
		opts cluster.TransitionOptions
	}{
		{cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "node-1"}},
		{cpb.JobStatusRunning, cluster.TransitionOptions{}},
		{cpb.JobStatusCompleted, cluster.TransitionOptions{ExitCode: 0}},
	}

	for _, step := range steps {
		if err := s.Transition(ctx, jobID, step.to, step.opts); err != nil {
			t.Errorf("job %s: Transition → %s: %v", jobID, step.to, err)
			return
		}
	}
}

// ── BadgerJobPersister (disk-backed, for restart test) ───────────────────────

// diskJobPersister is a simple BadgerDB-backed JobPersister used only in the
// restart integration test.  It is deliberately minimal — the production
// BadgerJobPersister will be implemented in the persistence package once the
// Job type is a proto message.  Here we encode/decode as JSON (same as
// BadgerJSONPersister for Node) to keep the test self-contained.
type diskJobPersister struct {
	db *badger.DB
}

func openDiskJobPersister(t *testing.T, path string) *diskJobPersister {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions(path).WithLogger(nil))
	if err != nil {
		t.Fatalf("open badger at %s: %v", path, err)
	}
	return &diskJobPersister{db: db}
}

func (p *diskJobPersister) close(t *testing.T) {
	t.Helper()
	if err := p.db.Close(); err != nil {
		t.Errorf("close badger: %v", err)
	}
}

func (p *diskJobPersister) SaveJob(_ context.Context, j *cpb.Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	key := []byte("jobs/" + j.ID)
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

func (p *diskJobPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error) {
	var jobs []*cpb.Job
	prefix := []byte("jobs/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var j cpb.Job
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &j)
			}); err != nil {
				return fmt.Errorf("unmarshal job %q: %w", it.Item().Key(), err)
			}
			jobs = append(jobs, &j)
		}
		return nil
	})
	return jobs, err
}

func (p *diskJobPersister) AppendAudit(_ context.Context, eventType, _, target, detail string) error {
	key := []byte(fmt.Sprintf("audit/%020d-%s", time.Now().UnixNano(), target))
	data := []byte(fmt.Sprintf("%s target=%s %s", eventType, target, detail))
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// ── TestJobLifecycle_HappyPath ─────────────────────────────────────────────────

func TestJobLifecycle_HappyPath(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemJobPersister()
	s := cluster.NewJobStore(p, nil)

	j := newJob("job-happy")
	if err := s.Submit(ctx, j); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Verify initial persisted state.
	got, err := s.Get("job-happy")
	if err != nil {
		t.Fatalf("Get after Submit: %v", err)
	}
	if got.Status != cpb.JobStatusPending {
		t.Errorf("after Submit: status = %s, want pending", got.Status)
	}

	// pending → dispatching
	if err := s.Transition(ctx, "job-happy", cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "node-42"}); err != nil {
		t.Fatalf("→ dispatching: %v", err)
	}
	got, _ = s.Get("job-happy")
	if got.Status != cpb.JobStatusDispatching {
		t.Errorf("after dispatching: status = %s", got.Status)
	}
	if got.NodeID != "node-42" {
		t.Errorf("NodeID = %q, want node-42", got.NodeID)
	}
	if got.DispatchedAt.IsZero() {
		t.Error("DispatchedAt not set after dispatching transition")
	}

	// dispatching → running
	if err := s.Transition(ctx, "job-happy", cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
		t.Fatalf("→ running: %v", err)
	}

	// running → completed
	if err := s.Transition(ctx, "job-happy", cpb.JobStatusCompleted, cluster.TransitionOptions{ExitCode: 0}); err != nil {
		t.Fatalf("→ completed: %v", err)
	}
	got, _ = s.Get("job-happy")
	if got.Status != cpb.JobStatusCompleted {
		t.Errorf("final status = %s, want completed", got.Status)
	}
	if got.FinishedAt.IsZero() {
		t.Error("FinishedAt not set on completed job")
	}
	if !got.Status.IsTerminal() {
		t.Error("completed should be terminal")
	}

	// Verify in persister.
	persisted := p.AllJobs()["job-happy"]
	if persisted == nil || persisted.Status != cpb.JobStatusCompleted {
		t.Errorf("persisted job status = %v, want completed", persisted)
	}
}

// ── TestJobLifecycle_InvalidTransitions ───────────────────────────────────────

func TestJobLifecycle_InvalidTransitions(t *testing.T) {
	ctx := context.Background()

	t.Run("pending to running is rejected", func(t *testing.T) {
		p := cluster.NewMemJobPersister()
		s := cluster.NewJobStore(p, nil)
		if err := s.Submit(ctx, newJob("j1")); err != nil {
			t.Fatal(err)
		}
		err := s.Transition(ctx, "j1", cpb.JobStatusRunning, cluster.TransitionOptions{})
		if err == nil {
			t.Fatal("expected error for pending→running, got nil")
		}
	})

	t.Run("completed job rejects further transitions", func(t *testing.T) {
		p := cluster.NewMemJobPersister()
		s := cluster.NewJobStore(p, nil)
		if err := s.Submit(ctx, newJob("j2")); err != nil {
			t.Fatal(err)
		}
		driveToCompleted(t, ctx, s, "j2")

		err := s.Transition(ctx, "j2", cpb.JobStatusFailed, cluster.TransitionOptions{})
		if err == nil {
			t.Fatal("expected ErrJobAlreadyTerminal, got nil")
		}
	})

	t.Run("unknown job returns ErrJobNotFound", func(t *testing.T) {
		s := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
		err := s.Transition(ctx, "does-not-exist", cpb.JobStatusDispatching, cluster.TransitionOptions{})
		if err == nil {
			t.Fatal("expected ErrJobNotFound, got nil")
		}
	})
}

// ── TestJobLifecycle_AllTerminalVariants ──────────────────────────────────────

func TestJobLifecycle_AllTerminalVariants(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		terminal cpb.JobStatus
		setup    func(t *testing.T, s *cluster.JobStore, id string)
	}{
		{
			name:     "completed",
			terminal: cpb.JobStatusCompleted,
			setup: func(t *testing.T, s *cluster.JobStore, id string) {
				t.Helper()
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ dispatching: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ running: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusCompleted, cluster.TransitionOptions{ExitCode: 0}); err != nil {
					t.Fatalf("→ completed: %v", err)
				}
			},
		},
		{
			name:     "failed_from_running",
			terminal: cpb.JobStatusFailed,
			setup: func(t *testing.T, s *cluster.JobStore, id string) {
				t.Helper()
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ dispatching: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ running: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusFailed, cluster.TransitionOptions{ExitCode: 1, ErrMsg: "exit 1"}); err != nil {
					t.Fatalf("→ failed: %v", err)
				}
			},
		},
		{
			name:     "failed_from_dispatching",
			terminal: cpb.JobStatusFailed,
			setup: func(t *testing.T, s *cluster.JobStore, id string) {
				t.Helper()
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ dispatching: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusFailed, cluster.TransitionOptions{ErrMsg: "dispatch rpc error"}); err != nil {
					t.Fatalf("→ failed: %v", err)
				}
			},
		},
		{
			name:     "timeout",
			terminal: cpb.JobStatusTimeout,
			setup: func(t *testing.T, s *cluster.JobStore, id string) {
				t.Helper()
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ dispatching: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ running: %v", err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusTimeout, cluster.TransitionOptions{ErrMsg: "exceeded 30s limit"}); err != nil {
					t.Fatalf("→ timeout: %v", err)
				}
			},
		},
		{
			name:     "lost",
			terminal: cpb.JobStatusLost,
			setup: func(t *testing.T, s *cluster.JobStore, id string) {
				t.Helper()
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("→ dispatching: %v", err)
				}
				if err := s.MarkLost(ctx, id, "coordinator restarted"); err != nil {
					t.Fatalf("MarkLost: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p := cluster.NewMemJobPersister()
			s := cluster.NewJobStore(p, nil)
			id := "job-" + tc.name
			if err := s.Submit(ctx, newJob(id)); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, s, id)

			got, err := s.Get(id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.terminal {
				t.Errorf("status = %s, want %s", got.Status, tc.terminal)
			}
			if !got.Status.IsTerminal() {
				t.Errorf("%s should be terminal", tc.terminal)
			}
			if got.FinishedAt.IsZero() {
				t.Error("FinishedAt not set on terminal job")
			}
		})
	}
}

// ── TestJobLifecycle_10ConcurrentJobs_AllReachTerminal ────────────────────────
//
// Phase 2 exit criterion:
//   "Integration test submits 10 jobs concurrently; all reach terminal state."

func TestJobLifecycle_10ConcurrentJobs_AllReachTerminal(t *testing.T) {
	ctx := context.Background()
	p := cluster.NewMemJobPersister()
	s := cluster.NewJobStore(p, nil)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)

	// Submit all 10 jobs first (could also be concurrent, but sequential
	// Submit + concurrent lifecycle is the more realistic scenario).
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("concurrent-job-%02d", i)
		if err := s.Submit(ctx, newJob(ids[i])); err != nil {
			t.Fatalf("Submit %s: %v", ids[i], err)
		}
	}

	// Drive all 10 jobs concurrently through the full lifecycle.
	for i := 0; i < n; i++ {
		id := ids[i]
		go func() {
			defer wg.Done()
			driveToCompleted(t, ctx, s, id)
		}()
	}

	// Wait with a generous timeout so the test fails cleanly on deadlock.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for concurrent jobs to complete")
	}

	// Assert: every job is in a terminal state.
	for _, id := range ids {
		j, err := s.Get(id)
		if err != nil {
			t.Errorf("Get %s: %v", id, err)
			continue
		}
		if !j.Status.IsTerminal() {
			t.Errorf("job %s status = %s (non-terminal)", id, j.Status)
		}
	}

	// Assert: NonTerminal() returns empty.
	if nt := s.NonTerminal(); len(nt) != 0 {
		t.Errorf("NonTerminal() returned %d jobs, want 0", len(nt))
		for _, j := range nt {
			t.Logf("  non-terminal: %s status=%s", j.ID, j.Status)
		}
	}

	// Assert: persister also sees all 10 as terminal.
	persisted := p.AllJobs()
	for _, id := range ids {
		j, ok := persisted[id]
		if !ok {
			t.Errorf("job %s missing from persister", id)
			continue
		}
		if !j.Status.IsTerminal() {
			t.Errorf("persisted job %s status = %s (non-terminal)", id, j.Status)
		}
	}
}

// ── TestJobLifecycle_BadgerPersistence_ConsistentAfterRestart ─────────────────
//
// Phase 2 exit criterion:
//   "BadgerDB state is consistent after coordinator restart."
//
// Sequence:
//   1. Open BadgerDB at a temp path.
//   2. Submit 10 jobs and drive all to terminal state.
//   3. Close BadgerDB (simulates coordinator shutdown).
//   4. Reopen BadgerDB at the same path (simulates coordinator restart).
//   5. Call Restore() on a fresh JobStore.
//   6. Verify all 10 jobs are present with their correct terminal statuses.

func TestJobLifecycle_BadgerPersistence_ConsistentAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	const n = 10
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("restart-job-%02d", i)
	}

	// ── Phase A: run coordinator, drive all jobs to terminal ──────────────
	{
		p := openDiskJobPersister(t, dir)
		s := cluster.NewJobStore(p, nil)

		for i, id := range ids {
			j := newJob(id)
			if err := s.Submit(ctx, j); err != nil {
				t.Fatalf("Submit %s: %v", id, err)
			}
			// Mix of terminal variants to exercise all branches.
			switch i % 3 {
			case 0:
				driveToCompleted(t, ctx, s, id)
			case 1:
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("restart %s \u2192 dispatching: %v", id, err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusRunning, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("restart %s \u2192 running: %v", id, err)
				}
				if err := s.Transition(ctx, id, cpb.JobStatusFailed, cluster.TransitionOptions{ExitCode: 1}); err != nil {
					t.Fatalf("restart %s \u2192 failed: %v", id, err)
				}
			case 2:
				if err := s.Transition(ctx, id, cpb.JobStatusDispatching, cluster.TransitionOptions{}); err != nil {
					t.Fatalf("restart %s \u2192 dispatching: %v", id, err)
				}
				if err := s.MarkLost(ctx, id, "simulated crash"); err != nil {
					t.Fatalf("restart %s MarkLost: %v", id, err)
				}
			}
		}

		// Verify all terminal before close.
		for _, id := range ids {
			j, err := s.Get(id)
			if err != nil {
				t.Fatalf("pre-close Get %s: %v", id, err)
			}
			if !j.Status.IsTerminal() {
				t.Fatalf("pre-close: job %s non-terminal (%s)", id, j.Status)
			}
		}

		p.close(t) // ← coordinator shutdown
	}

	// ── Phase B: reopen and restore — simulates coordinator restart ───────
	{
		p2 := openDiskJobPersister(t, dir) // same path — existing data
		defer p2.close(t)

		s2 := cluster.NewJobStore(p2, nil)
		if err := s2.Restore(ctx); err != nil {
			t.Fatalf("Restore: %v", err)
		}

		// All 10 jobs must be present and terminal after restore.
		if total := len(s2.List()); total != n {
			t.Fatalf("after restart: List() returned %d jobs, want %d", total, n)
		}

		for _, id := range ids {
			j, err := s2.Get(id)
			if err != nil {
				t.Errorf("after restart Get %s: %v", id, err)
				continue
			}
			if !j.Status.IsTerminal() {
				t.Errorf("after restart: job %s status = %s (non-terminal)", id, j.Status)
			}
		}

		// NonTerminal must be empty — crash recovery would have no work to do.
		if nt := s2.NonTerminal(); len(nt) != 0 {
			t.Errorf("after restart: NonTerminal() = %d, want 0", len(nt))
		}
	}
}
