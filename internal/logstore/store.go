// internal/logstore/store.go
//
// Job log storage — persists stdout/stderr from job execution for later
// retrieval via GET /jobs/{id}/logs.
//
// Logs are stored in BadgerDB under log:{job_id}:{seq} keys. Each entry
// is a LogEntry with the raw bytes, a sequence number for ordering, and
// a timestamp. Entries are written during StreamLogs RPC processing and
// read back when the API serves GET /jobs/{id}/logs.
//
// TTL: logs expire after a configurable retention period (default 7 days)
// to prevent unbounded storage growth.

package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// LogEntry is a single chunk of job output (stdout or stderr).
type LogEntry struct {
	JobID     string    `json:"job_id"`
	Seq       uint64    `json:"seq"`
	Data      string    `json:"data"` // base64 or UTF-8 text
	Timestamp time.Time `json:"timestamp"`
}

// Store is the interface for job log persistence.
type Store interface {
	// Append adds a log entry for a job. Entries are ordered by seq.
	Append(ctx context.Context, entry LogEntry) error

	// Get retrieves all log entries for a job, ordered by seq.
	Get(ctx context.Context, jobID string) ([]LogEntry, error)
}

// Reconcilable is the feature-28 extension capability: a store
// that can safely drop entries that have been confirmed durably
// stored elsewhere (PostgreSQL's `job_log_entries` table). Kept
// separate from Store so the production BadgerDB path wires it
// but tests that don't care about reconciliation (e.g.
// `MemLogStore`) aren't forced to implement it.
//
// Split-brain guarantee: the reconciler MUST NOT call this with a
// confirmedFn that returns true for an entry the caller hasn't
// actually observed in the downstream store. A wrongly-reported
// "confirmed" would permanently lose data (Badger is the only
// copy until PG has it).
type Reconcilable interface {
	// ReconcileConfirmed walks every stored entry, calls
	// confirmedFn(jobID, seq) for each, and deletes the Badger
	// entry if BOTH:
	//
	//   (a) confirmedFn returns (true, nil), AND
	//   (b) the entry's Timestamp is at least minAge old.
	//
	// The age check prevents a too-aggressive deletion when a
	// chunk has just arrived and the PG sink hasn't flushed yet;
	// it defaults to the interval the reconciler caller uses, not
	// the Store's business.
	//
	// Returns (deleted, scanned, err). A per-entry delete failure
	// is counted toward scanned but does NOT abort the scan — the
	// next entry still gets its chance. The final err is the first
	// non-nil one encountered, if any.
	ReconcileConfirmed(
		ctx context.Context,
		minAge time.Duration,
		confirmedFn func(jobID string, seq uint64) (bool, error),
	) (deleted, scanned int, err error)
}

// Persistence is the narrow BadgerDB interface the logstore needs.
// Delete is the feature-28 addition — see BadgerLogStore.ReconcileConfirmed.
type Persistence interface {
	Put(ctx context.Context, key string, value []byte) error
	Scan(ctx context.Context, prefix string, limit int) ([][]byte, error)
	Delete(ctx context.Context, key string) error
}

// BadgerLogStore implements Store using BadgerDB.
type BadgerLogStore struct {
	db  Persistence
	ttl time.Duration
}

// NewBadgerLogStore creates a log store backed by BadgerDB.
// retention controls how long logs are kept (default 7 days if 0).
func NewBadgerLogStore(db Persistence, retention time.Duration) *BadgerLogStore {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return &BadgerLogStore{db: db, ttl: retention}
}

// Append writes a log entry to BadgerDB.
func (s *BadgerLogStore) Append(ctx context.Context, entry LogEntry) error {
	key := fmt.Sprintf("log:%s:%010d", entry.JobID, entry.Seq)
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("logstore.Append marshal: %w", err)
	}
	return s.db.Put(ctx, key, data)
}

