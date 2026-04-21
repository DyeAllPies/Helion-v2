// internal/analytics/flush_test.go
//
// Tests for the PostgreSQL-hitting paths: flush, insertEvents,
// upsertSummaries, and all individual upsert methods.

package analytics

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// ── flush ────────────────────────────────────────────────────────────────

func TestFlush_CommitsTransaction(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}

	batch := []events.Event{events.JobSubmitted("j1", "echo", 50)}
	if err := s.flush(context.Background(), batch); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if !mc.tx.Committed() {
		t.Error("transaction was not committed")
	}
}

func TestFlush_BeginError_Returns(t *testing.T) {
	mc := newMockConn()
	mc.beginErr = fmt.Errorf("pg down")
	s := &Sink{conn: mc, log: testLogger()}

	err := s.flush(context.Background(), []events.Event{events.JobSubmitted("j1", "echo", 50)})
	if err == nil {
		t.Fatal("expected error from Begin failure")
	}
	if !strings.Contains(err.Error(), "begin tx") {
		t.Errorf("error = %q, want to contain 'begin tx'", err)
	}
}

func TestFlush_InsertError_RollsBack(t *testing.T) {
	mc := newMockConn()
	mc.tx.setExecErr(fmt.Errorf("insert failed"))
	s := &Sink{conn: mc, log: testLogger()}

	err := s.flush(context.Background(), []events.Event{events.JobSubmitted("j1", "echo", 50)})
	if err == nil {
		t.Fatal("expected error from insert failure")
	}
	if !mc.tx.RolledBack() {
		t.Error("transaction was not rolled back on insert error")
	}
}

func TestFlush_EmptyBatch_Commits(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}

	if err := s.flush(context.Background(), nil); err != nil {
		t.Fatalf("flush empty: %v", err)
	}
	// Even an empty batch opens+commits a transaction (insert is a no-op,
	// upsertSummaries loops over nothing).
	if !mc.tx.Committed() {
		t.Error("empty batch should still commit the transaction")
	}
}

// ── insertEvents ─────────────────────────────────────────────────────────

func TestInsertEvents_BuildsMultiRowSQL(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}

	batch := []events.Event{
		events.JobSubmitted("j1", "echo", 50),
		events.NodeRegistered("n1", "10.0.0.1:8080"),
	}

	tx := mc.tx
	if err := s.insertEvents(context.Background(), tx, batch); err != nil {
		t.Fatalf("insertEvents: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec call, got %d", tx.ExecCallCount())
	}

	sql := tx.ExecCall(0).SQL
	if !strings.Contains(sql, "INSERT INTO events") {
		t.Error("SQL should contain INSERT INTO events")
	}
	if !strings.Contains(sql, "ON CONFLICT (id) DO NOTHING") {
		t.Error("SQL should contain ON CONFLICT clause")
	}
	// 2 rows × 7 params = 14 args
	if len(tx.ExecCall(0).Args) != 14 {
		t.Errorf("expected 14 args, got %d", len(tx.ExecCall(0).Args))
	}
}

func TestInsertEvents_EmptyBatch_NoExec(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	if err := s.insertEvents(context.Background(), tx, nil); err != nil {
		t.Fatalf("insertEvents empty: %v", err)
	}
	if tx.ExecCallCount() != 0 {
		t.Error("empty batch should not exec")
	}
}

// ── upsertSummaries ──────────────────────────────────────────────────────

func TestUpsertSummaries_RoutesAllEventTypes(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	batch := []events.Event{
		events.JobSubmitted("j1", "echo", 50),
		events.JobTransition("j1", "pending", "running", "n1"),
		events.JobCompleted("j1", "n1", 1500),
		events.JobFailed("j2", "err", 1, 2),
		events.JobRetrying("j3", 3, time.Now().Add(time.Minute)),
		events.NodeRegistered("n1", "10.0.0.1:8080"),
		events.NodeStale("n2"),
		events.NodeRevoked("n3", "bad cert"),
		events.WorkflowCompleted("w1"),
		events.WorkflowFailed("w2", "step3"),
	}

	if err := s.upsertSummaries(context.Background(), tx, batch); err != nil {
		t.Fatalf("upsertSummaries: %v", err)
	}

	// job.submitted, job.transition(running), job.completed + node incr,
	// job.failed (no node_id in standard constructor → no node incr),
	// job.retrying, node.registered, node.stale, node.revoked,
	// plus feature-40: workflow.completed + workflow.failed each
	// INSERT into workflow_outcomes = 11 total.
	if tx.ExecCallCount() != 11 {
		t.Errorf("expected 11 exec calls, got %d", tx.ExecCallCount())
		for i, c := range tx.ExecCalls() {
			t.Logf("  [%d] %s", i, c.SQL[:min(80, len(c.SQL))])
		}
	}
}

// ── Individual upsert methods ────────────────────────────────────────────

func TestUpsertJobSubmitted_InsertsWithCorrectArgs(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.JobSubmitted("j1", "echo hello", 75)
	if err := s.upsertJobSubmitted(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertJobSubmitted: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(0).SQL, "job_summary") {
		t.Error("should target job_summary table")
	}
	// args: job_id, command, priority, timestamp
	if tx.ExecCall(0).Args[0] != "j1" {
		t.Errorf("job_id = %v, want j1", tx.ExecCall(0).Args[0])
	}
}

