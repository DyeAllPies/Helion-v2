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

	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

// BadgerJSONPersister implements Persister (nodes), JobPersister (jobs), and
// WorkflowPersister (workflows) using BadgerDB with JSON encoding.
type BadgerJSONPersister struct {
	db                *badger.DB
	heartbeatInterval time.Duration

	// Feature 30 — optional envelope-encryption keyring. When
	// non-nil, SaveJob and SaveWorkflow rewrite every declared
	// secret env value into an on-disk EncryptedEnv entry before
	// marshaling; LoadAllJobs and LoadAllWorkflows reverse the
	// transform so in-memory records carry plaintext Env for the
	// dispatch / reveal / log-scrub / response-redact paths.
	//
	// Nil means "no encryption configured" — secret values are
	// persisted in plaintext, matching pre-feature-30 behaviour.
	// The coordinator logs a one-shot warning at boot when a
	// deployment has SecretKeys in use without a KEK; operators
	// who want at-rest encryption set HELION_SECRETSTORE_KEK.
	keyring *secretstore.KeyRing
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

// DB returns the underlying *badger.DB handle for callers that need
// to layer their own key-prefix subsystem on the shared database
// (the dataset + model registries in internal/registry/ do this).
// Returned handle is owned by the persister — callers must not Close
// it; the persister's own shutdown path handles that.
func (p *BadgerJSONPersister) DB() *badger.DB { return p.db }

// SetKeyRing configures the feature-30 envelope-encryption
// keyring. Must be called BEFORE the persister is handed to
// JobStore / WorkflowStore.Restore so the load path sees the
// keyring. Passing nil is a no-op (disables encryption for
// subsequent writes — but previously-encrypted records stay
// on disk and become undecryptable).
//
// Calling SetKeyRing after the persister has been serving
// traffic is supported but not recommended: existing records
// don't get encrypted retroactively, so the coordinator ends
// up with a mix of encrypted and plaintext records. Use the
// rotation admin endpoint for controlled migrations.
func (p *BadgerJSONPersister) SetKeyRing(kr *secretstore.KeyRing) {
	p.keyring = kr
}

// KeyRing returns the currently configured keyring or nil when
// envelope encryption is disabled. Exposed so the feature-30
// rotation endpoint can advance the active KEK.
func (p *BadgerJSONPersister) KeyRing() *secretstore.KeyRing {
	return p.keyring
}

// NewBadgerJSONPersisterReadOnly opens an existing BadgerDB at path in
// read-only mode. This is the safe way to scan a live database — the
// BypassLockGuard flag allows a reader to open the DB even while a separate
// writer (the running coordinator) has it open, so tools like
// `helion-coordinator analytics backfill` can run against a DB in use.
//
// Any write will fail with a BadgerDB error; only Get / Scan / View-style
// operations are supported.
func NewBadgerJSONPersisterReadOnly(path string) (*BadgerJSONPersister, error) {
	opts := badger.DefaultOptions(path).
		WithLogger(nil).
		WithReadOnly(true).
		WithBypassLockGuard(true)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("BadgerJSONPersister open read-only %q: %w", path, err)
	}
	return &BadgerJSONPersister{db: db, heartbeatInterval: 0}, nil
}

// Close flushes and closes the underlying BadgerDB.
func (p *BadgerJSONPersister) Close() error {
	return p.db.Close()
}

// Ping does a lightweight read transaction to verify BadgerDB is open and operational.
func (p *BadgerJSONPersister) Ping() error {
	return p.db.View(func(_ *badger.Txn) error { return nil })
}
