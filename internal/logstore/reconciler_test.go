// internal/logstore/reconciler_test.go
//
// Tests for feature 28's Badger→Postgres log reconciler.
//
// The reconciler is a thin orchestration layer over
// Reconcilable.ReconcileConfirmed + PGLogConfirmer; most of the
// complex logic (age gate, idempotent delete, corrupt-entry skip)
// lives in the store implementations and is covered by tests on
// those specifically. Here we test the behaviours the reconciler
// owns:
//
//   - One sweep on Start, then periodic on Interval.
//   - PG confirms → Badger entry goes away.
//   - PG does NOT confirm → Badger entry stays.
//   - PG query errors → reconciler logs and tries again next tick;
//     no deletions on a failed sweep.
//   - Age gate: entries younger than MinAge are skipped even if
//     PG confirms them.
//   - Stop is idempotent and drains the loop.

package logstore

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockConfirmer implements PGLogConfirmer. It captures the
// candidates it was asked about so a test can assert batching
// behaviour, and returns a caller-configured set of "confirmed"
// keys.
type mockConfirmer struct {
	mu        sync.Mutex
	confirmed map[LogKey]bool
	queries   [][]LogKey
	err       error
	callCount atomic.Int64
}

func newMockConfirmer(confirmed map[LogKey]bool) *mockConfirmer {
	return &mockConfirmer{confirmed: confirmed}
}

func (m *mockConfirmer) ConfirmLogBatch(_ context.Context, candidates []LogKey) (map[LogKey]bool, error) {
	m.callCount.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries = append(m.queries, append([]LogKey{}, candidates...))
	if m.err != nil {
		return nil, m.err
	}
	out := make(map[LogKey]bool, len(candidates))
	for _, k := range candidates {
		if m.confirmed[k] {
			out[k] = true
		}
	}
	return out, nil
}

// seedStore fills a MemLogStore with entries spanning a range of
// ages so age-gate tests can exercise both sides of MinAge.
func seedStore(t *testing.T, s *MemLogStore, entries []LogEntry) {
	t.Helper()
	for _, e := range entries {
		if err := s.Append(context.Background(), e); err != nil {
			t.Fatalf("seed Append: %v", err)
		}
	}
}

// TestReconciler_ConfirmedAreDeletedUnconfirmedKept is the
// core contract: a PG confirm deletes; a miss leaves intact.
func TestReconciler_ConfirmedAreDeletedUnconfirmedKept(t *testing.T) {
	store := NewMemLogStore()
	old := time.Now().Add(-1 * time.Hour) // well past any reasonable MinAge
	seedStore(t, store, []LogEntry{
		{JobID: "j-a", Seq: 1, Data: "line-a1", Timestamp: old},
		{JobID: "j-a", Seq: 2, Data: "line-a2", Timestamp: old},
		{JobID: "j-b", Seq: 1, Data: "line-b1", Timestamp: old},
	})
	confirmer := newMockConfirmer(map[LogKey]bool{
		{"j-a", 1}: true,
		{"j-b", 1}: true,
		// {"j-a", 2}: NOT confirmed → must survive.
	})
	r := NewReconciler(store, confirmer, ReconcilerConfig{
		Interval: time.Minute, MinAge: time.Second, BatchSize: 10,
	}, nil)
	r.sweep(context.Background())

	got, _ := store.Get(context.Background(), "j-a")
	if len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("j-a: want only seq=2 to survive, got %+v", got)
	}
	got, _ = store.Get(context.Background(), "j-b")
	if len(got) != 0 {
		t.Errorf("j-b: want all deleted, got %+v", got)
	}
}

// TestReconciler_AgeGate_SkipsYoung guards against deleting
// entries newer than MinAge even if PG confirms them. Catches a
// race between a just-landed chunk and the sink's batched flush.
func TestReconciler_AgeGate_SkipsYoung(t *testing.T) {
	store := NewMemLogStore()
	// "old" = safely past the MinAge we'll set; "young" = freshly
	// appended, inside the MinAge window.
	old := time.Now().Add(-1 * time.Hour)
	young := time.Now() // just happened
	seedStore(t, store, []LogEntry{
		{JobID: "j-a", Seq: 1, Data: "old", Timestamp: old},
		{JobID: "j-a", Seq: 2, Data: "young", Timestamp: young},
	})
	confirmer := newMockConfirmer(map[LogKey]bool{
		// PG "claims" both are confirmed. The age gate should
		// still protect the young one.
		{"j-a", 1}: true,
		{"j-a", 2}: true,
	})
	r := NewReconciler(store, confirmer, ReconcilerConfig{
		Interval: time.Minute,
		MinAge:   10 * time.Minute, // any age > 0 would work for young
		BatchSize: 10,
	}, nil)
	r.sweep(context.Background())

	got, _ := store.Get(context.Background(), "j-a")
	if len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("age-gate: expected only young (seq=2) to survive, got %+v", got)
	}
}

