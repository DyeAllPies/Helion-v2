// internal/registry/badger.go
//
// BadgerDB-backed Store implementation. Shares the coordinator's
// main BadgerDB instance (no separate DB file) — registry metadata
// is small and low-traffic compared to jobs, so a dedicated store
// would be operational overhead for no isolation benefit.
//
// Key layout:
//   datasets/<name>/<version> -> JSON(Dataset)
//   models/<name>/<version>   -> JSON(Model)
//
// The (name, version) pair becomes a nested key segment, which
// gives us prefix-scan range queries for free: "list all versions
// of dataset X" is a prefix scan on "datasets/X/".

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	badger "github.com/dgraph-io/badger/v4"
)

// BadgerStore satisfies Store against a shared *badger.DB.
type BadgerStore struct {
	db *badger.DB
}

// NewBadgerStore returns a BadgerStore backed by db. The coordinator
// passes its existing DB handle here — no TTL, no per-key expiry.
// Registry entries are intentionally long-lived (a registered model
// is the primary key into the artifact store for that model).
func NewBadgerStore(db *badger.DB) *BadgerStore {
	return &BadgerStore{db: db}
}

// Key prefixes kept as package-local constants so tests that need
// to sanity-check the on-disk layout don't have to guess them.
const (
	datasetKeyPrefix = "datasets/"
	modelKeyPrefix   = "models/"
)

func datasetKey(name, version string) []byte {
	return []byte(datasetKeyPrefix + name + "/" + version)
}
func modelKey(name, version string) []byte {
	return []byte(modelKeyPrefix + name + "/" + version)
}

// ── Datasets ────────────────────────────────────────────────────────────

// RegisterDataset persists d under datasets/<name>/<version>. Returns
// ErrAlreadyExists if that key is already present — re-registering
// the same version is a user error, not a silent update.
func (s *BadgerStore) RegisterDataset(_ context.Context, d *Dataset) error {
	key := datasetKey(d.Name, d.Version)
	return s.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(key); err == nil {
			return ErrAlreadyExists
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("RegisterDataset: probe: %w", err)
		}
		data, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("RegisterDataset: marshal: %w", err)
		}
		return txn.Set(key, data)
	})
}

// GetDataset reads a specific (name, version) entry, returning
// ErrNotFound for any missing key.
func (s *BadgerStore) GetDataset(name, version string) (*Dataset, error) {
	key := datasetKey(name, version)
	var out *Dataset
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			var d Dataset
			if err := json.Unmarshal(v, &d); err != nil {
				return fmt.Errorf("GetDataset: unmarshal: %w", err)
			}
			out = &d
			return nil
		})
	})
	return out, err
}

// ListDatasets reads every dataset entry under the prefix, sorts by
// created-at descending (newest first to match the project's job /
// workflow list conventions), then paginates. Page is 1-indexed to
// match the rest of the API.
func (s *BadgerStore) ListDatasets(_ context.Context, page, size int) ([]*Dataset, int, error) {
	all, err := s.loadAllDatasets()
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	total := len(all)
	start, end := pageBounds(page, size, total)
	return all[start:end], total, nil
}

// DeleteDataset removes a specific version. Returns ErrNotFound if
// nothing was deleted — callers that want idempotent semantics
// check for the sentinel explicitly.
func (s *BadgerStore) DeleteDataset(_ context.Context, name, version string) error {
	key := datasetKey(name, version)
	return s.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(key); errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return txn.Delete(key)
	})
}

// CountDatasets walks the dataset prefix in key-only mode so the count
// doesn't pay the JSON-decode cost. O(n) in the number of registered
// datasets but cheap — BadgerDB prefix iteration with fetchValues=false
// is essentially an LSM-level key scan.
func (s *BadgerStore) CountDatasets(_ context.Context) (int, error) {
	return s.countPrefix([]byte(datasetKeyPrefix))
}

// CountModels is the model-side counterpart to CountDatasets.
func (s *BadgerStore) CountModels(_ context.Context) (int, error) {
	return s.countPrefix([]byte(modelKeyPrefix))
}

