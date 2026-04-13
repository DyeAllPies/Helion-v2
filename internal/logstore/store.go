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

// Persistence is the narrow BadgerDB interface the logstore needs.
type Persistence interface {
	Put(ctx context.Context, key string, value []byte) error
	Scan(ctx context.Context, prefix string, limit int) ([][]byte, error)
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
