package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) *LocalStore {
	t.Helper()
	s, err := NewLocalStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return s
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestNewLocalStore_CreatesRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	s, err := NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	if _, err := os.Stat(s.Root()); err != nil {
		t.Fatalf("root not created: %v", err)
	}
	if !filepath.IsAbs(s.Root()) {
		t.Fatalf("root not absolute: %q", s.Root())
	}
}

func TestNewLocalStore_EmptyDir(t *testing.T) {
	if _, err := NewLocalStore(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	payload := []byte("hello world")

	uri, err := s.Put(ctx, "run-1/model.bin", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(string(uri), "file://") {
		t.Fatalf("unexpected scheme: %q", uri)
	}

	rc, err := s.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, payload)
	}
}

func TestPut_UnknownSize(t *testing.T) {
	s := newStore(t)
	payload := []byte("streamed")
	uri, err := s.Put(context.Background(), "a/b.dat", bytes.NewReader(payload), -1)
	if err != nil {
		t.Fatalf("Put unknown size: %v", err)
	}
	md, err := s.Stat(context.Background(), uri)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if md.Size != int64(len(payload)) {
		t.Fatalf("size mismatch: got %d want %d", md.Size, len(payload))
	}
}

func TestPut_SizeMismatch_LeavesNoFile(t *testing.T) {
	s := newStore(t)
	payload := []byte("abc")
	_, err := s.Put(context.Background(), "x/y.bin", bytes.NewReader(payload), 999)
	if err == nil {
		t.Fatal("expected size-mismatch error")
	}
	// The final path must not exist, and neither should any tempfile.
	final := filepath.Join(s.Root(), "x", "y.bin")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final path exists after failed Put: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), "x"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".helion-artifact-") {
			t.Fatalf("tempfile left behind: %s", e.Name())
		}
	}
}

func TestPut_OverwriteLastWriterWins(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := []byte("first")
	second := []byte("second-and-longer")

	u1, err := s.Put(ctx, "k", bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatalf("Put1: %v", err)
	}
	u2, err := s.Put(ctx, "k", bytes.NewReader(second), int64(len(second)))
	if err != nil {
		t.Fatalf("Put2: %v", err)
	}
	if u1 != u2 {
		t.Fatalf("URIs differ for same key: %q vs %q", u1, u2)
	}
	md, err := s.Stat(ctx, u2)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if md.SHA256 != sha(second) {
		t.Fatalf("digest mismatch: got %s want %s", md.SHA256, sha(second))
	}
}

func TestPut_InvalidKeys(t *testing.T) {
	s := newStore(t)
	bad := []string{
		"",
		"/absolute",
		"../escape",
		"a/../../escape",
		"./",
		".",
		"with\\backslash",
		"C:/drive",
		"nul\x00byte",
		"tab\there",
		"newline\nhere",
		"carriage\rreturn",
		"del\x7f",
		"bell\x07",
	}
	for _, k := range bad {
		if _, err := s.Put(context.Background(), k, bytes.NewReader(nil), 0); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q: expected ErrInvalidKey, got %v", k, err)
		}
	}
}

func TestPut_OversizeKeyRejected(t *testing.T) {
	s := newStore(t)
	big := strings.Repeat("a", MaxKeyLength+1)
	if _, err := s.Put(context.Background(), big, bytes.NewReader(nil), 0); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("oversize key: got %v want ErrInvalidKey", err)
	}
	// A key well under MaxKeyLength but with a long leaf must still
	// pass sanitisation. We stay clear of Windows MAX_PATH (260) so
	// the on-disk rename succeeds cross-platform — the OS path limit
	// is unrelated to the key-length policy enforced by sanitizeKey.
	ok := strings.Repeat("b", 100)
	if _, err := s.Put(context.Background(), ok, bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("under-limit key rejected: %v", err)
	}
}

func TestPut_NestedKeyCreatesDirs(t *testing.T) {
	s := newStore(t)
	uri, err := s.Put(context.Background(), "a/b/c/d.bin", bytes.NewReader([]byte("x")), 1)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.Stat(context.Background(), uri); err != nil {
		t.Fatalf("Stat: %v", err)
	}
}

