// internal/persistence/errors.go
//
// Sentinel errors exported by the persistence layer.
//
// Callers should use errors.Is() to inspect errors returned by Store methods.
// Importing BadgerDB directly in business-logic packages is unnecessary and
// couples those packages to the storage backend — use these sentinels instead.

package persistence

import "errors"

// ErrNotFound is returned by Get when the requested key does not exist in the
// database.  It wraps badger.ErrKeyNotFound so that callers never need to
// import github.com/dgraph-io/badger/v4.
var ErrNotFound = errors.New("persistence: key not found")

// ErrClosed is returned when a method is called on a Store that has already
// been closed.
var ErrClosed = errors.New("persistence: store is closed")
