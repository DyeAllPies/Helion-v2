// internal/staging/sweep.go
//
// Orphaned-workdir sweep. If the node agent dies between Prepare and
// Finalize (OOM, SIGKILL, host reboot), the per-job directory under
// HELION_WORK_ROOT never gets cleaned up. On node startup we walk the
// work root once and remove anything older than a configurable age so
// stale workdirs don't accumulate forever on long-running nodes.
//
// The age threshold is meaningful: a job that's actively being dispatched
// by the coordinator has a workdir whose mtime was touched within the
// last few seconds. A workdir whose tree hasn't been touched in an hour
// is almost certainly from a previous node-agent lifetime. Default
// threshold is 1 hour; operators can tighten via SweepStaleWorkdirs.

package staging

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// DefaultSweepAge is the age threshold for SweepStaleWorkdirs when the
// caller does not supply one. One hour comfortably covers any job that
// could still be considered "in flight" (coordinator's stale-node
// detection and retry paths fire within seconds-to-minutes), while
// still reclaiming the bulk of orphans from a crash hours ago.
const DefaultSweepAge = time.Hour

// SweepStaleWorkdirs removes every direct child of workRoot whose
// mtime is older than maxAge. Returns the number of entries removed
// and the first error encountered (continues on per-entry errors so
// one permission problem can't block the whole sweep).
//
// Safe to run on an empty or non-existent workRoot — returns (0, nil)
// in both cases. Safe to run concurrently with active Stagers on the
// same root: we only touch entries whose mtime predates maxAge, and
// a live Prepare/Finalize cycle keeps mtimes fresh via workdir
// creation and file writes.
func (s *Stager) SweepStaleWorkdirs(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		maxAge = DefaultSweepAge
	}
	entries, err := os.ReadDir(s.workRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)

	removed := 0
	var firstErr error
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			// Lstat race: directory disappeared between ReadDir and
			// Info. Treat as already-cleaned and move on.
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(s.workRoot, e.Name())
		if err := os.RemoveAll(path); err != nil {
			s.log.Warn("sweep: failed to remove stale workdir",
				slog.String("path", path), slog.Any("err", err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
		s.log.Info("sweep: removed stale workdir",
			slog.String("path", path),
			slog.Time("mtime", info.ModTime()))
	}
	return removed, firstErr
}