// ListBySourceJob walks every model and returns those whose
// SourceJobID matches the argument. Linear scan; acceptable at MVP
// scale because the caller (workflow-lineage endpoint) is behind
// the registry rate limiter and model counts in a minimal-ML
// deployment are O(hundreds). If that assumption changes, add a
// secondary index under `models-by-source-job/<source_job_id>/<model_name>/<version>`.
func (s *BadgerStore) ListBySourceJob(_ context.Context, sourceJobID string) ([]*Model, error) {
	if sourceJobID == "" {
		return nil, nil
	}
	all, err := s.loadAllModels()
	if err != nil {
		return nil, err
	}
	out := make([]*Model, 0, 4)
	for _, m := range all {
		if m.SourceJobID == sourceJobID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *BadgerStore) countPrefix(prefix []byte) (int, error) {
	n := 0
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			n++
		}
		return nil
	})
	return n, err
}

func (s *BadgerStore) loadAllDatasets() ([]*Dataset, error) {
	var out []*Dataset
	prefix := []byte(datasetKeyPrefix)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var d Dataset
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &d)
			}); err != nil {
				return fmt.Errorf("loadAllDatasets: unmarshal %q: %w", it.Item().Key(), err)
			}
			out = append(out, &d)
		}
		return nil
	})
	return out, err
}

// ── Models ──────────────────────────────────────────────────────────────

// RegisterModel persists m under models/<name>/<version>.
func (s *BadgerStore) RegisterModel(_ context.Context, m *Model) error {
	key := modelKey(m.Name, m.Version)
	return s.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(key); err == nil {
			return ErrAlreadyExists
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("RegisterModel: probe: %w", err)
		}
		data, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("RegisterModel: marshal: %w", err)
		}
		return txn.Set(key, data)
	})
}

// GetModel reads a specific (name, version) model entry.
func (s *BadgerStore) GetModel(name, version string) (*Model, error) {
	key := modelKey(name, version)
	var out *Model
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			var m Model
			if err := json.Unmarshal(v, &m); err != nil {
				return fmt.Errorf("GetModel: unmarshal: %w", err)
			}
			out = &m
			return nil
		})
	})
	return out, err
}

// LatestModel returns the most recently created entry for a given
// model name. "Latest" is *chronological*, not semantic — if the
// user registers v0.2 before v0.1, v0.2 wins. Works for the common
// flow where versions are monotonically created; a registrar who
// needs strict semver ordering can Get a specific version by name.
func (s *BadgerStore) LatestModel(name string) (*Model, error) {
	all, err := s.loadAllModelsFor(name)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNotFound
	}
	latest := all[0]
	for _, m := range all[1:] {
		if m.CreatedAt.After(latest.CreatedAt) {
			latest = m
		}
	}
	return latest, nil
}

// ListModels paginates over every model entry, newest first.
func (s *BadgerStore) ListModels(_ context.Context, page, size int) ([]*Model, int, error) {
	all, err := s.loadAllModels()
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	total := len(all)
	start, end := pageBounds(page, size, total)
	return all[start:end], total, nil
}

// DeleteModel removes a specific version of a model.
func (s *BadgerStore) DeleteModel(_ context.Context, name, version string) error {
	key := modelKey(name, version)
	return s.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(key); errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return txn.Delete(key)
	})
}

func (s *BadgerStore) loadAllModels() ([]*Model, error) {
	return s.loadModelsPrefixed([]byte(modelKeyPrefix))
}
func (s *BadgerStore) loadAllModelsFor(name string) ([]*Model, error) {
	return s.loadModelsPrefixed([]byte(modelKeyPrefix + name + "/"))
}

func (s *BadgerStore) loadModelsPrefixed(prefix []byte) ([]*Model, error) {
	var out []*Model
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var m Model
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &m)
			}); err != nil {
				return fmt.Errorf("loadModelsPrefixed: unmarshal %q: %w", it.Item().Key(), err)
			}
			out = append(out, &m)
		}
		return nil
	})
	return out, err
}

// pageBounds converts (page, size, total) into a safe slice range
// matching the rest of the API's pagination conventions. Page is
// 1-indexed; negative sizes fall back to 50; page past the end
// returns an empty window (not an error).
func pageBounds(page, size, total int) (start, end int) {
	if size <= 0 {
		size = 50
	}
	if page < 1 {
		page = 1
	}
	start = (page - 1) * size
	if start >= total {
		return total, total
	}
	end = start + size
	if end > total {
		end = total
	}
	return start, end
}
