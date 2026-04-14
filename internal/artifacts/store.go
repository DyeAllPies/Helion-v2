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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

	// ErrChecksumMismatch is returned by GetAndVerify when the bytes
	// read from the Store do not hash to the expected SHA-256. Callers
	// that receive this error MUST NOT use the bytes — the backend is
	// either corrupted or untrusted.
	ErrChecksumMismatch = errors.New("artifacts: sha256 mismatch")
)

// GetAndVerifyTo streams the artifact at uri into dst, computing a
// rolling SHA-256 as each chunk passes through. On EOF the digest is
// compared to expectedHex (case-insensitive); a mismatch returns
// ErrChecksumMismatch. The number of bytes written is returned in
// both the success and mismatch cases — callers are expected to
// truncate / unlink dst on ErrChecksumMismatch (the stager uses a
// tempfile → rename pattern so a partial write never becomes
// visible as a finalised input).
//
// Memory use is O(chunkSize) — the 64 KiB copy buffer from io.Copy,
// not the whole object. This is the primary reader for multi-GB ML
// artifacts where the older all-in-memory GetAndVerify would OOM.
//
// maxBytes bounds the object size. Zero means "no caller cap" (the
// Store's own backend caps still apply). A positive value returns
// an error if the backend produces more bytes than expected — +1 in
// the LimitedReader so an object exactly at the cap still produces
// an "oversize" error, not a silent truncation.
func GetAndVerifyTo(ctx context.Context, s Store, uri URI, expectedHex string, maxBytes int64, dst io.Writer) (int64, error) {
	if expectedHex == "" {
		return 0, errors.New("artifacts: GetAndVerifyTo requires expected sha256")
	}
	rc, err := s.Get(ctx, uri)
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	var src io.Reader = rc
	var limit *io.LimitedReader
	if maxBytes > 0 {
		limit = &io.LimitedReader{R: rc, N: maxBytes + 1}
		src = limit
	}
	h := sha256.New()
	// TeeReader pipes every chunk io.Copy pulls from src into h, so
	// the hash is computed as part of the stream — no second pass
	// over the bytes, no buffering.
	tee := io.TeeReader(src, h)
	n, err := io.Copy(dst, tee)
	if err != nil {
		return n, fmt.Errorf("artifacts: GetAndVerifyTo copy: %w", err)
	}
	// LimitedReader stops at maxBytes+1, so if we managed to write
	// more than maxBytes bytes the source was oversize.
	if maxBytes > 0 && n > maxBytes {
		return n, fmt.Errorf("artifacts: GetAndVerifyTo size > %d", maxBytes)
	}
	if !equalFoldHex(hex.EncodeToString(h.Sum(nil)), expectedHex) {
		return n, ErrChecksumMismatch
	}
	return n, nil
}

// GetAndVerify reads the artifact at uri and returns its bytes only
// if their SHA-256 matches expectedHex (case-insensitive lower-hex).
// Mismatches return ErrChecksumMismatch and nil bytes so callers
// cannot accidentally use corrupted data.
//
// Convenience wrapper around GetAndVerifyTo for small artifacts
// where holding the bytes in memory is acceptable. For multi-GB
// downloads, use GetAndVerifyTo with a file or tempfile writer to
// avoid OOM. maxBytes bounds the response size; pass 0 for unlimited.
func GetAndVerify(ctx context.Context, s Store, uri URI, expectedHex string, maxBytes int64) ([]byte, error) {
	var buf bytesBuffer
	_, err := GetAndVerifyTo(ctx, s, uri, expectedHex, maxBytes, &buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bytesBuffer avoids pulling in bytes.Buffer just for this one call
// path — keeps the import list minimal and the allocation pattern
// clear (one grow cycle per digest-known read).
type bytesBuffer struct{ b []byte }

func (w *bytesBuffer) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *bytesBuffer) Bytes() []byte { return w.b }

// equalFoldHex is a hex-only case-insensitive compare. Hex.EncodeToString
// produces lower case, so this is mostly defensive against callers who
// stored upper-case digests somewhere.
func equalFoldHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ac, bc := a[i], b[i]
		if ac >= 'A' && ac <= 'Z' {
			ac += 'a' - 'A'
		}
		if bc >= 'A' && bc <= 'Z' {
			bc += 'a' - 'A'
		}
		if ac != bc {
			return false
		}
	}
	return true
}
