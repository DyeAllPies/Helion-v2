// internal/analytics/migrations_test.go

package analytics

import (
	"testing"
)

// TestLoadMigrations verifies that the embedded SQL files parse into the
// expected number of ordered migrations, each with non-empty up/down SQL.
func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	if len(migrations) == 0 {
		t.Fatal("expected at least one migration, got 0")
	}

	// Verify ordering and completeness.
	for i, m := range migrations {
		if m.Version <= 0 {
			t.Errorf("migration %d: version must be positive, got %d", i, m.Version)
		}
		if i > 0 && migrations[i-1].Version >= m.Version {
			t.Errorf("migration %d: version %d is not greater than previous %d",
				i, m.Version, migrations[i-1].Version)
		}
		if m.Name == "" {
			t.Errorf("migration %d (version %d): empty name", i, m.Version)
		}
		if m.UpSQL == "" {
			t.Errorf("migration %d (%s): empty up SQL", m.Version, m.Name)
		}
		if m.DownSQL == "" {
			t.Errorf("migration %d (%s): empty down SQL", m.Version, m.Name)
		}
	}
}

// TestLoadMigrations_ExpectedVersions checks the specific migrations we ship.
func TestLoadMigrations_ExpectedVersions(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	expected := []struct {
		version int
		name    string
	}{
		{1, "001_create_events"},
		{2, "002_create_job_summary"},
		{3, "003_create_node_summary"},
		{4, "004_create_views"},
		{5, "005_unified_sink"},
		{6, "006_workflow_outcomes"},
		{7, "007_workflow_outcomes_duration"},
	}

	if len(migrations) != len(expected) {
		t.Fatalf("expected %d migrations, got %d", len(expected), len(migrations))
	}

	for i, want := range expected {
		got := migrations[i]
		if got.Version != want.version {
			t.Errorf("migration %d: version = %d, want %d", i, got.Version, want.version)
		}
		if got.Name != want.name {
			t.Errorf("migration %d: name = %q, want %q", i, got.Name, want.name)
		}
	}
}

// TestLoadMigrations_UpSQLContainsExpectedStatements spot-checks that the
// embedded SQL contains the expected CREATE statements.
func TestLoadMigrations_UpSQLContainsExpectedStatements(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	checks := []struct {
		version  int
		contains string
	}{
		{1, "CREATE TABLE IF NOT EXISTS events"},
		{2, "CREATE TABLE IF NOT EXISTS job_summary"},
		{3, "CREATE TABLE IF NOT EXISTS node_summary"},
		{4, "CREATE OR REPLACE VIEW v_hourly_throughput"},
	}

	byVersion := make(map[int]Migration)
	for _, m := range migrations {
		byVersion[m.Version] = m
	}

	for _, c := range checks {
		m, ok := byVersion[c.version]
		if !ok {
			t.Errorf("migration version %d not found", c.version)
			continue
		}
		if !containsString(m.UpSQL, c.contains) {
			t.Errorf("migration %d (%s): up SQL does not contain %q", m.Version, m.Name, c.contains)
		}
	}
}

// TestLoadMigrations_DownSQLContainsDrop verifies each down migration drops
// what the up migration created.
func TestLoadMigrations_DownSQLContainsDrop(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	for _, m := range migrations {
		if !containsString(m.DownSQL, "DROP") {
			t.Errorf("migration %d (%s): down SQL does not contain DROP", m.Version, m.Name)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && searchString(haystack, needle)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