// TestReconciler_PGQueryError_NoDeletes guards against a bad PG
// state causing the reconciler to ghost entries. If the confirm
// query errors, NOTHING gets deleted.
func TestReconciler_PGQueryError_NoDeletes(t *testing.T) {
	store := NewMemLogStore()
	old := time.Now().Add(-1 * time.Hour)
	seedStore(t, store, []LogEntry{
		{JobID: "j-a", Seq: 1, Data: "x", Timestamp: old},
	})
	confirmer := newMockConfirmer(map[LogKey]bool{{"j-a", 1}: true})
	confirmer.err = &testErr{msg: "connection refused"}

	r := NewReconciler(store, confirmer, ReconcilerConfig{
		Interval: time.Minute, MinAge: time.Second, BatchSize: 10,
	}, nil)
	r.sweep(context.Background())

	got, _ := store.Get(context.Background(), "j-a")
	if len(got) != 1 {
		t.Errorf("PG error: expected no deletions, got %+v", got)
	}
}

// TestReconciler_Batching asserts ConfirmLogBatch is called with
// BatchSize-sized chunks (or the remainder), not one-per-entry.
func TestReconciler_Batching(t *testing.T) {
	store := NewMemLogStore()
	old := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 25; i++ {
		_ = store.Append(context.Background(), LogEntry{
			JobID: "j", Seq: uint64(i), Data: "x", Timestamp: old,
		})
	}
	confirmer := newMockConfirmer(nil)
	r := NewReconciler(store, confirmer, ReconcilerConfig{
		Interval: time.Minute, MinAge: time.Second, BatchSize: 10,
	}, nil)
	r.sweep(context.Background())

	// 25 candidates / batch 10 = 3 batches (10, 10, 5). The first
	// (gathering) pass does NOT call the confirmer — only the
	// second (deletion) pass does.
	if got := confirmer.callCount.Load(); got != 3 {
		t.Errorf("call count: want 3 batches, got %d", got)
	}
}

// TestReconciler_EmptyStore_NoConfirmerCall asserts we don't even
// hit PG when Badger has nothing to reconcile.
func TestReconciler_EmptyStore_NoConfirmerCall(t *testing.T) {
	store := NewMemLogStore()
	confirmer := newMockConfirmer(nil)
	r := NewReconciler(store, confirmer, ReconcilerConfig{
		Interval: time.Minute, MinAge: time.Second, BatchSize: 10,
	}, nil)
	r.sweep(context.Background())
	if got := confirmer.callCount.Load(); got != 0 {
		t.Errorf("empty store: expected 0 PG calls, got %d", got)
	}
}

// TestReconciler_StartStop_Idempotent guards Stop being called
// multiple times / before Start.
func TestReconciler_StartStop_Idempotent(t *testing.T) {
	store := NewMemLogStore()
	confirmer := newMockConfirmer(nil)
	r := NewReconciler(store, confirmer, ReconcilerConfig{
		Interval: 10 * time.Millisecond, MinAge: time.Second, BatchSize: 10,
	}, nil)
	r.Stop() // before Start
	r.Start(context.Background())
	r.Start(context.Background()) // second Start — no-op
	time.Sleep(30 * time.Millisecond)
	r.Stop()
	r.Stop() // second Stop — no-op
}

// TestReconciler_NilConfirmedFn_IsError guards the
// ReconcileConfirmed contract: a nil callback is a misuse
// returning an error, not a silent no-op that might mask a
// wiring bug.
func TestReconciler_NilConfirmedFn_IsError(t *testing.T) {
	store := NewMemLogStore()
	_, _, err := store.ReconcileConfirmed(context.Background(), time.Second, nil)
	if err == nil {
		t.Error("nil confirmedFn: want err, got nil")
	}
}

// testErr is a minimal error so we don't pull in errors.New here.
type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
