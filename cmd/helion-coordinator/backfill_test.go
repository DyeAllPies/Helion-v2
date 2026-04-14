// cmd/helion-coordinator/backfill_test.go
//
// Tests for parseBackfillFlags — the pure flag-parsing + env-fallback
// logic of the `analytics backfill` subcommand. The I/O portions of
// runAnalyticsBackfill (BadgerDB open, pgxpool.New, migrate, backfill)
// are exercised by the E2E test in dashboard/e2e/specs/analytics.spec.ts
// and the package-level TestBackfill_* unit tests in internal/analytics.

package main

import (
	"errors"
	"testing"
)

func TestParseBackfillFlags_MissingDSN_Errors(t *testing.T) {
	t.Setenv("HELION_ANALYTICS_DSN", "")
	t.Setenv("HELION_DB_PATH", "")

	_, err := parseBackfillFlags([]string{})
	if err == nil {
		t.Fatal("expected error when DSN is missing")
	}
	if !errors.Is(err, errNoDSN) {
		t.Errorf("error = %v, want errNoDSN", err)
	}
}

func TestParseBackfillFlags_FlagProvidesDSN(t *testing.T) {
	t.Setenv("HELION_ANALYTICS_DSN", "")
	cfg, err := parseBackfillFlags([]string{
		"--pg-dsn=postgres://u:p@host/db?sslmode=disable",
		"--db-path=/tmp/helion",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DSN != "postgres://u:p@host/db?sslmode=disable" {
		t.Errorf("DSN = %q", cfg.DSN)
	}
	if cfg.DBPath != "/tmp/helion" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
}

func TestParseBackfillFlags_EnvFallbackForDSN(t *testing.T) {
	t.Setenv("HELION_ANALYTICS_DSN", "postgres://env/db")
	t.Setenv("HELION_DB_PATH", "/var/helion/db")

	cfg, err := parseBackfillFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DSN != "postgres://env/db" {
		t.Errorf("DSN = %q, want env value", cfg.DSN)
	}
	if cfg.DBPath != "/var/helion/db" {
		t.Errorf("DBPath = %q, want env value", cfg.DBPath)
	}
}

func TestParseBackfillFlags_FlagOverridesEnv(t *testing.T) {
	t.Setenv("HELION_ANALYTICS_DSN", "postgres://env/db")
	t.Setenv("HELION_DB_PATH", "/var/helion/db")

	cfg, err := parseBackfillFlags([]string{
		"--pg-dsn=postgres://flag/db",
		"--db-path=/tmp/flag-path",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DSN != "postgres://flag/db" {
		t.Errorf("DSN = %q, want flag value (flag should override env)", cfg.DSN)
	}
	if cfg.DBPath != "/tmp/flag-path" {
		t.Errorf("DBPath = %q, want flag value", cfg.DBPath)
	}
}

func TestParseBackfillFlags_DefaultDBPath(t *testing.T) {
	t.Setenv("HELION_ANALYTICS_DSN", "postgres://x/y")
	t.Setenv("HELION_DB_PATH", "") // unset → use compiled-in default

	cfg, err := parseBackfillFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DBPath != "/var/lib/helion/db" {
		t.Errorf("DBPath = %q, want default /var/lib/helion/db", cfg.DBPath)
	}
}

func TestParseBackfillFlags_UnknownFlag_Errors(t *testing.T) {
	t.Setenv("HELION_ANALYTICS_DSN", "postgres://x/y")

	_, err := parseBackfillFlags([]string{"--not-a-real-flag"})
	if err == nil {
		t.Fatal("expected error on unknown flag")
	}
	// Using ContinueOnError mode — the error must be propagated, not
	// os.Exit'd. The wrapping happens inside parseBackfillFlags.
	if errors.Is(err, errNoDSN) {
		t.Error("should not be errNoDSN — the DSN is set, the issue is the unknown flag")
	}
}

// ── isBackfillSubcommand ────────────────────────────────────────────────

func TestIsBackfillSubcommand(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{"exact match, no extra args",
			[]string{"helion-coordinator", "analytics", "backfill"}, true},
		{"with flags after",
			[]string{"helion-coordinator", "analytics", "backfill", "--pg-dsn=x"}, true},
		{"empty",
			[]string{}, false},
		{"just binary",
			[]string{"helion-coordinator"}, false},
		{"analytics without backfill",
			[]string{"helion-coordinator", "analytics"}, false},
		{"wrong first subcommand",
			[]string{"helion-coordinator", "serve", "backfill"}, false},
		{"wrong second subcommand",
			[]string{"helion-coordinator", "analytics", "export"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBackfillSubcommand(tt.argv); got != tt.want {
				t.Errorf("isBackfillSubcommand(%v) = %v, want %v", tt.argv, got, tt.want)
			}
		})
	}
}
