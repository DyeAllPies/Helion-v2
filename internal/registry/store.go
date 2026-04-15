// internal/registry/store.go
//
// Store interface + sentinel errors. Split from the BadgerDB
// implementation so tests can inject an in-memory fake without
// touching disk.

package registry

import (
	"context"
	"errors"
)

// Sentinel errors. Callers match with errors.Is so the REST handler
// can surface distinct HTTP codes (404 / 409 / 400) for each case.
var (
	// ErrNotFound is returned by Get / Delete / LatestModel when the
	// requested entry does not exist.
	ErrNotFound = errors.New("registry: not found")

	// ErrAlreadyExists is returned by Register when (name, version)
	// is already present. Versions are immutable — a re-register with
	// the same key is a bug, not a mutation, and must be rejected.
	ErrAlreadyExists = errors.New("registry: already exists")
)

// DatasetStore holds the persistence interface the REST handler needs
// for datasets. Tests implement it with a map; production wires a
// BadgerDB-backed implementation via NewBadgerStore.
type DatasetStore interface {
	RegisterDataset(ctx context.Context, d *Dataset) error
	GetDataset(name, version string) (*Dataset, error)
	ListDatasets(ctx context.Context, page, size int) ([]*Dataset, int, error)
	DeleteDataset(ctx context.Context, name, version string) error
	// CountDatasets returns the number of registered dataset entries.
	// Backs helion_datasets_total. Kept on the interface so a fake
	// test store can satisfy the metrics path without a full scan.
	CountDatasets(ctx context.Context) (int, error)
}

// ModelStore is the equivalent surface for models. Separate from
// DatasetStore so a coordinator could plausibly wire them to
// different backends (e.g. BadgerDB for datasets, a remote registry
// for models) without type-system contortions. In practice the
// BadgerDB-backed store satisfies both.
type ModelStore interface {
	RegisterModel(ctx context.Context, m *Model) error
	GetModel(name, version string) (*Model, error)
	LatestModel(name string) (*Model, error)
	ListModels(ctx context.Context, page, size int) ([]*Model, int, error)
	DeleteModel(ctx context.Context, name, version string) error
	// CountModels returns the number of registered model entries.
	// Backs helion_models_total.
	CountModels(ctx context.Context) (int, error)
	// ListBySourceJob returns every model whose SourceJobID matches
	// the argument. Empty slice (not an error) when no model was
	// produced by that job. Backs the workflow-lineage endpoint
	// (feature 18 → deferred/24): the coordinator joins workflow
	// jobs against this to surface "which registered model came out
	// of each workflow step". Walks the model prefix; cheap at MVP
	// scale, will want a secondary index once the model count passes
	// the low thousands.
	ListBySourceJob(ctx context.Context, sourceJobID string) ([]*Model, error)
}

// Store is the union interface the REST handler depends on — it
// owns both dataset and model operations. Most deployments will
// satisfy it with a single *BadgerStore.
type Store interface {
	DatasetStore
	ModelStore
}
