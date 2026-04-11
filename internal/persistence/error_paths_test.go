// internal/persistence/error_paths_test.go
//
// Tests for unhappy paths: invalid DB path, operations on a closed store,
// and RunGC on an idle store.

package persistence_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/persistence"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TestRunGCNoError verifies RunGC on an idle store does not return an error.
// BadgerDB returns ErrNoRewrite when there is nothing to rewrite; the Store
// wrapper translates that to nil.
func TestRunGCNoError(t *testing.T) {
	s := openFresh(t)
	if err := s.RunGC(0.5); err != nil {
		t.Errorf("RunGC: %v", err)
	}
}

// TestOpen_InvalidPath_ReturnsError covers the badger.Open error branch in
// persistence.Open. Passing a path that already exists as a plain file (not
// a directory) causes badger to fail.
func TestOpen_InvalidPath_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Create a plain file at the DB path location — badger expects a dir.
	fpath := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(fpath, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup file: %v", err)
	}
	if _, err := persistence.Open(fpath); err == nil {
		t.Error("Open: want error when path is a regular file, got nil")
	}
}

// TestGet_OnClosedStore_ReturnsError covers the non-NotFound error branch in
// Get: once the underlying DB is closed, Get returns a "DB closed" error
// that is not badger.ErrKeyNotFound.
func TestGet_OnClosedStore_ReturnsError(t *testing.T) {
	// Don't use openFresh here — it registers a Cleanup that calls Close and
	// would panic on the second Close. Open manually and close in one place.
	s, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()

	_, err = persistence.Get[*wrapperspb.StringValue](s, []byte("some-key"))
	if err == nil {
		t.Error("Get on closed store: want error, got nil")
	}
	if errors.Is(err, persistence.ErrNotFound) {
		t.Error("Get on closed store: want non-NotFound error")
	}
}

// TestGetRaw_OnClosedStore_ReturnsError covers the non-NotFound error branch
// in GetRaw.
func TestGetRaw_OnClosedStore_ReturnsError(t *testing.T) {
	s, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()

	if _, err := s.GetRaw([]byte("some-key")); err == nil {
		t.Error("GetRaw on closed store: want error, got nil")
	}
}

// TestList_OnClosedStore_ReturnsError covers the view-error branch in List.
func TestList_OnClosedStore_ReturnsError(t *testing.T) {
	s, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()

	_, err = persistence.List[*wrapperspb.StringValue](s, []byte("some-prefix"))
	if err == nil {
		t.Error("List on closed store: want error, got nil")
	}
}
