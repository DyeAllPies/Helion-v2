// internal/cluster/persistence.go
//
// BadgerJSONPersister — production Persister + JobPersister implementation.
// NopPersister        — test no-op (satisfies both interfaces).
// MemPersister        — test in-memory node/audit store (satisfies Persister).
//
// Persister    (registry.go) — node CRUD + audit
// JobPersister (job.go)      — job CRUD + audit
//
// BadgerJSONPersister and NopPersister satisfy both interfaces so a single
// instance can be injected into both Registry and JobStore in production.
// MemPersister covers the node side only; MemJobPersister (in job.go) covers
// the job side — keeping test helpers focused and independently inspectable.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── BadgerJSONPersister ───────────────────────────────────────────────────────

// BadgerJSONPersister implements both Persister (nodes) and JobPersister (jobs)
// using BadgerDB with JSON encoding.
//
// Migration: replace json.Marshal/Unmarshal with proto.Marshal/Unmarshal once
// Node and Job are generated proto messages. The key schema and transaction
// boundaries do not change.
type BadgerJSONPersister struct {
	db                *badger.DB
	heartbeatInterval time.Duration
}

// NewBadgerJSONPersister opens (or creates) a BadgerDB at path.
func NewBadgerJSONPersister(path string, heartbeatInterval time.Duration) (*BadgerJSONPersister, error) {
	opts := badger.DefaultOptions(path).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("BadgerJSONPersister open %q: %w", path, err)
	}
	return &BadgerJSONPersister{db: db, heartbeatInterval: heartbeatInterval}, nil
}

// Close flushes and closes the underlying BadgerDB.
func (p *BadgerJSONPersister) Close() error {
	return p.db.Close()
}

// ── Node methods (satisfies Persister) ───────────────────────────────────────

// SaveNode writes a Node record under nodes/{address} with TTL = 2× heartbeat interval.
func (p *BadgerJSONPersister) SaveNode(_ context.Context, n *cpb.Node) error {
	data, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("SaveNode marshal: %w", err)
	}
	key := []byte("nodes/" + n.Address)
	ttl := 2 * p.heartbeatInterval
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.SetEntry(badger.NewEntry(key, data).WithTTL(ttl))
	})
}

// LoadAllNodes reads all nodes/ entries for crash-recovery on startup.
func (p *BadgerJSONPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error) {
	var nodes []*cpb.Node
	prefix := []byte("nodes/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var n cpb.Node
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &n)
			}); err != nil {
				return fmt.Errorf("LoadAllNodes unmarshal %q: %w", it.Item().Key(), err)
			}
			nodes = append(nodes, &n)
		}
		return nil
	})
	return nodes, err
}

// ── Job methods (satisfies JobPersister) ─────────────────────────────────────

// SaveJob writes a Job record under jobs/{id} in a single read-write
// transaction.  Job entries have no TTL — they are immutable once terminal and
// are the source of truth for crash recovery.
func (p *BadgerJSONPersister) SaveJob(_ context.Context, j *cpb.Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("SaveJob marshal: %w", err)
	}
	key := []byte("jobs/" + j.ID)
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// LoadAllJobs reads all jobs/ entries for crash-recovery on startup.
// It returns every job regardless of status; the caller (JobStore.Restore)
// filters for non-terminal jobs to build the retry queue.
func (p *BadgerJSONPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error) {
	var jobs []*cpb.Job
	prefix := []byte("jobs/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var j cpb.Job
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &j)
			}); err != nil {
				return fmt.Errorf("LoadAllJobs unmarshal %q: %w", it.Item().Key(), err)
			}
			jobs = append(jobs, &j)
		}
		return nil
	})
	return jobs, err
}

// ── Shared audit method (satisfies both Persister and JobPersister) ───────────

// AppendAudit writes an audit entry under audit/{nano}-{target}.
// Called by both the Registry (node events) and JobStore (job events).
// The key schema guarantees chronological order in prefix scans.
func (p *BadgerJSONPersister) AppendAudit(_ context.Context, eventType, actor, target, detail string) error {
	record := map[string]string{
		"event_type":  eventType,
		"actor":       actor,
		"target":      target,
		"detail":      detail,
		"occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("AppendAudit marshal: %w", err)
	}
	key := []byte(fmt.Sprintf("audit/%020d-%s", time.Now().UnixNano(), target))
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// ── NopPersister ──────────────────────────────────────────────────────────────

// NopPersister satisfies both Persister and JobPersister — for unit tests that
// do not need to inspect persisted state.
type NopPersister struct{}

func (NopPersister) SaveNode(_ context.Context, _ *cpb.Node) error               { return nil }
func (NopPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error)          { return nil, nil }
func (NopPersister) SaveJob(_ context.Context, _ *cpb.Job) error                  { return nil }
func (NopPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error)            { return nil, nil }
func (NopPersister) AppendAudit(_ context.Context, _, _, _, _ string) error       { return nil }

// ── MemPersister ──────────────────────────────────────────────────────────────

// MemPersister is an in-memory Persister (node side) for tests that need to
// inspect what was persisted without a real database.
//
// For the job side, use MemJobPersister (defined in job.go).  Keeping them
// separate means each test helper stays focused and independently inspectable.
type MemPersister struct {
	mu     sync.Mutex
	Nodes  map[string]*cpb.Node
	Audits []map[string]string
}

// NewMemPersister returns an initialised MemPersister.
func NewMemPersister() *MemPersister {
	return &MemPersister{Nodes: make(map[string]*cpb.Node)}
}

// Mu locks the MemPersister for direct field inspection in tests.
func (m *MemPersister) Mu() { m.mu.Lock() }

// MuUnlock releases the lock acquired by Mu.
func (m *MemPersister) MuUnlock() { m.mu.Unlock() }

func (m *MemPersister) SaveNode(_ context.Context, n *cpb.Node) error {
	cp := *n
	m.mu.Lock()
	m.Nodes[n.Address] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error) {
	m.mu.Lock()
	nodes := make([]*cpb.Node, 0, len(m.Nodes))
	for _, n := range m.Nodes {
		cp := *n
		nodes = append(nodes, &cp)
	}
	m.mu.Unlock()
	return nodes, nil
}

func (m *MemPersister) AppendAudit(_ context.Context, eventType, actor, target, detail string) error {
	m.mu.Lock()
	m.Audits = append(m.Audits, map[string]string{
		"event_type": eventType,
		"actor":      actor,
		"target":     target,
		"detail":     detail,
	})
	m.mu.Unlock()
	return nil
}
