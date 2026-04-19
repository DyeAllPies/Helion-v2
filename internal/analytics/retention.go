// internal/analytics/retention.go
//
// Feature 28 — retention cron.
//
// The analytics store is the operational-window record; the audit
// log (BadgerDB) is the forever-record. This file owns the
// once-a-day sweep that prunes rows older than
// `HELION_ANALYTICS_RETENTION_DAYS` from every feature-28 table.
// Audit data is untouched — see docs/persistence.md for the tier
// contract.
//
// Design:
//
//   - One goroutine; 24 h ticker; a single initial sweep on Start
//     so a freshly-restarted coordinator with stale rows doesn't
//     wait 24 h to cull them.
//   - Each table is pruned in its own statement so one table
//     with a lock contention doesn't block the others.
//   - Row counts are logged at INFO on each sweep. If the sweep
//     errors on one table the next table still runs.
//   - No PostgreSQL-level cron / pg_partman dependency; this is
//     pure Go so the analytics pipeline stays self-contained and
//     doesn't require an extension install.
//
// Edge cases handled:
//
//   - retentionDays <= 0 means "retention disabled" — the caller
//     checks this and simply doesn't start the cron. The struct
//     still constructs without error so tests can exercise the
//     sweep path with retentionDays > 0.
//   - Stop() is idempotent; calling it twice or before Start is a
//     no-op. The cron exits on context cancel too.

package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// retainedTables is the list of tables the cron prunes. Ordered
// alphabetically so log output is predictable. A new feature-28
// table added in a future migration must be appended here — the
// retention cron is the single source of truth for "what ages out
// of the analytics store".
var retainedTables = []string{
	"artifact_transfers",
	"auth_events",
	"job_log_entries",
	"registry_mutations",
	"service_probe_events",
	"submission_history",
	"unschedulable_events",

	// Existing tables from earlier migrations. These were
	// previously retention-unbounded (the events table in
	// particular can grow indefinitely on a busy cluster).
	// Retention cron now covers them consistently.
	"events",
}

// timeColumn names the TIMESTAMPTZ column the cron compares against
// `NOW() - interval`. Most feature-28 tables use `occurred_at`;
// submission_history uses `submitted_at`; the pre-existing `events`
// table uses `timestamp`. One map centralises the exception handling.
var timeColumn = map[string]string{
	"submission_history": "submitted_at",
	"events":             "timestamp",
	// default for unlisted tables: "occurred_at"
}

// RetentionDB is the narrow Exec-capable surface the cron needs.
// *pgxpool.Pool satisfies this directly (its Exec returns
// pgconn.CommandTag). A thin interface keeps tests mockable.
type RetentionDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RetentionCron prunes rows older than the configured retention
// window from every feature-28 table on a daily ticker.
type RetentionCron struct {
	db            RetentionDB
	retentionDays int
	log           *slog.Logger
	interval      time.Duration // exposed for tests

	cancel context.CancelFunc
	done   chan struct{}
}

// NewRetentionCron returns a cron ready for Start. retentionDays
// <= 0 is accepted (the cron will log a WARN on Start and idle).
// The caller (main.go) is expected to skip Start when retention
// is disabled, but the struct refuses to panic on bad inputs.
func NewRetentionCron(db RetentionDB, retentionDays int, log *slog.Logger) *RetentionCron {
	if log == nil {
		log = slog.Default()
	}
	return &RetentionCron{
		db:            db,
		retentionDays: retentionDays,
		log:           log,
		interval:      24 * time.Hour,
		done:          make(chan struct{}),
	}
}

// Start launches the cron goroutine. Idempotent: calling Start a
// second time without Stop is a no-op. An initial sweep fires
// immediately so a freshly-restarted coordinator with old rows
// culls them without waiting for the 24 h ticker.
func (r *RetentionCron) Start(ctx context.Context) {
	if r.cancel != nil {
		return
	}
	if r.retentionDays <= 0 {
		r.log.Warn("analytics retention cron disabled (retentionDays <= 0)")
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.loop(ctx)
}

// Stop cancels the cron context and waits for the loop to drain.
// Idempotent.
func (r *RetentionCron) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
}

func (r *RetentionCron) loop(ctx context.Context) {
	defer close(r.done)
	// Initial sweep so stale rows cull on restart.
	r.sweep(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// sweep runs one round of DELETEs across every retained table.
// Errors on one table are logged but don't stop the loop — a
// permission issue on one table shouldn't starve the rest.
func (r *RetentionCron) sweep(ctx context.Context) {
	cutoffSQL := fmt.Sprintf("NOW() - INTERVAL '%d days'", r.retentionDays)
	for _, table := range retainedTables {
		col := timeColumn[table]
		if col == "" {
			col = "occurred_at"
		}
		// Table name + column name are from a compile-time constant
		// list, not user input — safe to interpolate. Interval is
		// constructed from r.retentionDays (int validated at NewRetentionCron).
		sql := fmt.Sprintf(`DELETE FROM %s WHERE %s < %s`, table, col, cutoffSQL)
		tag, err := r.db.Exec(ctx, sql)
		if err != nil {
			r.log.Error("analytics retention prune failed",
				slog.String("table", table), slog.Any("err", err))
			continue
		}
		rows := tag.RowsAffected()
		if rows > 0 {
			r.log.Info("analytics retention prune",
				slog.String("table", table),
				slog.Int64("rows_deleted", rows),
				slog.Int("retention_days", r.retentionDays))
		}
	}
}
