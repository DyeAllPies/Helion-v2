// internal/analytics/mock_db_test.go
//
// Mock implementations of dbConn, migrationConn, and pgx.Tx for testing
// the flush, migration, and backfill paths without a real PostgreSQL.

package analytics

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ── mockConn ─────────────────────────────────────────────────────────────

// mockConn implements both dbConn and migrationConn.
type mockConn struct {
	mu       sync.Mutex
	beginErr error
	execErr  error
	queryFn  func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	tx       *mockTx
	execs    []execCall
}

type execCall struct {
	SQL  string
	Args []any
}

func newMockConn() *mockConn {
	mc := &mockConn{}
	mc.tx = &mockTx{conn: mc}
	return mc
}

func (m *mockConn) Begin(_ context.Context) (pgx.Tx, error) {
	if m.beginErr != nil {
		return nil, m.beginErr
	}
	m.tx.committed.Store(false)
	m.tx.rolledBack.Store(false)
	return m.tx, nil
}

func (m *mockConn) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	m.execs = append(m.execs, execCall{SQL: sql, Args: args})
	m.mu.Unlock()
	if m.execErr != nil {
		return pgconn.NewCommandTag(""), m.execErr
	}
	return pgconn.NewCommandTag("OK 1"), nil
}

func (m *mockConn) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, sql, args...)
	}
	return &emptyRows{}, nil
}

// ── mockTx ───────────────────────────────────────────────────────────────

// mockTx implements pgx.Tx by recording Exec calls and commit/rollback state.
//
// All fields touched by the tx methods (Commit, Rollback, Exec) are accessed
// from both the main test goroutine (assertions) and the Sink's background
// flush goroutine, so they need synchronisation. We use atomic.Bool for the
// simple flags and a mutex for the exec-call slice. Tests must use the
// accessor methods (Committed(), RolledBack(), ExecCalls(), ExecCall(i))
// rather than reading the fields directly — direct access is a data race.
type mockTx struct {
	conn    *mockConn
	execErr atomic.Value // nil or error, injected Exec failure

	committed  atomic.Bool
	rolledBack atomic.Bool

	execMu    sync.Mutex
	execCalls []execCall
}

// Committed reports whether Commit was called since the last reset (Begin).
func (t *mockTx) Committed() bool { return t.committed.Load() }

// RolledBack reports whether Rollback was called since the last reset.
func (t *mockTx) RolledBack() bool { return t.rolledBack.Load() }

// ExecCalls returns a defensive copy of the recorded Exec calls.
func (t *mockTx) ExecCalls() []execCall {
	t.execMu.Lock()
	defer t.execMu.Unlock()
	out := make([]execCall, len(t.execCalls))
	copy(out, t.execCalls)
	return out
}

// ExecCall returns the i-th recorded Exec call.
func (t *mockTx) ExecCall(i int) execCall {
	t.execMu.Lock()
	defer t.execMu.Unlock()
	return t.execCalls[i]
}

// ExecCallCount returns how many Exec calls have been recorded.
func (t *mockTx) ExecCallCount() int {
	t.execMu.Lock()
	defer t.execMu.Unlock()
	return len(t.execCalls)
}

// setExecErr injects an error for future Exec calls. Nil to clear.
func (t *mockTx) setExecErr(err error) {
	if err == nil {
		t.execErr.Store((*mockErr)(nil))
		return
	}
	t.execErr.Store(&mockErr{err: err})
}

// mockErr boxes an error so atomic.Value's strict-type rule is satisfied
// (atomic.Value panics if you call Store with differently-typed values).
type mockErr struct{ err error }

func (t *mockTx) Begin(_ context.Context) (pgx.Tx, error) {
	return t, nil
}

func (t *mockTx) Commit(_ context.Context) error {
	t.committed.Store(true)
	return nil
}

func (t *mockTx) Rollback(_ context.Context) error {
	t.rolledBack.Store(true)
	return nil
}

func (t *mockTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.execMu.Lock()
	t.execCalls = append(t.execCalls, execCall{SQL: sql, Args: args})
	t.execMu.Unlock()
	if boxed, ok := t.execErr.Load().(*mockErr); ok && boxed != nil && boxed.err != nil {
		return pgconn.NewCommandTag(""), boxed.err
	}
	return pgconn.NewCommandTag("OK 1"), nil
}

func (t *mockTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return &emptyRows{}, nil
}

func (t *mockTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &emptyRow{}
}

func (t *mockTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, nil
}

func (t *mockTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	return nil
}

func (t *mockTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (t *mockTx) Prepare(_ context.Context, _ string, _ string) (*pgconn.StatementDescription, error) {
	return nil, nil
}

func (t *mockTx) Conn() *pgx.Conn {
	return nil
}

// ── emptyRows ────────────────────────────────────────────────────────────

// emptyRows implements pgx.Rows returning zero rows.
type emptyRows struct{}

func (r *emptyRows) Close()                                       {}
func (r *emptyRows) Err() error                                   { return nil }
func (r *emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("SELECT 0") }
func (r *emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *emptyRows) Next() bool                                   { return false }
func (r *emptyRows) Scan(_ ...any) error                          { return nil }
func (r *emptyRows) Values() ([]any, error)                       { return nil, nil }
func (r *emptyRows) RawValues() [][]byte                          { return nil }
func (r *emptyRows) Conn() *pgx.Conn                              { return nil }

// ── versionRows ──────────────────────────────────────────────────────────

// versionRows implements pgx.Rows returning a list of integer versions,
// used to mock the appliedVersions query.
type versionRows struct {
	versions []int
	idx      int
	closed   bool
}

func (r *versionRows) Close()                                       { r.closed = true }
func (r *versionRows) Err() error                                   { return nil }
func (r *versionRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("SELECT") }
func (r *versionRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *versionRows) Values() ([]any, error)                       { return nil, nil }
func (r *versionRows) RawValues() [][]byte                          { return nil }
func (r *versionRows) Conn() *pgx.Conn                              { return nil }

func (r *versionRows) Next() bool {
	if r.idx < len(r.versions) {
		r.idx++
		return true
	}
	return false
}

func (r *versionRows) Scan(dest ...any) error {
	if p, ok := dest[0].(*int); ok {
		*p = r.versions[r.idx-1]
	}
	return nil
}

// ── emptyRow ─────────────────────────────────────────────────────────────

type emptyRow struct{}

func (r *emptyRow) Scan(_ ...any) error { return nil }
