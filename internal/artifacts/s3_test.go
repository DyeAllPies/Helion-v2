package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// fakeS3 is an in-memory s3Client. It covers just enough of the S3
// surface to exercise S3Store logic without standing up MinIO.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]fakeObject
	// putErr / getErr / statErr / removeErr let individual tests
	// simulate backend failures on a specific call.
	putErr    error
	getErr    error
	statErr   error
	removeErr error
}

type fakeObject struct {
	data     []byte
	modified time.Time
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string]fakeObject{}}
}

func (f *fakeS3) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
	if f.putErr != nil {
		return minio.UploadInfo{}, f.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return minio.UploadInfo{}, err
	}
	f.mu.Lock()
	f.objects[bucket+"/"+key] = fakeObject{data: b, modified: time.Now()}
	f.mu.Unlock()
	return minio.UploadInfo{Bucket: bucket, Key: key, Size: int64(len(b))}, nil
}

func (f *fakeS3) GetObject(ctx context.Context, bucket, key string, _ minio.GetObjectOptions) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	obj, ok := f.objects[bucket+"/"+key]
	f.mu.Unlock()
	if !ok {
		return nil, noSuchKey()
	}
	return io.NopCloser(bytes.NewReader(obj.data)), nil
}

func (f *fakeS3) StatObject(ctx context.Context, bucket, key string, _ minio.StatObjectOptions) (minio.ObjectInfo, error) {
	if f.statErr != nil {
		return minio.ObjectInfo{}, f.statErr
	}
	f.mu.Lock()
	obj, ok := f.objects[bucket+"/"+key]
	f.mu.Unlock()
	if !ok {
		return minio.ObjectInfo{}, noSuchKey()
	}
	return minio.ObjectInfo{Key: key, Size: int64(len(obj.data)), LastModified: obj.modified}, nil
}

func (f *fakeS3) RemoveObject(ctx context.Context, bucket, key string, _ minio.RemoveObjectOptions) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.mu.Lock()
	delete(f.objects, bucket+"/"+key)
	f.mu.Unlock()
	return nil
}

// noSuchKey returns a minio.ErrorResponse shaped exactly like what a
// real S3 backend sends for a missing object. ToErrorResponse in s3.go
// inspects .Code, so that's the field we set.
func noSuchKey() error {
	return minio.ErrorResponse{
		StatusCode: http.StatusNotFound,
		Code:       "NoSuchKey",
		Message:    "The specified key does not exist.",
	}
}

// ---------- tests ----------

func newS3(t *testing.T) (*S3Store, *fakeS3) {
	t.Helper()
	f := newFakeS3()
	return newS3StoreWithClient("helion", f), f
}

func TestS3_PutGetRoundTrip(t *testing.T) {
	s, _ := newS3(t)
	ctx := context.Background()
	payload := []byte("hello s3")
	uri, err := s.Put(ctx, "run/1/model.pt", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, want := string(uri), "s3://helion/run/1/model.pt"; got != want {
		t.Fatalf("URI: got %q want %q", got, want)
	}
	rc, err := s.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: %q", got)
	}
}

func TestS3_Stat(t *testing.T) {
	s, _ := newS3(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte{'Z'}, 1024)
	uri, err := s.Put(ctx, "k", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	md, err := s.Stat(ctx, uri)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if md.Size != int64(len(payload)) {
		t.Fatalf("size: %d", md.Size)
	}
	if md.SHA256 != sha(payload) {
		t.Fatalf("sha256: %s vs %s", md.SHA256, sha(payload))
	}
	if md.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt zero")
	}
}

func TestS3_GetStatDeleteNotFound(t *testing.T) {
	s, _ := newS3(t)
	missing := URI("s3://helion/absent")
	if _, err := s.Get(context.Background(), missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if _, err := s.Stat(context.Background(), missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat: %v", err)
	}
	if err := s.Delete(context.Background(), missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: %v", err)
	}
}

func TestS3_Delete(t *testing.T) {
	s, _ := newS3(t)
	ctx := context.Background()
	uri, _ := s.Put(ctx, "x", bytes.NewReader([]byte("x")), 1)
	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, uri); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
}

func TestS3_InvalidKeys(t *testing.T) {
	s, _ := newS3(t)
	bad := []string{"", "/abs", "../esc", "a/..", "c:\\x"}
	for _, k := range bad {
		if _, err := s.Put(context.Background(), k, bytes.NewReader(nil), 0); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q: %v", k, err)
		}
	}
}

func TestS3_URI_RejectsWrongBucketAndScheme(t *testing.T) {
	s, _ := newS3(t)
	cases := []URI{
		"s3://other-bucket/key",
		"file:///tmp/x",
		"",
		"s3://helion/",
	}
	for _, u := range cases {
		if _, err := s.Stat(context.Background(), u); !errors.Is(err, ErrInvalidURI) {
			t.Errorf("uri %q: %v", u, err)
		}
	}
}

