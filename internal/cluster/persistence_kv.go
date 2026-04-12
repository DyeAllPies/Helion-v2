// internal/cluster/persistence_kv.go
//
// BadgerJSONPersister generic key-value methods: AppendAudit, Get, Put,
// PutWithTTL, Delete, Scan. Used by audit logging and Phase 4 auth storage.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

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

// Get retrieves a value by key from BadgerDB.
func (p *BadgerJSONPersister) Get(_ context.Context, key string) ([]byte, error) {
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
func (p *BadgerJSONPersister) Put(_ context.Context, key string, value []byte) error {
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	})
}

// PutWithTTL stores a value by key with a TTL in BadgerDB.
func (p *BadgerJSONPersister) PutWithTTL(_ context.Context, key string, value []byte, ttl time.Duration) error {
	return p.db.Update(func(txn *badger.Txn) error {
		entry := badger.NewEntry([]byte(key), value).WithTTL(ttl)
		return txn.SetEntry(entry)
	})
}

// Delete removes a key from BadgerDB.
func (p *BadgerJSONPersister) Delete(_ context.Context, key string) error {
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// Scan retrieves all keys with a given prefix, up to limit entries.
// Used by audit.Logger to query events.
func (p *BadgerJSONPersister) Scan(_ context.Context, prefix string, limit int) ([][]byte, error) {
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
