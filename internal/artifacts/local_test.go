package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
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