func TestStat_DigestAndSize(t *testing.T) {
	s := newStore(t)
	payload := bytes.Repeat([]byte("ABCD"), 4096) // 16 KiB
	uri, err := s.Put(context.Background(), "big.dat", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	md, err := s.Stat(context.Background(), uri)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if md.Size != int64(len(payload)) {
		t.Fatalf("size: got %d want %d", md.Size, len(payload))
	}
	if md.SHA256 != sha(payload) {
		t.Fatalf("sha256: got %s want %s", md.SHA256, sha(payload))
	}
	if md.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt zero")
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newStore(t)
	missing := fileURI(filepath.Join(s.Root(), "nope"))
	if _, err := s.Get(context.Background(), missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: got %v want ErrNotFound", err)
	}
	if _, err := s.Stat(context.Background(), missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat missing: got %v want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	uri, err := s.Put(ctx, "d.bin", bytes.NewReader([]byte("bye")), 3)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, uri); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if err := s.Delete(ctx, uri); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing: got %v want ErrNotFound", err)
	}
}

func TestURI_RejectsOutsideRoot(t *testing.T) {
	s := newStore(t)
	outside := fileURI("/etc/passwd")
	if _, err := s.Get(context.Background(), outside); !errors.Is(err, ErrInvalidURI) {
		t.Fatalf("outside root: got %v want ErrInvalidURI", err)
	}
}

func TestURI_RejectsWrongScheme(t *testing.T) {
	s := newStore(t)
	cases := []URI{
		"s3://bucket/key",
		"http://example.com/file",
		"not a uri at all",
		"",
	}
	for _, u := range cases {
		if _, err := s.Stat(context.Background(), u); !errors.Is(err, ErrInvalidURI) {
			t.Errorf("uri %q: got %v want ErrInvalidURI", u, err)
		}
	}
}

func TestURI_RejectsTraversalViaEncodedDotDot(t *testing.T) {
	s := newStore(t)
	// Craft a URI whose path escapes root via ../. pathForURI cleans the
	// path before the prefix check, so this must be rejected.
	escape := URI("file://" + filepath.ToSlash(filepath.Join(s.Root(), "..", "escaped")))
	if _, err := s.Get(context.Background(), escape); !errors.Is(err, ErrInvalidURI) {
		t.Fatalf("traversal URI: got %v want ErrInvalidURI", err)
	}
}

func TestPut_ContextCancelled(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A slow reader that would block forever; cancellation must kick in
	// before the first chunk completes.
	r := &slowReader{payload: bytes.Repeat([]byte("x"), 1<<20)}
	_, err := s.Put(ctx, "cancelled", r, -1)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

// slowReader returns 1 byte per Read so copyWithCtx has many chances
// to observe a cancelled context.
type slowReader struct {
	payload []byte
	pos     int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.payload) {
		return 0, io.EOF
	}
	p[0] = r.payload[r.pos]
	r.pos++
	return 1, nil
}

// TestLocalStore_Permissions locks in the 0o700 root / 0o600 file
// invariant the feature 11 spec calls out. Model checkpoints + raw
// training data can be sensitive; a regression that widens the
// root's mode (for instance, dropping to os.MkdirAll's default
// 0o755) silently exposes every artifact the process produces to
// any local user. This test is the alarm for that class of
// regression.
//
// Skipped on Windows — NTFS' Unix-mode bit emulation is a known
// source of flakes and the bits aren't security-load-bearing on
// Windows anyway. The production deployment runs on Linux nodes.
func TestLocalStore_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not security-load-bearing on Windows")
	}

	s := newStore(t)
	ctx := context.Background()

	// (a) Root directory mode is exactly 0o700.
	rootStat, err := os.Stat(s.Root())
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if m := rootStat.Mode().Perm(); m != 0o700 {
		t.Errorf("root permission bits: got %#o, want 0o700", m)
	}

	// (b) A freshly-Put file has mode 0o600.
	payload := []byte("permission-check payload")
	uri, err := s.Put(ctx, "perms/test-file", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// uri is file:///abs/path — translate back to a filesystem path.
	u, err := url.Parse(string(uri))
	if err != nil {
		t.Fatalf("parse URI %q: %v", uri, err)
	}
	filePath := filepath.FromSlash(u.Path)
	fi, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat file %q: %v", filePath, err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("file permission bits: got %#o, want 0o600", m)
	}

	// (c) A nested-key intermediate directory — created via
	// os.MkdirAll on the Put path — must also be 0o700.
	if _, err := s.Put(ctx, "deep/nested/key", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put nested: %v", err)
	}
	nestedDir := filepath.Join(s.Root(), "deep", "nested")
	nestedStat, err := os.Stat(nestedDir)
	if err != nil {
		t.Fatalf("Stat nested dir: %v", err)
	}
	if m := nestedStat.Mode().Perm(); m != 0o700 {
		t.Errorf("nested dir permission bits: got %#o, want 0o700", m)
	}
}

