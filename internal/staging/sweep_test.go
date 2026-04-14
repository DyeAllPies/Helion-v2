package staging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func testCtx() context.Context { return context.Background() }
func testEmptyJob(id string) *cpb.Job {
	return &cpb.Job{ID: id}
}

// newSweepStager returns a Stager whose workRoot is a dedicated temp
// directory, ready for sweep tests to plant stale directories in.
func newSweepStager(t *testing.T) (*Stager, string) {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), "store")
	store, err := artifacts.NewLocalStore(storeDir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	workRoot := filepath.Join(t.TempDir(), "work")
	return NewStager(store, workRoot, false, slog.Default()), workRoot
}

func mkStaleDir(t *testing.T, root, name string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	return path
}

func TestSweepStaleWorkdirs_RemovesOldDirs(t *testing.T) {
	s, workRoot := newSweepStager(t)
	// MkdirAll creates workRoot through NewStager's first Prepare,
	// but the sweep runs at startup *before* any Prepare — so we
	// must create it manually here.
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll workRoot: %v", err)
	}

	old := mkStaleDir(t, workRoot, "stale-job", time.Now().Add(-2*time.Hour))
	fresh := mkStaleDir(t, workRoot, "fresh-job", time.Now())

	removed, err := s.SweepStaleWorkdirs(time.Hour)
	if err != nil {
		t.Fatalf("SweepStaleWorkdirs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed count: %d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stale workdir survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh workdir removed by mistake: %v", err)
	}
}

func TestSweepStaleWorkdirs_NonExistentRootIsNoOp(t *testing.T) {
	s, workRoot := newSweepStager(t)
	_ = os.RemoveAll(workRoot) // workRoot wasn't created yet

	removed, err := s.SweepStaleWorkdirs(time.Hour)
	if err != nil {
		t.Fatalf("unexpected error on missing root: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed: %d, want 0", removed)
	}
}

func TestSweepStaleWorkdirs_EmptyRootIsNoOp(t *testing.T) {
	s, workRoot := newSweepStager(t)
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	removed, err := s.SweepStaleWorkdirs(time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("empty-root sweep: removed=%d err=%v", removed, err)
	}
}

func TestSweepStaleWorkdirs_ZeroMaxAgeUsesDefault(t *testing.T) {
	s, workRoot := newSweepStager(t)
	_ = os.MkdirAll(workRoot, 0o700)
	// Fresh dir — at default age (1 hour) should NOT be removed.
	fresh := mkStaleDir(t, workRoot, "new-job", time.Now())
	removed, _ := s.SweepStaleWorkdirs(0)
	if removed != 0 {
		t.Fatalf("fresh dir removed under default age: removed=%d", removed)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh dir gone: %v", err)
	}
}

func TestSweepStaleWorkdirs_DoesNotTouchLiveStager(t *testing.T) {
	// Live Prepare creates a workdir with fresh mtime. A sweep with
	// a long maxAge must leave it alone — proves the sweep is safe
	// to run concurrent with live traffic on the same root.
	s, _ := newSweepStager(t)
	p, err := s.Prepare(testCtx(), testEmptyJob("live-job"))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer p.Cleanup()

	removed, err := s.SweepStaleWorkdirs(time.Hour)
	if err != nil {
		t.Fatalf("SweepStaleWorkdirs: %v", err)
	}
	if removed != 0 {
		t.Fatalf("live workdir got reaped: removed=%d", removed)
	}
	if _, err := os.Stat(p.WorkingDir); err != nil {
		t.Fatalf("live workdir gone: %v", err)
	}
}
