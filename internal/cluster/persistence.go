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

// Ping does a lightweight read transaction to verify BadgerDB is open and operational.
func (p *BadgerJSONPersister) Ping() error {
	return p.db.View(func(_ *badger.Txn) error { return nil })
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

// ── Workflow methods (satisfies WorkflowPersister) ──────────────────────────

// SaveWorkflow writes a Workflow record under workflows/{id}.
// Workflow entries have no TTL — they persist until explicitly deleted.
func (p *BadgerJSONPersister) SaveWorkflow(_ context.Context, w *cpb.Workflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("SaveWorkflow marshal: %w", err)
	}
	key := []byte("workflows/" + w.ID)
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// LoadAllWorkflows reads all workflows/ entries for crash-recovery on startup.
func (p *BadgerJSONPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error) {
	var workflows []*cpb.Workflow
	prefix := []byte("workflows/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var w cpb.Workflow
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &w)
			}); err != nil {
				return fmt.Errorf("LoadAllWorkflows unmarshal %q: %w", it.Item().Key(), err)
			}
			workflows = append(workflows, &w)
		}
		return nil
	})
	return workflows, err
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
func (NopPersister) SaveWorkflow(_ context.Context, _ *cpb.Workflow) error        { return nil }
func (NopPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error)  { return nil, nil }
func (NopPersister) AppendAudit(_ context.Context, _, _, _, _ string) error       { return nil }

// ── MemPersister ──────────────────────────────────────────────────────────────

// MemPersister is an in-memory Persister (node side) for tests that need to
// inspect what was persisted without a real database.
//
// For the job side, use MemJobPersister (defined in job.go).  Keeping them
// separate means each test helper stays focused and independently inspectable.
type MemPersister struct {
	mu        sync.Mutex
	Nodes     map[string]*cpb.Node
	Workflows map[string]*cpb.Workflow
	Audits    []map[string]string
}

// NewMemPersister returns an initialised MemPersister.
func NewMemPersister() *MemPersister {
	return &MemPersister{
		Nodes:     make(map[string]*cpb.Node),
		Workflows: make(map[string]*cpb.Workflow),
	}
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

func (m *MemPersister) SaveWorkflow(_ context.Context, w *cpb.Workflow) error {
	cp := *w
	cpJobs := make([]cpb.WorkflowJob, len(w.Jobs))
	copy(cpJobs, w.Jobs)
	cp.Jobs = cpJobs
	m.mu.Lock()
	m.Workflows[w.ID] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error) {
	m.mu.Lock()
	workflows := make([]*cpb.Workflow, 0, len(m.Workflows))
	for _, w := range m.Workflows {
		cp := *w
		cpJobs := make([]cpb.WorkflowJob, len(w.Jobs))
		copy(cpJobs, w.Jobs)
		cp.Jobs = cpJobs
		workflows = append(workflows, &cp)
	}
	m.mu.Unlock()
	return workflows, nil
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

// ── Generic key-value methods for Phase 4 auth storage ───────────────────────

// Get retrieves a value by key from BadgerDB.
func (p *BadgerJSONPersister) Get(ctx context.Context, key string) ([]byte, error) {
	var value []byte
	err := p.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		value, err = item.ValueCopy(nil)
		return err
	})
	if err == badger.ErrKeyNotFound {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return value, err
}

// Put stores a value by key in BadgerDB.
func (p *BadgerJSONPersister) Put(ctx context.Context, key string, value []byte) error {
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	})
}

// PutWithTTL stores a value by key with a TTL in BadgerDB.
func (p *BadgerJSONPersister) PutWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return p.db.Update(func(txn *badger.Txn) error {
		entry := badger.NewEntry([]byte(key), value).WithTTL(ttl)
		return txn.SetEntry(entry)
	})
}

// Delete removes a key from BadgerDB.
func (p *BadgerJSONPersister) Delete(ctx context.Context, key string) error {
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// Scan retrieves all keys with a given prefix, up to limit entries.
// Used by audit.Logger to query events.
func (p *BadgerJSONPersister) Scan(ctx context.Context, prefix string, limit int) ([][]byte, error) {
	var results [][]byte
	
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		
		it := txn.NewIterator(opts)
		defer it.Close()
		
		count := 0
		for it.Rewind(); it.Valid(); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			
			item := it.Item()
			value, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			
			results = append(results, value)
			count++
		}
		
		return nil
	})
	
	return results, err
}
