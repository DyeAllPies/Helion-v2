// internal/artifacts/local.go
//
// LocalStore is a filesystem-backed Store. It holds artifacts under a
// single root directory and returns file:// URIs pointing into that root.
//
// This is the default backend for dev and single-node deployments — no
// object-storage dependency, no network, just a directory on disk. The
// same interface is satisfied by S3Store (see s3.go) so production
// deployments can swap backends without touching job / workflow code.
//
// Concurrency: Put is atomic via tempfile+rename on the same volume;
// callers writing the same key concurrently will see last-writer-wins.
// Get / Stat / Delete hold no locks — the OS handles file locking.

package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// LocalStore stores artifacts on the local filesystem.
type LocalStore struct {
	root string // absolute, cleaned
}

// NewLocalStore creates a LocalStore rooted at dir. dir is created if it
// does not exist. The returned root is always absolute and cleaned so
// that URI translation is unambiguous.
func NewLocalStore(dir string) (*LocalStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("artifacts: local store root is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("artifacts: resolve root: %w", err)
	}
	abs = filepath.Clean(abs)
	// 0o700: the artifact root may hold model checkpoints, training
	// data, and the full set of job IDs on the host. World-readable
	// permissions would leak that metadata to any unprivileged user
	// on a shared machine. File contents inside the tree are written
	// with os.CreateTemp (0o600 by default) so the files themselves
	// were already owner-only — tightening the directory mode closes
	// the enumeration gap.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("artifacts: create root: %w", err)
	}
	return &LocalStore{root: abs}, nil
}

// Root returns the absolute root directory. Exposed for tests and for the
// coordinator's /readyz introspection.
func (s *LocalStore) Root() string { return s.root }

// Put writes r to <root>/<key> and returns a file:// URI. The write is
// atomic: bytes land in a sibling tempfile, are fsynced, then renamed
// onto the final path. If size >= 0 it is used as a sanity check; a
// short or long read returns an error and leaves no partial file behind.
func (s *LocalStore) Put(ctx context.Context, key string, r io.Reader, size int64) (URI, error) {
	rel, err := sanitizeKey(key)
	if err != nil {
		return "", err
	}
	full := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", fmt.Errorf("artifacts: mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".helion-artifact-*.tmp")
	if err != nil {
		return "", fmt.Errorf("artifacts: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	// If anything below fails, make sure the tempfile is cleaned up.
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	n, err := copyWithCtx(ctx, tmp, r)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("artifacts: copy: %w", err)
	}
	if size >= 0 && n != size {
		cleanup()
		return "", fmt.Errorf("artifacts: size mismatch: declared %d, wrote %d", size, n)
	}

	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("artifacts: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("artifacts: close: %w", err)
	}
	if err := os.Rename(tmpPath, full); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("artifacts: rename: %w", err)
	}
	return fileURI(full), nil
}

// Get opens the artifact at uri for reading.
func (s *LocalStore) Get(ctx context.Context, uri URI) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.pathForURI(uri)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("artifacts: open: %w", err)
	}
	return f, nil
}

// Stat returns size, sha256, and mtime. The digest is computed by
// streaming the file; callers who only need size should avoid Stat on
// huge artifacts or cache the result themselves.
func (s *LocalStore) Stat(ctx context.Context, uri URI) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	path, err := s.pathForURI(uri)
	if err != nil {
		return Metadata{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Metadata{}, ErrNotFound
		}
		return Metadata{}, fmt.Errorf("artifacts: stat: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("artifacts: open for digest: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := copyWithCtx(ctx, h, f); err != nil {
		return Metadata{}, fmt.Errorf("artifacts: digest: %w", err)
	}
	return Metadata{
		Size:      info.Size(),
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		UpdatedAt: info.ModTime(),
	}, nil
}

// Delete removes the artifact. Returns ErrNotFound if it did not exist.
func (s *LocalStore) Delete(ctx context.Context, uri URI) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.pathForURI(uri)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("artifacts: delete: %w", err)
	}
	return nil
}

// pathForURI resolves a file:// URI back to its on-disk path and
// verifies that it sits inside s.root. URIs that escape the root or
// belong to a different scheme are rejected with ErrInvalidURI.
func (s *LocalStore) pathForURI(uri URI) (string, error) {
	u, err := url.Parse(string(uri))
	if err != nil || u.Scheme != "file" {
		return "", ErrInvalidURI
	}
	path := filepath.FromSlash(u.Path)
	// On Windows, url.Parse of "file:///C:/foo" yields Path="/C:/foo".
	// Trim the leading separator so filepath can treat it as absolute.
	if len(path) > 2 && path[0] == filepath.Separator && path[2] == ':' {
		path = path[1:]
	}
	path = filepath.Clean(path)
	rootAbs := filepath.Clean(s.root) + string(filepath.Separator)
	if !strings.HasPrefix(path+string(filepath.Separator), rootAbs) {
		return "", ErrInvalidURI
	}
	return path, nil
}

// MaxKeyLength bounds the accepted key length. S3 permits up to 1024
// bytes; local paths have tighter OS limits. 1024 matches the S3 ceiling
// and is well below any OS PATH_MAX we care about once joined to root.
const MaxKeyLength = 1024

// sanitizeKey rejects keys that would let a caller write outside the
// store root (absolute paths, "..", empty segments), plus keys that
// contain NUL or ASCII control bytes (which can confuse the OS or
// S3-compatible backends), plus oversize keys. It returns a cleaned,
// relative, forward-slash path that is safe to Join onto root.
func sanitizeKey(key string) (string, error) {
	if key == "" {
		return "", ErrInvalidKey
	}
	if len(key) > MaxKeyLength {
		return "", ErrInvalidKey
	}
	// NUL and C0 controls: NUL is a hard stop on POSIX path syscalls
	// and can truncate a key silently in C bindings. The other controls
	// (0x01-0x1F, 0x7F) never belong in an artifact key — reject rather
	// than guess intent. Tab / newline are included in this range.
	for i := 0; i < len(key); i++ {
		if b := key[i]; b == 0 || b < 0x20 || b == 0x7f {
			return "", ErrInvalidKey
		}
	}
	// Forbid backslash entirely: callers must use forward slashes even
	// on Windows so URIs are portable. Internally we convert to OS
	// separator via filepath.Join.
	if strings.ContainsRune(key, '\\') {
		return "", ErrInvalidKey
	}
	if strings.HasPrefix(key, "/") {
		return "", ErrInvalidKey
	}
	// Windows drive letter.
	if len(key) >= 2 && key[1] == ':' {
		return "", ErrInvalidKey
	}
	cleaned := filepath.ToSlash(filepath.Clean(key))
	if cleaned == "." || cleaned == "" {
		return "", ErrInvalidKey
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == ".." {
			return "", ErrInvalidKey
		}
	}
	return cleaned, nil
}

// fileURI turns an absolute OS path into a file:// URI. On Windows we
// need a leading slash before the drive letter so that url.Parse treats
// the path as absolute; on POSIX the path already starts with /.
func fileURI(path string) URI {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return URI(u.String())
}

// copyWithCtx is io.Copy with cooperative cancellation: it checks
// ctx.Err() between chunks so a cancelled upload stops promptly.
func copyWithCtx(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := src.Read(buf)
		if n > 0 {
			m, werr := dst.Write(buf[:n])
			total += int64(m)
			if werr != nil {
				return total, werr
			}
			if m != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
