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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/analytics"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backfillConfig is the validated result of parseBackfillFlags.
type backfillConfig struct {
	DSN    string
	DBPath string
}

// errNoDSN is returned when neither --pg-dsn nor HELION_ANALYTICS_DSN is set.
var errNoDSN = errors.New("analytics backfill: --pg-dsn or HELION_ANALYTICS_DSN is required")

// parseBackfillFlags parses argv for the `analytics backfill` subcommand and
// returns a validated config. Separated from runAnalyticsBackfill so the
// flag-parsing + env-fallback logic is unit-testable without needing a real
// BadgerDB or PostgreSQL. Uses ContinueOnError so bad flags return an error
// instead of calling os.Exit.
func parseBackfillFlags(args []string) (*backfillConfig, error) {
	fs := flag.NewFlagSet("analytics backfill", flag.ContinueOnError)
	dsn := fs.String("pg-dsn", os.Getenv("HELION_ANALYTICS_DSN"),
		"PostgreSQL connection string (or set HELION_ANALYTICS_DSN)")
	dbPath := fs.String("db-path", envOr("HELION_DB_PATH", "/var/lib/helion/db"),
		"Path to the BadgerDB directory (or set HELION_DB_PATH)")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}
	if *dsn == "" {
		return nil, errNoDSN
	}
	return &backfillConfig{DSN: *dsn, DBPath: *dbPath}, nil
}

func runAnalyticsBackfill(log *slog.Logger, args []string) {
	cfg, err := parseBackfillFlags(args)
	if err != nil {
		log.Error("analytics backfill: configuration", slog.Any("err", err))
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// ── Open BadgerDB read-only ──────────────────────────────────────────
	// Read-only + BypassLockGuard lets us scan the audit trail while the
	// coordinator has the DB open for writes. Backfill only reads — any
	// accidental write would fail with a BadgerDB error.
	log.Info("analytics backfill: opening BadgerDB (read-only)", slog.String("path", cfg.DBPath))
	persister, err := cluster.NewBadgerJSONPersisterReadOnly(cfg.DBPath)
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
	conn, err := pgxpool.New(ctx, cfg.DSN)
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
