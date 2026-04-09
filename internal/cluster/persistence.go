// internal/cluster/persistence.go
//
// BadgerJSONPersister — production Persister implementation.
// NopPersister        — test no-op.
// MemPersister        — test in-memory implementation.
//
// All three satisfy the Persister interface defined in registry.go.

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

// BadgerJSONPersister implements Persister using BadgerDB with JSON encoding.
// Migration: replace json.Marshal/Unmarshal with proto.Marshal/Unmarshal once
// the Node and Job types are generated proto messages.
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

// AppendAudit writes an audit entry under audit/{nano}-{target}.
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

// NopPersister is a Persister that does nothing — for unit tests.
type NopPersister struct{}

func (NopPersister) SaveNode(_ context.Context, _ *cpb.Node) error                          { return nil }
func (NopPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error)                    { return nil, nil }
func (NopPersister) AppendAudit(_ context.Context, _, _, _, _ string) error                 { return nil }

// ── MemPersister ──────────────────────────────────────────────────────────────

// MemPersister is an in-memory Persister for integration tests that need to
// inspect what was persisted without a real database.
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
