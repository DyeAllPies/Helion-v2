// internal/artifacts/store.go
//
// Artifact store abstraction for the Helion ML pipeline.
//
// The store holds the *bytes* of ML artifacts (datasets, model checkpoints,
// preprocessed features, metrics blobs). Metadata — names, versions,
// lineage — lives in BadgerDB and PostgreSQL; this package does not know
// about any of that.
//
// URIs returned by a Store are opaque to callers: pass them back to Get /
// Stat / Delete, or hand them to a node so it can stage an input file
// before running a job. Do not parse them outside this package.

package artifacts

import (
	"context"
	"errors"
	"io"
	"time"
)

// URI is an opaque artifact reference. Examples:
//
//	file:///var/lib/helion/artifacts/run-42/model.pt
//	s3://helion/run-42/model.pt
//
// Callers must treat URIs as opaque — only the Store that produced a URI
// is guaranteed to understand it.
type URI string

// Metadata describes a stored artifact.
type Metadata struct {
	Size      int64     // byte length of the stored object
	SHA256    string    // lower-case hex digest of the stored bytes
	UpdatedAt time.Time // last-modified timestamp from the backend
}

// Store is the artifact backend interface. Implementations must be safe
// for concurrent use.
type Store interface {
	// Put writes size bytes read from r under the given logical key and
	// returns the resulting URI. Keys are backend-relative (e.g.
	// "run-42/model.pt"); the Store maps them onto its native namespace.
	//
	// size may be -1 if unknown, in which case the Store must still
	// read r to EOF. Implementations are free to reject unknown-size
	// uploads if they cannot support streaming.
	Put(ctx context.Context, key string, r io.Reader, size int64) (URI, error)

	// Get opens the artifact at uri for reading. The caller must Close
	// the returned ReadCloser.
	Get(ctx context.Context, uri URI) (io.ReadCloser, error)

	// Stat returns metadata for the artifact at uri, or ErrNotFound if
	// no such artifact exists.
	Stat(ctx context.Context, uri URI) (Metadata, error)

	// Delete removes the artifact at uri. Deleting a non-existent URI
	// returns ErrNotFound; callers who want idempotent semantics should
	// check for that sentinel explicitly.
	Delete(ctx context.Context, uri URI) error
}

// Sentinel errors returned by Store implementations.
var (
	// ErrNotFound is returned by Get/Stat/Delete when the requested URI
	// does not exist in the backend.
	ErrNotFound = errors.New("artifacts: not found")

	// ErrInvalidKey is returned by Put when the supplied key is unsafe
	// (empty, absolute, contains "..", or otherwise cannot be mapped
	// onto the backend's namespace).
	ErrInvalidKey = errors.New("artifacts: invalid key")

	// ErrInvalidURI is returned by Get/Stat/Delete when a URI does not
	// belong to the Store it was passed to (wrong scheme, wrong root,
	// malformed).
	ErrInvalidURI = errors.New("artifacts: invalid uri")
)