func TestS3_PutError_Propagated(t *testing.T) {
	s, f := newS3(t)
	f.putErr = errors.New("connection refused")
	_, err := s.Put(context.Background(), "k", bytes.NewReader([]byte("x")), 1)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Put error not propagated: %v", err)
	}
}

func TestS3_GetError_NonNotFoundPropagated(t *testing.T) {
	s, f := newS3(t)
	f.getErr = errors.New("network down")
	_, err := s.Get(context.Background(), "s3://helion/k")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

func TestS3_ContextCancelled(t *testing.T) {
	s, _ := newS3(t)
	// Seed an object so the cancellation path has to trip during the
	// digest stream, not during Stat.
	ctx := context.Background()
	uri, _ := s.Put(ctx, "k", bytes.NewReader([]byte("abc")), 3)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Stat(cctx, uri); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestNewS3Store_RequiresEndpointAndBucket(t *testing.T) {
	if _, err := NewS3Store(S3Config{}); err == nil {
		t.Fatal("expected endpoint error")
	}
	if _, err := NewS3Store(S3Config{Endpoint: "minio:9000"}); err == nil {
		t.Fatal("expected bucket error")
	}
	if _, err := NewS3Store(S3Config{Endpoint: "minio:9000", Bucket: "b"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewS3Store_WarnsWhenUseSSLDisabled captures slog output and
// confirms the WARN line fires whenever an S3Store is constructed
// with UseSSL=false. This is the alarm an operator sees on every
// node startup if they forget `HELION_ARTIFACTS_S3_USE_SSL=1` in
// production — a silent regression (the Warn gets removed or
// gated) would let artifact traffic run in the clear without any
// visible signal, a harvest-now-decrypt-later risk that
// docs/SECURITY.md §3 explicitly calls out.
//
// The matching negative case (UseSSL=true produces no WARN) is
// covered implicitly by the whole test suite running without
// emitting the warning string.
func TestNewS3Store_WarnsWhenUseSSLDisabled(t *testing.T) {
	// Capture slog output to an in-memory buffer. Using the JSON
	// handler makes the assertions robust against cosmetic
	// reordering of attributes.
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, err := NewS3Store(S3Config{
		Endpoint: "minio:9000",
		Bucket:   "helion",
		UseSSL:   false,
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("expected a WARN-level record, got:\n%s", out)
	}
	if !strings.Contains(out, "S3 backend configured without TLS") {
		t.Errorf("expected the documented warning message in the log, got:\n%s", out)
	}
	if !strings.Contains(out, "HELION_ARTIFACTS_S3_USE_SSL=1") {
		t.Errorf("expected the remediation hint in the log, got:\n%s", out)
	}

	// Sanity: with UseSSL=true, no WARN about TLS should fire.
	buf.Reset()
	_, err = NewS3Store(S3Config{
		Endpoint: "s3.amazonaws.com",
		Bucket:   "helion",
		UseSSL:   true,
	})
	if err != nil {
		t.Fatalf("NewS3Store (UseSSL=true): %v", err)
	}
	if strings.Contains(buf.String(), "S3 backend configured without TLS") {
		t.Errorf("unexpected TLS warning when UseSSL=true:\n%s", buf.String())
	}
}

// TestS3_LiveIntegration runs against a real S3-compatible endpoint if
// MINIO_TEST_ENDPOINT is set. Skipped otherwise so unit runs stay
// hermetic. Set:
//
//	MINIO_TEST_ENDPOINT=localhost:9000
//	MINIO_TEST_BUCKET=helion-test
//	MINIO_TEST_ACCESS=minioadmin
//	MINIO_TEST_SECRET=minioadmin
func TestS3_LiveIntegration(t *testing.T) {
	ep := os.Getenv("MINIO_TEST_ENDPOINT")
	if ep == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set; skipping live S3 test")
	}
	cfg := S3Config{
		Endpoint:  ep,
		Bucket:    os.Getenv("MINIO_TEST_BUCKET"),
		AccessKey: os.Getenv("MINIO_TEST_ACCESS"),
		SecretKey: os.Getenv("MINIO_TEST_SECRET"),
		UseSSL:    truthy(os.Getenv("MINIO_TEST_SSL")),
	}
	s, err := NewS3Store(cfg)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	ctx := context.Background()
	key := "unit-test/" + time.Now().UTC().Format("20060102T150405.000000000")
	payload := []byte("live s3 round-trip")

	uri, err := s.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(ctx, uri) })

	rc, err := s.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: %q", got)
	}
	md, err := s.Stat(ctx, uri)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if md.Size != int64(len(payload)) || md.SHA256 != sha(payload) {
		t.Fatalf("metadata: %+v", md)
	}
}