func TestUpsertJobSubmitted_EmptyJobID_Skips(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.NewEvent("job.submitted", map[string]any{})
	if err := s.upsertJobSubmitted(context.Background(), tx, evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ExecCallCount() != 0 {
		t.Error("should skip when job_id is empty")
	}
}

func TestUpsertJobTransition_Running_SetsStartedAt(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.JobTransition("j1", "dispatching", "running", "n1")
	if err := s.upsertJobTransition(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertJobTransition: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(0).SQL, "started_at") {
		t.Error("running transition should SET started_at")
	}
}

func TestUpsertJobTransition_NonRunning(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.JobTransition("j1", "pending", "scheduled", "")
	if err := s.upsertJobTransition(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertJobTransition: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
	if strings.Contains(tx.ExecCall(0).SQL, "started_at") {
		t.Error("non-running transition should NOT set started_at")
	}
}

func TestUpsertJobCompleted_IncrementsNodeCounter(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.JobCompleted("j1", "n1", 1500)
	if err := s.upsertJobCompleted(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertJobCompleted: %v", err)
	}

	// Should have 2 exec calls: job_summary upsert + node_summary increment.
	if tx.ExecCallCount() != 2 {
		t.Fatalf("expected 2 exec calls, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(1).SQL, "node_summary") {
		t.Error("second exec should update node_summary")
	}
	if !strings.Contains(tx.ExecCall(1).SQL, "jobs_completed") {
		t.Error("should increment jobs_completed")
	}
}

func TestUpsertJobFailed_IncrementsNodeCounter(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	// JobFailed doesn't include node_id in the standard constructor,
	// so use NewEvent directly to include it.
	evt := events.NewEvent("job.failed", map[string]any{
		"job_id":    "j1",
		"error":     "OOM",
		"exit_code": int32(137),
		"attempt":   uint32(2),
		"node_id":   "n1",
	})
	if err := s.upsertJobFailed(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertJobFailed: %v", err)
	}

	if tx.ExecCallCount() != 2 {
		t.Fatalf("expected 2 exec calls, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(1).SQL, "jobs_failed") {
		t.Error("should increment jobs_failed on node_summary")
	}
}

func TestUpsertJobRetrying(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.JobRetrying("j1", 3, time.Now())
	if err := s.upsertJobRetrying(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertJobRetrying: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
}

func TestUpsertNodeRegistered(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.NodeRegistered("n1", "10.0.0.1:8080")
	if err := s.upsertNodeRegistered(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertNodeRegistered: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(0).SQL, "node_summary") {
		t.Error("should target node_summary")
	}
}

func TestUpsertNodeStale(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.NodeStale("n1")
	if err := s.upsertNodeStale(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertNodeStale: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(0).SQL, "times_stale") {
		t.Error("should increment times_stale")
	}
}

func TestUpsertNodeRevoked(t *testing.T) {
	mc := newMockConn()
	s := &Sink{conn: mc, log: testLogger()}
	tx := mc.tx

	evt := events.NodeRevoked("n1", "expired")
	if err := s.upsertNodeRevoked(context.Background(), tx, evt); err != nil {
		t.Fatalf("upsertNodeRevoked: %v", err)
	}

	if tx.ExecCallCount() != 1 {
		t.Fatalf("expected 1 exec, got %d", tx.ExecCallCount())
	}
	if !strings.Contains(tx.ExecCall(0).SQL, "times_revoked") {
		t.Error("should increment times_revoked")
	}
}

// ── Sink flush integration via loop ──────────────────────────────────────

func TestSink_BatchFlush_TriggersOnBatchSize(t *testing.T) {
	mc := newMockConn()
	bus := events.NewBus(256, nil)
	s := NewSink(mc, bus, SinkConfig{
		BatchSize:     2,
		FlushInterval: 10 * time.Second, // large so only batch triggers
		BufferLimit:   100,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	// Publish exactly BatchSize events.
	bus.Publish(events.JobSubmitted("j1", "echo", 50))
	bus.Publish(events.JobSubmitted("j2", "echo", 50))

	// Give the loop time to flush.
	time.Sleep(300 * time.Millisecond)

	// The tx should have been committed (flush happened).
	if !mc.tx.Committed() {
		t.Error("expected flush to commit after batch size reached")
	}

	cancel()
	s.Stop()
}

func TestSink_TimedFlush_TriggersOnInterval(t *testing.T) {
	mc := newMockConn()
	bus := events.NewBus(256, nil)
	s := NewSink(mc, bus, SinkConfig{
		BatchSize:     1000, // large so only timer triggers
		FlushInterval: 100 * time.Millisecond,
		BufferLimit:   100,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	bus.Publish(events.JobSubmitted("j1", "echo", 50))

	// Wait for timed flush.
	time.Sleep(400 * time.Millisecond)

	if !mc.tx.Committed() {
		t.Error("expected timed flush to commit")
	}

	cancel()
	s.Stop()
}

func TestSink_Stop_FlushesRemaining(t *testing.T) {
	mc := newMockConn()
	bus := events.NewBus(256, nil)
	s := NewSink(mc, bus, SinkConfig{
		BatchSize:     1000,
		FlushInterval: 10 * time.Second,
		BufferLimit:   100,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	bus.Publish(events.NodeRegistered("n1", "10.0.0.1"))
	time.Sleep(100 * time.Millisecond)

	cancel()
	s.Stop()

	if !mc.tx.Committed() {
		t.Error("Stop() should flush remaining events")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
