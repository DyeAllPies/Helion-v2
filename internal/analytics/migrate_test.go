// internal/analytics/migrate_test.go
//
// Tests for Migrate and Rollback using mock database connections.

package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Migrate ──────────────────────────────────────────────────────────────

func TestMigrate_AppliesAllMigrations(t *testing.T) {
	mc := newMockConn()
	// No applied versions → all migrations are pending.
	count, err := Migrate(context.Background(), mc, testLogger())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	migrations, _ := loadMigrations()
	if count != len(migrations) {
		t.Errorf("applied %d, want %d", count, len(migrations))
	}
}

func TestMigrate_SkipsAlreadyApplied(t *testing.T) {
	mc := newMockConn()
	migrations, _ := loadMigrations()

	// Return all versions as already applied.
	allVersions := make([]int, len(migrations))
	for i, m := range migrations {
		allVersions[i] = m.Version
	}
	mc.queryFn = func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
		if strings.Contains(sql, "schema_migrations") {
			return &versionRows{versions: allVersions}, nil
		}
		return &emptyRows{}, nil
	}

	count, err := Migrate(context.Background(), mc, testLogger())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if count != 0 {
		t.Errorf("applied %d, want 0 (all already applied)", count)
	}
}

func TestMigrate_EnsureTrackingTableError(t *testing.T) {
	mc := newMockConn()
	mc.execErr = fmt.Errorf("permission denied")

	_, err := Migrate(context.Background(), mc, testLogger())
	if err == nil {
		t.Fatal("expected error from ensureTrackingTable")
	}
	if !strings.Contains(err.Error(), "ensure tracking table") {
		t.Errorf("error = %q, want 'ensure tracking table'", err)
	}
}

func TestMigrate_BeginTxError(t *testing.T) {
	mc := newMockConn()
	mc.beginErr = fmt.Errorf("too many connections")

	_, err := Migrate(context.Background(), mc, testLogger())
	if err == nil {
		t.Fatal("expected error from Begin")
	}
	if !strings.Contains(err.Error(), "begin tx") {
		t.Errorf("error = %q, want 'begin tx'", err)
	}
}

func TestMigrate_ExecError_RollsBack(t *testing.T) {
	mc := newMockConn()
	mc.tx.execErr = fmt.Errorf("syntax error")

	_, err := Migrate(context.Background(), mc, testLogger())
	if err == nil {
		t.Fatal("expected error from migration exec")
	}
	if !mc.tx.rolledBack {
		t.Error("transaction should have been rolled back")
	}
}

// ── Rollback ─────────────────────────────────────────────────────────────

func TestRollback_NoMigrations_ReturnsZero(t *testing.T) {
	mc := newMockConn()
	// No applied versions.
	version, err := Rollback(context.Background(), mc, testLogger())
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if version != 0 {
		t.Errorf("version = %d, want 0", version)
	}
}

func TestRollback_RollsBackHighestVersion(t *testing.T) {
	mc := newMockConn()
	migrations, _ := loadMigrations()

	// Simulate all versions applied.
	allVersions := make([]int, len(migrations))
	for i, m := range migrations {
		allVersions[i] = m.Version
	}
	mc.queryFn = func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
		if strings.Contains(sql, "schema_migrations") {
			return &versionRows{versions: allVersions}, nil
		}
		return &emptyRows{}, nil
	}

	version, err := Rollback(context.Background(), mc, testLogger())
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Should roll back the highest version.
	maxV := 0
	for _, v := range allVersions {
		if v > maxV {
			maxV = v
		}
	}
	if version != maxV {
		t.Errorf("rolled back version %d, want %d", version, maxV)
	}
	if !mc.tx.committed {
		t.Error("rollback transaction should have been committed")
	}
}

func TestRollback_ExecError_RollsBack(t *testing.T) {
	mc := newMockConn()
	mc.queryFn = func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
		if strings.Contains(sql, "schema_migrations") {
			return &versionRows{versions: []int{1}}, nil
		}
		return &emptyRows{}, nil
	}
	mc.tx.execErr = fmt.Errorf("drop failed")

	_, err := Rollback(context.Background(), mc, testLogger())
	if err == nil {
		t.Fatal("expected error from rollback exec")
	}
	if !mc.tx.rolledBack {
		t.Error("transaction should have been rolled back")
	}
}

// ── ensureTrackingTable ──────────────────────────────────────────────────

func TestEnsureTrackingTable_ExecsCREATE(t *testing.T) {
	mc := newMockConn()
	if err := ensureTrackingTable(context.Background(), mc); err != nil {
		t.Fatalf("ensureTrackingTable: %v", err)
	}
	if len(mc.execs) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(mc.execs))
	}
	if !strings.Contains(mc.execs[0].SQL, "CREATE TABLE IF NOT EXISTS schema_migrations") {
		t.Error("should create schema_migrations table")
	}
}

// ── appliedVersions ──────────────────────────────────────────────────────

func TestAppliedVersions_ReturnsMap(t *testing.T) {
	mc := newMockConn()
	mc.queryFn = func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return &versionRows{versions: []int{1, 3}}, nil
	}

	got, err := appliedVersions(context.Background(), mc)
	if err != nil {
		t.Fatalf("appliedVersions: %v", err)
	}
	if !got[1] || !got[3] {
		t.Errorf("expected versions 1 and 3, got %v", got)
	}
	if got[2] {
		t.Error("version 2 should not be present")
	}
}

func TestAppliedVersions_QueryError(t *testing.T) {
	mc := newMockConn()
	mc.queryFn = func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return nil, fmt.Errorf("query failed")
	}

	_, err := appliedVersions(context.Background(), mc)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ── Backfill with mock DB ────────────────────────────────────────────────

func TestBackfill_WithMockDB_InsertsEvents(t *testing.T) {
	mc := newMockConn()
	ts := time.Now()
	scanner := &mockAuditScanner{
		entries: [][]byte{
			mustJSON(t, auditEvent{ID: "e1", Timestamp: ts, Type: "job_submit", Actor: "api",
				Details: map[string]interface{}{"job_id": "j1", "command": "echo"}}),
			mustJSON(t, auditEvent{ID: "e2", Timestamp: ts, Type: "node_register", Actor: "n1",
				Details: map[string]interface{}{"node_id": "n1", "address": "10.0.0.1"}}),
		},
	}

	n, err := Backfill(context.Background(), scanner, mc, testLogger())
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if n != 2 {
		t.Errorf("inserted = %d, want 2", n)
	}
	if !mc.tx.committed {
		t.Error("backfill should commit the transaction")
	}
}

func TestBackfill_WithMalformedEntries_SkipsAndContinues(t *testing.T) {
	mc := newMockConn()
	ts := time.Now()
	scanner := &mockAuditScanner{
		entries: [][]byte{
			[]byte("not json"),
			mustJSON(t, auditEvent{ID: "good", Timestamp: ts, Type: "job_submit", Actor: "api",
				Details: map[string]interface{}{"job_id": "j1"}}),
		},
	}

	n, err := Backfill(context.Background(), scanner, mc, testLogger())
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1 (malformed skipped)", n)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