// Get retrieves all log entries for a job, ordered by seq.
func (s *BadgerLogStore) Get(ctx context.Context, jobID string) ([]LogEntry, error) {
	prefix := fmt.Sprintf("log:%s:", jobID)
	raw, err := s.db.Scan(ctx, prefix, 0)
	if err != nil {
		return nil, fmt.Errorf("logstore.Get scan: %w", err)
	}

	entries := make([]LogEntry, 0, len(raw))
	for _, data := range raw {
		var entry LogEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue // skip corrupt entries
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ReconcileConfirmed implements Reconcilable. See the interface doc
// for the deletion contract. Feature 28: callers wire this to a PG-
// backed confirmedFn so Badger log entries get freed once PG has
// them, letting the (low-query-flexibility) Badger log cache shrink
// while PG becomes the authoritative long-term store.
//
// Iteration strategy: Scan the entire `log:` prefix in one call
// (no Badger API today for streaming key-only iteration from the
// narrow Persistence interface). At realistic volumes (tens of
// thousands of active job chunks) this is cheap; operators with
// multi-million-chunk backlogs should size the reconciler interval
// to match their cluster's log rate.
func (s *BadgerLogStore) ReconcileConfirmed(
	ctx context.Context,
	minAge time.Duration,
	confirmedFn func(jobID string, seq uint64) (bool, error),
) (deleted, scanned int, firstErr error) {
	if confirmedFn == nil {
		return 0, 0, fmt.Errorf("logstore.ReconcileConfirmed: confirmedFn is required")
	}
	raw, err := s.db.Scan(ctx, "log:", 0)
	if err != nil {
		return 0, 0, fmt.Errorf("logstore.ReconcileConfirmed scan: %w", err)
	}
	ageCutoff := time.Now().Add(-minAge)
	for _, data := range raw {
		if err := ctx.Err(); err != nil {
			return deleted, scanned, err
		}
		scanned++
		var entry LogEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			// Corrupt entries are left in place — the Badger TTL
			// eventually removes them. Better to skip than to
			// delete something we can't positively identify.
			continue
		}
		// Safety margin: entries newer than minAge might not be in
		// PG yet (sink batches every FlushInterval; we don't want
		// to race with the sink flush).
		if entry.Timestamp.After(ageCutoff) {
			continue
		}
		ok, err := confirmedFn(entry.JobID, entry.Seq)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("confirmedFn(%s, %d): %w", entry.JobID, entry.Seq, err)
			}
			continue
		}
		if !ok {
			continue
		}
		key := fmt.Sprintf("log:%s:%010d", entry.JobID, entry.Seq)
		if err := s.db.Delete(ctx, key); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("delete %s: %w", key, err)
			}
			continue
		}
		deleted++
	}
	return deleted, scanned, firstErr
}

// ── In-memory store for tests ────────────────────────────────────────────────

// MemLogStore is an in-memory Store for testing.
type MemLogStore struct {
	entries map[string][]LogEntry // keyed by job_id
}

// NewMemLogStore returns an initialised MemLogStore.
func NewMemLogStore() *MemLogStore {
	return &MemLogStore{entries: make(map[string][]LogEntry)}
}

func (m *MemLogStore) Append(_ context.Context, entry LogEntry) error {
	m.entries[entry.JobID] = append(m.entries[entry.JobID], entry)
	return nil
}

func (m *MemLogStore) Get(_ context.Context, jobID string) ([]LogEntry, error) {
	return m.entries[jobID], nil
}

// ReconcileConfirmed implements Reconcilable for the in-memory store
// so tests for the feature-28 reconciler loop can use MemLogStore
// directly (no Badger required).
func (m *MemLogStore) ReconcileConfirmed(
	ctx context.Context,
	minAge time.Duration,
	confirmedFn func(jobID string, seq uint64) (bool, error),
) (deleted, scanned int, firstErr error) {
	if confirmedFn == nil {
		return 0, 0, fmt.Errorf("logstore.ReconcileConfirmed: confirmedFn is required")
	}
	ageCutoff := time.Now().Add(-minAge)
	for jobID, list := range m.entries {
		kept := list[:0]
		for _, entry := range list {
			if err := ctx.Err(); err != nil {
				return deleted, scanned, err
			}
			scanned++
			if entry.Timestamp.After(ageCutoff) {
				kept = append(kept, entry)
				continue
			}
			ok, err := confirmedFn(entry.JobID, entry.Seq)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				kept = append(kept, entry)
				continue
			}
			if !ok {
				kept = append(kept, entry)
				continue
			}
			deleted++
		}
		m.entries[jobID] = kept
	}
	return deleted, scanned, firstErr
}
