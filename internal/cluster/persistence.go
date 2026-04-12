// internal/cluster/persistence.go
//
// BadgerJSONPersister — production implementation for node, job, workflow, and
// audit persistence using BadgerDB with JSON encoding.
//
// File layout
// ───────────
//   persistence.go              — struct, constructor, Close, Ping
//   persistence_nodes.go        — SaveNode, LoadAllNodes
//   persistence_jobs.go         — SaveJob, LoadAllJobs
//   persistence_workflows.go    — SaveWorkflow, LoadAllWorkflows
//   persistence_kv.go           — AppendAudit, Get, Put, PutWithTTL, Delete, Scan
//   persistence_test_helpers.go — NopPersister, MemPersister

package cluster

import (
	"fmt"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

// BadgerJSONPersister implements Persister (nodes), JobPersister (jobs), and
// WorkflowPersister (workflows) using BadgerDB with JSON encoding.
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
