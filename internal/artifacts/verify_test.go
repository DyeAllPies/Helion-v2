package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// ── VerifyStore ─────────────────────────────────────────────────────────

func TestVerifyStore_HappyPath(t *testing.T) {
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	if err := VerifyStore(context.Background(), store); err != nil {
		t.Fatalf("VerifyStore: %v", err)
	}
	// Probe artifact must not linger after success — scan the
	// helion-probe/ prefix under the root and fail if anything is
	// still there.
	probeDir := filepath.Join(store.Root(), "helion-probe")
	entries, _ := filepath.Glob(probeDir + string(filepath.Separator) + "*")
	if len(entries) > 0 {
		t.Fatalf("probe artifact lingered: %+v", entries)
	}
}

// failingStore implements Store but Put/Get/Delete always error, so
// VerifyStore should fail at the Put step without swallowing the error.
type failingStore struct {
	putErr  error
	getErr  error
	content []byte
}

func (f *failingStore) Put(_ context.Context, _ string, r io.Reader, _ int64) (URI, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	b, _ := io.ReadAll(r)
	f.content = b
	return "fake://", nil
}
func (f *failingStore) Get(_ context.Context, _ URI) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return io.NopCloser(bytes.NewReader(f.content)), nil
}
func (f *failingStore) Stat(_ context.Context, _ URI) (Metadata, error) { return Metadata{}, nil }
func (f *failingStore) Delete(_ context.Context, _ URI) error            { return nil }

func TestVerifyStore_PutFailurePropagated(t *testing.T) {
	err := VerifyStore(context.Background(), &failingStore{putErr: errors.New("dial refused")})
	if err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("expected dial refused, got %v", err)
	}
}

func TestVerifyStore_GetFailurePropagated(t *testing.T) {
	err := VerifyStore(context.Background(), &failingStore{getErr: errors.New("network down")})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("expected network down, got %v", err)
	}
}

// corruptingStore returns different bytes from what it stored — the
// round-trip check must detect the tamper.
type corruptingStore struct{ failingStore }

func (c *corruptingStore) Get(_ context.Context, _ URI) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("different-bytes"))), nil
}

func TestVerifyStore_DetectsTamper(t *testing.T) {
	err := VerifyStore(context.Background(), &corruptingStore{})
	if err == nil || !strings.Contains(err.Error(), "round-trip mismatch") {
		t.Fatalf("expected round-trip mismatch, got %v", err)
	}
}

// ── GetAndVerify ────────────────────────────────────────────────────────

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestGetAndVerify_HappyPath(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "g"))
	payload := []byte("expected-bytes")
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))

	got, err := GetAndVerify(context.Background(), store, uri, shaHex(payload), 0)
	if err != nil {
		t.Fatalf("GetAndVerify: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content: %q", got)
	}
}

func TestGetAndVerify_UpperCaseDigestAccepted(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "g"))
	payload := []byte("X")
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), 1)
	upper := strings.ToUpper(shaHex(payload))
	if _, err := GetAndVerify(context.Background(), store, uri, upper, 0); err != nil {
		t.Fatalf("should accept uppercase: %v", err)
	}
}

func TestGetAndVerify_ChecksumMismatch(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "g"))
	payload := []byte("abc")
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), 3)
	wrong := shaHex([]byte("different"))
	got, err := GetAndVerify(context.Background(), store, uri, wrong, 0)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if got != nil {
		t.Fatalf("mismatched read must return nil bytes, got %d", len(got))
	}
}

func TestGetAndVerify_MissingExpectedRejected(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "g"))
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader([]byte("x")), 1)
	if _, err := GetAndVerify(context.Background(), store, uri, "", 0); err == nil {
		t.Fatal("empty expected sha must be rejected")
	}
}

func TestGetAndVerify_MaxBytesCapEnforced(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "g"))
	payload := []byte("1234567890")
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))
	got, err := GetAndVerify(context.Background(), store, uri, shaHex(payload), 5)
	if err == nil {
		t.Fatalf("oversize read should error, got %d bytes", len(got))
	}
	if !strings.Contains(err.Error(), "size >") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestGetAndVerify_NotFound(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "g"))
	absent := fileURI(filepath.Join(store.Root(), "nope"))
	_, err := GetAndVerify(context.Background(), store, absent, shaHex([]byte("x")), 0)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