// TestConcurrentPuts_SameKey_NoOrphanTempfile locks in the
// invariant documented in feature 11's "Deliberately not fixed" #2:
// racing Puts on the same key each materialise + rename their own
// tempfile, last-writer-wins on the destination, and neither tempfile
// survives because os.Rename consumes the source.
//
// If this test ever starts finding a `.helion-artifact-*.tmp` file
// left behind after all goroutines have returned, the invariant has
// regressed and the feature 11 doc's claim is wrong — either the
// claim needs updating or the Put path needs a cleanup fix.
func TestConcurrentPuts_SameKey_NoOrphanTempfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows rename-while-open is not atomic: a racing goroutine
		// that opens the destination before another's os.Rename
		// completes trips MoveFileEx with ERROR_ACCESS_DENIED. The
		// invariant under test (no orphan tempfiles + exactly one
		// resolved file + last-writer-wins) is a POSIX-rename
		// guarantee; on Windows it's a best-effort behavior that
		// no production Helion deployment relies on (coordinator
		// and nodes run on Linux). CI is Linux, so this test
		// exercises the real semantics there.
		t.Skip("Windows rename-while-open is not atomic; test validates POSIX semantics only")
	}
	s := newStore(t)
	const n = 16
	const key = "same-key/target.bin"

	// Each goroutine writes a unique payload (the byte == its index)
	// so we can verify afterwards which one won the rename race. Any
	// one of them is an acceptable "winner" — the invariant we care
	// about is (a) no goroutine returned an error, (b) the final file
	// contains one of the submitted payloads in full, (c) no stray
	// tempfile remains alongside it.
	var wg sync.WaitGroup
	payloads := make([][]byte, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		payloads[i] = bytes.Repeat([]byte{byte(i)}, 4096)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Put(context.Background(), key,
				bytes.NewReader(payloads[i]), int64(len(payloads[i])))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	// (c) The parent directory must contain exactly one file
	// (the rename target) + no tempfiles. os.CreateTemp's pattern
	// is ".helion-artifact-*.tmp" so anything matching is an orphan.
	dir := filepath.Dir(filepath.Join(s.Root(), key))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var orphans []string
	nonTempCount := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".helion-artifact-") && strings.HasSuffix(name, ".tmp") {
			orphans = append(orphans, name)
			continue
		}
		nonTempCount++
	}
	if len(orphans) > 0 {
		t.Errorf("orphan tempfiles left after concurrent Put race: %v", orphans)
	}
	if nonTempCount != 1 {
		t.Errorf("want exactly 1 resolved file in dir, got %d", nonTempCount)
	}

	// (b) The final bytes must equal one of the submitted payloads
	// in full. If the rename swapped halfway through, we'd see a
	// mix and this would fail.
	finalBytes, err := os.ReadFile(filepath.Join(s.Root(), key))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(finalBytes) != 4096 {
		t.Fatalf("final file truncated or corrupt: %d bytes", len(finalBytes))
	}
	winningIdx := int(finalBytes[0]) // payload bytes are all == i
	if winningIdx < 0 || winningIdx >= n {
		t.Fatalf("final file has unrecognised winning index byte %d", winningIdx)
	}
	if !bytes.Equal(finalBytes, payloads[winningIdx]) {
		t.Fatal("final file is a mix of payloads — rename was not atomic")
	}
}

func TestConcurrentPuts_DistinctKeys(t *testing.T) {
	s := newStore(t)
	const n = 32
	var wg sync.WaitGroup
	uris := make([]URI, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte(i)}, 256)
			u, err := s.Put(context.Background(), filepath.ToSlash(filepath.Join("c", "k", itoa(i))), bytes.NewReader(payload), int64(len(payload)))
			uris[i] = u
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		md, err := s.Stat(context.Background(), uris[i])
		if err != nil {
			t.Fatalf("stat %d: %v", i, err)
		}
		if md.Size != 256 {
			t.Fatalf("size %d: %d", i, md.Size)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := make([]byte, 0, 8)
	n := i
	if n < 0 {
		buf = append(buf, '-')
		n = -n
	}
	start := len(buf)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse digits
	for l, r := start, len(buf)-1; l < r; l, r = l+1, r-1 {
		buf[l], buf[r] = buf[r], buf[l]
	}
	return string(buf)
}

func TestFileURI_RoundTripThroughPathForURI(t *testing.T) {
	s := newStore(t)
	uri, err := s.Put(context.Background(), "r/t.bin", bytes.NewReader([]byte("abc")), 3)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The URI should resolve back to a path that actually contains the bytes.
	rc, err := s.Get(context.Background(), uri)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "abc" {
		t.Fatalf("content: %q", got)
	}
}
