// internal/analytics/migrations.go
//
// Embedded SQL migrations and a sequential runner for the analytics
// PostgreSQL schema.
//
// Design
// ──────
// Migrations are embedded at compile time via go:embed so the coordinator
// binary is self-contained — no external files to deploy.  The runner
// creates a `schema_migrations` tracking table and applies each numbered
// migration exactly once, in order.  Rollbacks use the corresponding
// .down.sql files.
//
// The runner is intentionally minimal (no third-party migration library).
// Each migration runs inside its own transaction so a failure leaves the
// database in a known state: all prior migrations applied, the failed one
// rolled back.

package analytics

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migration represents a single numbered migration with up and down SQL.
type Migration struct {
	Version int
	Name    string // e.g. "001_create_events"
	UpSQL   string
	DownSQL string
}

// loadMigrations reads the embedded migrations directory and returns them
// sorted by version number.
func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(embeddedMigrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	// Group by version: version → {up: sql, down: sql, name: ...}
	type pair struct {
		name string
		up   string
		down string
	}
	grouped := make(map[int]*pair)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fname := entry.Name()
		if !strings.HasSuffix(fname, ".sql") {
			continue
		}

		data, err := fs.ReadFile(embeddedMigrations, "migrations/"+fname)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", fname, err)
		}

		// Parse "001_create_events.up.sql" → version=1, direction="up"
		base := strings.TrimSuffix(fname, ".sql")
		var direction string
		if strings.HasSuffix(base, ".up") {
			direction = "up"
			base = strings.TrimSuffix(base, ".up")
		} else if strings.HasSuffix(base, ".down") {
			direction = "down"
			base = strings.TrimSuffix(base, ".down")
		} else {
			continue // skip files that don't match the pattern
		}

		parts := strings.SplitN(base, "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("parse migration version from %s: %w", fname, err)
		}

		p, ok := grouped[version]
		if !ok {
			p = &pair{name: base}
			grouped[version] = p
		}
		switch direction {
		case "up":
			p.up = string(data)
		case "down":
			p.down = string(data)
		}
	}

	migrations := make([]Migration, 0, len(grouped))
	for v, p := range grouped {
		migrations = append(migrations, Migration{
			Version: v,
			Name:    p.name,
			UpSQL:   p.up,
			DownSQL: p.down,
		})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

// migrationConn is the subset of *pgx.Conn needed by the migration runner.
type migrationConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ensureTrackingTable creates the schema_migrations table if it does not
// exist.  This table records which migration versions have been applied.
func ensureTrackingTable(ctx context.Context, conn migrationConn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT         PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	return err
}

// appliedVersions returns the set of migration versions already applied.
func appliedVersions(ctx context.Context, conn migrationConn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan migration version: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// Migrate applies all pending up-migrations in order.  Each migration runs
// in its own transaction.  Returns the number of migrations applied.
func Migrate(ctx context.Context, conn migrationConn, log *slog.Logger) (int, error) {
	if err := ensureTrackingTable(ctx, conn); err != nil {
		return 0, fmt.Errorf("ensure tracking table: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if m.UpSQL == "" {
			return count, fmt.Errorf("migration %d (%s) has no up SQL", m.Version, m.Name)
		}

		log.Info("applying migration",
			slog.Int("version", m.Version),
			slog.String("name", m.Name))

		tx, err := conn.Begin(ctx)
		if err != nil {
			return count, fmt.Errorf("begin tx for migration %d: %w", m.Version, err)
		}

		if _, err := tx.Exec(ctx, m.UpSQL); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("migration %d (%s) failed: %w", m.Version, m.Name, err)
		}

		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			m.Version, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("record migration %d: %w", m.Version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return count, fmt.Errorf("commit migration %d: %w", m.Version, err)
		}

		log.Info("migration applied",
			slog.Int("version", m.Version),
			slog.String("name", m.Name))
		count++
	}

	return count, nil
}

// Rollback reverts the most recently applied migration.  Returns the version
// that was rolled back, or 0 if no migrations were applied.
func Rollback(ctx context.Context, conn migrationConn, log *slog.Logger) (int, error) {
	if err := ensureTrackingTable(ctx, conn); err != nil {
		return 0, fmt.Errorf("ensure tracking table: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return 0, err
	}

	// Find the highest applied version.
	maxVersion := 0
	for v := range applied {
		if v > maxVersion {
			maxVersion = v
		}
	}
	if maxVersion == 0 {
		log.Info("no migrations to roll back")
		return 0, nil
	}

	// Find the corresponding migration.
	var target *Migration
	for i := range migrations {
		if migrations[i].Version == maxVersion {
			target = &migrations[i]
			break
		}
	}
	if target == nil {
		return 0, fmt.Errorf("migration %d not found in embedded files", maxVersion)
	}
	if target.DownSQL == "" {
		return 0, fmt.Errorf("migration %d (%s) has no down SQL", target.Version, target.Name)
	}

	log.Info("rolling back migration",
		slog.Int("version", target.Version),
		slog.String("name", target.Name))

	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx for rollback %d: %w", target.Version, err)
	}

	if _, err := tx.Exec(ctx, target.DownSQL); err != nil {
		_ = tx.Rollback(ctx)
		return 0, fmt.Errorf("rollback %d (%s) failed: %w", target.Version, target.Name, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, target.Version); err != nil {
		_ = tx.Rollback(ctx)
		return 0, fmt.Errorf("remove migration record %d: %w", target.Version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit rollback %d: %w", target.Version, err)
	}

	log.Info("migration rolled back",
		slog.Int("version", target.Version),
		slog.String("name", target.Name))
	return target.Version, nil
}
