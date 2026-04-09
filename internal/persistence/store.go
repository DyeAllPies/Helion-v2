// internal/persistence/store.go
//
// Store wraps BadgerDB and exposes a typed, protobuf-aware API.
//
// Design goals
// ────────────
//   1. Backend isolation.  No caller outside this package imports BadgerDB.
//      The swap path to etcd (§3.3) works because business logic only touches
//      the Store interface, not badger.DB.
//
//   2. Type safety via generics.  Put[T] and Get[T] accept and return concrete
//      proto.Message types, so a mis-matched key/value pair is a compile error,
//      not a runtime panic.
//
//   3. TTL as a first-class feature.  PutWithTTL is required for nodes/ (TTL =
//      2× heartbeat interval) and tokens/ (TTL = token expiry).  It is the only
//      way to write a value with an expiry; Put never sets a TTL.
//
//   4. Crash-recovery readiness.  List scans all keys under a prefix in a
//      single read-only transaction.  The scheduler calls
//      List[*helionpb.Job](store, PrefixJobs) on startup to find non-terminal
//      jobs without any bespoke scanning logic.
//
//   5. Raw bytes escape hatch.  PutRaw / GetRaw handle values that are not
//      proto messages — specifically the X.509 DER bytes stored under certs/.
//
// Concurrency
// ───────────
// BadgerDB is safe for concurrent use.  The Store itself adds no extra locking.
// Callers do not need to serialise calls to Put/Get/Delete.
//
// Audit log
// ─────────
// AppendAudit is a dedicated helper for audit/ keys.  It builds the
// time-ordered key automatically and always uses a read-write transaction so
// the write is atomic.  Audit records never carry a TTL.

package persistence

import (
	"errors"
	"fmt"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// ---- Store ------------------------------------------------------------------

// Store is a thin, typed wrapper around a BadgerDB instance.
//
// Open a Store with Open(); always Close() it before process exit so BadgerDB
// can flush its write-ahead log.
type Store struct {
	db *badger.DB
}

// Open opens (or creates) a BadgerDB database at path.
//
// The caller is responsible for calling Close() when the store is no longer
// needed.  Concurrent calls to Open with different paths are safe.
func Open(path string) (*Store, error) {
	opts := badger.DefaultOptions(path).
		WithLogger(nil) // silence BadgerDB's default stderr logging
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("persistence.Open %q: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close flushes any pending writes and closes the underlying BadgerDB.
// Subsequent calls to any Store method return ErrClosed.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("persistence.Close: %w", err)
	}
	return nil
}

// ---- Typed proto helpers ----------------------------------------------------

// Put serialises v and writes it under key in a single read-write transaction.
// The value has no expiry.  Use PutWithTTL for values that must expire.
func Put[T proto.Message](s *Store, key []byte, v T) error {
	data, err := proto.Marshal(v)
	if err != nil {
		return fmt.Errorf("persistence.Put marshal: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// PutWithTTL is identical to Put except the entry expires after ttl.
//
// Use this for:
//   - nodes/ entries (ttl = 2 × heartbeat interval)
//   - tokens/ entries (ttl = remaining token lifetime)
//
// BadgerDB enforces the TTL at read time: a Get on an expired key returns
// ErrNotFound.  The GC eventually reclaims the disk space.
func PutWithTTL[T proto.Message](s *Store, key []byte, v T, ttl time.Duration) error {
	data, err := proto.Marshal(v)
	if err != nil {
		return fmt.Errorf("persistence.PutWithTTL marshal: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		e := badger.NewEntry(key, data).WithTTL(ttl)
		return txn.SetEntry(e)
	})
}

// Get deserialises the value stored at key into a new instance of T.
//
// Returns ErrNotFound if the key does not exist or has expired.
func Get[T proto.Message](s *Store, key []byte) (T, error) {
	// Allocate a zero value of T so we always return a typed result.
	var zero T
	result := zero.ProtoReflect().New().Interface().(T)

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return proto.Unmarshal(val, result)
		})
	})
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return zero, ErrNotFound
		}
		return zero, fmt.Errorf("persistence.Get: %w", err)
	}
	return result, nil
}

// Delete removes key from the store.  It is not an error to delete a key that
// does not exist.
func Delete(s *Store, key []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key)
	})
}

// List returns all values whose keys begin with prefix, deserialised as T.
//
// Entries are returned in key order (lexicographic, which for audit/ keys is
// also chronological order).  The scan runs in a single read-only snapshot
// transaction so it sees a consistent point-in-time view of the prefix.
//
// Example — load all jobs on startup:
//
//	jobs, err := List[*helionpb.Job](store, []byte(PrefixJobs))
func List[T proto.Message](s *Store, prefix []byte) ([]T, error) {
	var results []T

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix

		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()

			// Allocate a fresh zero value for each entry so callers get
			// independent objects, not aliases of the same underlying struct.
			var zero T
			v := zero.ProtoReflect().New().Interface().(T)

			if err := item.Value(func(val []byte) error {
				return proto.Unmarshal(val, v)
			}); err != nil {
				return fmt.Errorf("persistence.List unmarshal key %q: %w",
					item.Key(), err)
			}

			results = append(results, v)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("persistence.List prefix %q: %w", prefix, err)
	}
	return results, nil
}

// ---- Raw bytes helpers (for certs/) ----------------------------------------

// PutRaw writes raw bytes under key.  Used for X.509 DER certificate bytes
// stored under certs/ which are not proto messages.
func (s *Store) PutRaw(key, value []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
}

// GetRaw retrieves raw bytes stored at key.
// Returns ErrNotFound if the key does not exist.
func (s *Store) GetRaw(key []byte) ([]byte, error) {
	var out []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			// Copy the bytes out of the transaction-scoped slice.
			out = make([]byte, len(val))
			copy(out, val)
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("persistence.GetRaw: %w", err)
	}
	return out, nil
}

// ---- Audit helpers ----------------------------------------------------------

// AppendAudit writes a proto message as an append-only audit entry.
//
// The key is built automatically from the current wall-clock time and a
// caller-supplied eventID that disambiguates events in the same nanosecond.
// The eventID is typically the job ID or node address involved in the event.
//
// Audit entries have no TTL and are never overwritten — the key schema ensures
// uniqueness even under concurrent writers.
func AppendAudit(s *Store, eventID string, event proto.Message) error {
	key := AuditKey(time.Now().UnixNano(), eventID)
	data, err := proto.Marshal(event)
	if err != nil {
		return fmt.Errorf("persistence.AppendAudit marshal: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// ---- GC helper --------------------------------------------------------------

// RunGC triggers BadgerDB's value-log garbage collector.
//
// BadgerDB does not reclaim disk space automatically — the application must
// periodically call this.  A good cadence is every 5–15 minutes.  The
// coordinator's background loop calls this; callers do not need to.
//
// discardRatio controls how aggressively to GC.  0.5 is BadgerDB's recommended
// default: a vlog file is rewritten if more than 50% of its space is garbage.
func (s *Store) RunGC(discardRatio float64) error {
	err := s.db.RunValueLogGC(discardRatio)
	// ErrNoRewrite means nothing needed GC — not an error from the caller's
	// perspective.
	if errors.Is(err, badger.ErrNoRewrite) {
		return nil
	}
	return err
}
