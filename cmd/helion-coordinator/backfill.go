// cmd/helion-coordinator/backfill.go
//
// One-shot "analytics backfill" subcommand.
//
// Usage:
//   helion-coordinator analytics backfill [--pg-dsn=<DSN>] [--db-path=<path>]
//
// Reads the existing BadgerDB audit trail and inserts historical events into
// the analytics PostgreSQL database.  Idempotent — safe to run multiple times.
//
// Flags can be supplied on the command line or via env vars:
//   --pg-dsn   or HELION_ANALYTICS_DSN
//   --db-path  or HELION_DB_PATH

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/analytics"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runAnalyticsBackfill(log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("analytics backfill", flag.ExitOnError)
	dsn := fs.String("pg-dsn", os.Getenv("HELION_ANALYTICS_DSN"),
		"PostgreSQL connection string (or set HELION_ANALYTICS_DSN)")
	dbPath := fs.String("db-path", envOr("HELION_DB_PATH", "/var/lib/helion/db"),
		"Path to the BadgerDB directory (or set HELION_DB_PATH)")
	if err := fs.Parse(args); err != nil {
		log.Error("parse flags", slog.Any("err", err))
		os.Exit(1)
	}

	if *dsn == "" {
		log.Error("analytics backfill: --pg-dsn or HELION_ANALYTICS_DSN is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// ── Open BadgerDB read-only ──────────────────────────────────────────
	// Read-only + BypassLockGuard lets us scan the audit trail while the
	// coordinator has the DB open for writes. Backfill only reads — any
	// accidental write would fail with a BadgerDB error.
	log.Info("analytics backfill: opening BadgerDB (read-only)", slog.String("path", *dbPath))
	persister, err := cluster.NewBadgerJSONPersisterReadOnly(*dbPath)
	if err != nil {
		log.Error("analytics backfill: open BadgerDB", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() {
		if err := persister.Close(); err != nil {
			log.Error("analytics backfill: close BadgerDB", slog.Any("err", err))
		}
	}()

	// ── Connect to PostgreSQL ────────────────────────────────────────────
	log.Info("analytics backfill: connecting to PostgreSQL")
	conn, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Error("analytics backfill: connect PostgreSQL", slog.Any("err", err))
		os.Exit(1)
	}
	defer conn.Close()

	// ── Run migrations (idempotent, ensures schema exists) ───────────────
	applied, err := analytics.Migrate(ctx, conn, log)
	if err != nil {
		log.Error("analytics backfill: migrations failed", slog.Any("err", err))
		os.Exit(1)
	}
	if applied > 0 {
		log.Info("analytics backfill: migrations applied", slog.Int("count", applied))
	}

	// ── Run backfill ─────────────────────────────────────────────────────
	n, err := analytics.Backfill(ctx, persister, conn, log)
	if err != nil {
		log.Error("analytics backfill: failed",
			slog.Int("inserted_before_failure", n), slog.Any("err", err))
		os.Exit(1)
	}

	log.Info("analytics backfill: complete", slog.Int("events_inserted", n))
}
