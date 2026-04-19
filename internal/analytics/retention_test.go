// internal/analytics/retention_test.go
//
// Feature 28 — retention cron tests. Uses a mock RetentionDB that
// captures every Exec call so we can assert the cron ran a DELETE
// against each feature-28 table with the right time-column +
// interval. No live PostgreSQL needed.

package analytics

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// mockRetentionDB captures Exec calls for assertion. Returns a
// fixed affected-row count so the cron logs a prune line.
type mockRetentionDB struct {
	mu    sync.Mutex
	execs []string
	err   error
}

func (m *mockRetentionDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	m.execs = append(m.execs, sql)
	m.mu.Unlock()
	if m.err != nil {
		return pgconn.NewCommandTag(""), m.err
	}
	// NewCommandTag("DELETE 17") → RowsAffected() returns 17.
	return pgconn.NewCommandTag("DELETE 17"), nil
}

func (m *mockRetentionDB) Execs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.execs))
	copy(out, m.execs)
	return out
}

// TestRetentionCron_InitialSweepCoversEveryTable asserts the cron
// fires a DELETE against every table in retainedTables on the
// initial (Start) sweep.
func TestRetentionCron_InitialSweepCoversEveryTable(t *testing.T) {
	db := &mockRetentionDB{}
	cron := NewRetentionCron(db, 30, nil)
	// Use a tiny interval so the loop doesn't do another sweep
	// during the test. We call Stop before the ticker fires again.
	cron.interval = 24 * time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	cron.Start(ctx)
	// Give the goroutine a tick to run the initial sweep.
	time.Sleep(50 * time.Millisecond)
	cancel()
	cron.Stop()

	seen := make(map[string]bool)
	for _, sql := range db.Execs() {
		for _, table := range retainedTables {
			if strings.Contains(sql, "DELETE FROM "+table+" ") {
				seen[table] = true
			}
		}
	}
	for _, table := range retainedTables {
		if !seen[table] {
			t.Errorf("expected DELETE for table %q on initial sweep", table)
		}
	}
}

// TestRetentionCron_UsesCorrectTimeColumn asserts each table's
// DELETE uses the right timestamp column.
func TestRetentionCron_UsesCorrectTimeColumn(t *testing.T) {
	db := &mockRetentionDB{}
	cron := NewRetentionCron(db, 60, nil)
	cron.interval = 24 * time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cron.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	cron.Stop()

	cases := map[string]string{
		"submission_history":    "submitted_at",
		"events":                "timestamp",
		"auth_events":           "occurred_at",
		"job_log_entries":       "occurred_at",
		"artifact_transfers":    "occurred_at",
		"service_probe_events":  "occurred_at",
		"unschedulable_events":  "occurred_at",
		"registry_mutations":    "occurred_at",
	}
	for table, col := range cases {
		found := false
		for _, sql := range db.Execs() {
			if strings.Contains(sql, "DELETE FROM "+table+" WHERE "+col+" < ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("table %q: no DELETE using time column %q", table, col)
		}
	}
}

// TestRetentionCron_DisabledWhenRetentionDaysZero asserts the cron
// is a no-op when retentionDays <= 0 — a safety net for operators
// who want retention disabled (PII compliance pushback, etc.).
func TestRetentionCron_DisabledWhenRetentionDaysZero(t *testing.T) {
	db := &mockRetentionDB{}
	cron := NewRetentionCron(db, 0, nil)
	cron.interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	cron.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	cron.Stop()

	if len(db.Execs()) != 0 {
		t.Errorf("retention disabled: want 0 exec calls, got %d", len(db.Execs()))
	}
}

// TestRetentionCron_StopIsIdempotent asserts Stop can be called
// multiple times (and before Start) without panicking.
func TestRetentionCron_StopIsIdempotent(t *testing.T) {
	db := &mockRetentionDB{}
	cron := NewRetentionCron(db, 10, nil)
	cron.Stop()
	cron.Stop()
	cron.Start(context.Background())
	cron.Stop()
	cron.Stop()
}

// TestRetentionCron_ErrorOnOneTableContinues asserts that a Exec
// error on one table doesn't prevent other tables from being
// pruned — a permission issue on `events` shouldn't starve the
// rest.
func TestRetentionCron_ErrorOnOneTableContinues(t *testing.T) {
	// Simulate errors by overriding Exec to fail only on one table.
	db := &mockRetentionDBFailing{target: "events"}
	cron := NewRetentionCron(db, 30, nil)
	cron.interval = 24 * time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	cron.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	cron.Stop()

	// Every non-`events` table must still have a successful Exec.
	seen := make(map[string]bool)
	for _, sql := range db.execs {
		for _, table := range retainedTables {
			if strings.Contains(sql, "DELETE FROM "+table+" ") {
				seen[table] = true
			}
		}
	}
	for _, table := range retainedTables {
		if !seen[table] {
			t.Errorf("table %q: DELETE should have fired even after earlier error", table)
		}
	}
}

// mockRetentionDBFailing returns an error only for a specific
// table. All others succeed.
type mockRetentionDBFailing struct {
	mu     sync.Mutex
	execs  []string
	target string
}

func (m *mockRetentionDBFailing) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	m.execs = append(m.execs, sql)
	m.mu.Unlock()
	if strings.Contains(sql, "DELETE FROM "+m.target+" ") {
		return pgconn.NewCommandTag(""), testErr{msg: "permission denied"}
	}
	return pgconn.NewCommandTag("DELETE 3"), nil
}

type testErr struct{ msg string }

func (e testErr) Error() string { return e.msg }
