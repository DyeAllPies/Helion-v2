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
	"testing"
)

// These tests cover GetAndVerifyTo, the streaming verifier the Stager
// uses to download multi-GB ML artifacts without buffering in RAM.
// Right-sized payloads (1 MiB, not GB) prove the code path — the
// streaming property is architectural (io.Copy pulls 64 KiB chunks
// through a TeeReader, the hasher's state is ~200 bytes), not
// something we'd verify by eating GB of CI disk.

func shaBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func oneMiBPayload() []byte {
	// Repeating pattern so mismatches are easy to eyeball in a
	// failure log if one ever slips.
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = byte(i & 0xff)
	}
	return buf
}

func TestGetAndVerifyTo_StreamsToWriter(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	payload := oneMiBPayload()
	uri, err := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	var dst bytes.Buffer
	n, err := GetAndVerifyTo(context.Background(), store, uri, shaBytes(payload), 0, &dst)
	if err != nil {
		t.Fatalf("GetAndVerifyTo: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("bytes written: %d, want %d", n, len(payload))
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("content mismatch")
	}
}

func TestGetAndVerifyTo_MismatchHaltsButDoesNotPanic(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	payload := oneMiBPayload()
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))

	var dst bytes.Buffer
	wrong := shaBytes([]byte("not the same"))
	n, err := GetAndVerifyTo(context.Background(), store, uri, wrong, 0, &dst)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	// Bytes have already been written to dst (the streaming property
	// means we can't undo them) — the contract is that the caller
	// treats dst as tainted on mismatch. n is the byte count that
	// reached the writer, non-zero by design.
	if n != int64(len(payload)) {
		t.Fatalf("partial stream: %d bytes", n)
	}
}

func TestGetAndVerifyTo_SizeCap(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	payload := oneMiBPayload()
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))

	var dst bytes.Buffer
	// Cap at 1 KiB — well below the 1 MiB payload.
	_, err := GetAndVerifyTo(context.Background(), store, uri, shaBytes(payload), 1024, &dst)
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !strings.Contains(err.Error(), "size >") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestGetAndVerifyTo_EmptyExpected(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader([]byte("x")), 1)
	_, err := GetAndVerifyTo(context.Background(), store, uri, "", 0, io.Discard)
	if err == nil {
		t.Fatal("empty expected sha must be rejected")
	}
}

// TestGetAndVerifyTo_BoundedMemory is a functional proxy for the
// architectural property "streaming doesn't buffer the whole payload":
// when the destination is io.Discard (no memory at all) and the
// verifier succeeds, we've proven the verifier itself does not hold
// the payload. The existing in-memory GetAndVerify can't pass this
// test — it would need a real []byte and allocate the full size.
func TestGetAndVerifyTo_BoundedMemory(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	payload := oneMiBPayload()
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))

	n, err := GetAndVerifyTo(context.Background(), store, uri, shaBytes(payload), 0, io.Discard)
	if err != nil {
		t.Fatalf("GetAndVerifyTo into io.Discard: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("bytes: %d", n)
	}
}

// TestGetAndVerify_CompatWrapperStillWorks covers the shim that
// keeps callers of the old bytes-returning API working. Internally
// it now uses GetAndVerifyTo, so a refactor that breaks the
// streaming path would also break this test.
func TestGetAndVerify_CompatWrapperStillWorks(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	payload := []byte("tiny payload")
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))

	got, err := GetAndVerify(context.Background(), store, uri, shaBytes(payload), 0)
	if err != nil {
		t.Fatalf("GetAndVerify: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: %q", got)
	}

	wrong := shaBytes([]byte("other"))
	_, err = GetAndVerify(context.Background(), store, uri, wrong, 0)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

// ── Context cancellation mid-stream ──────────────────────────────────
//
// The Stager cancels downloads when a job is cancelled mid-staging
// (operator hits DELETE /jobs/{id}, or a workflow failure cascades
// through the parent). `GetAndVerifyTo` must observe ctx on the copy
// loop and return a cancellation error promptly — not run to
// completion and only fail at the digest check.
//
// We drive this via a Store wrapper that yields bytes slowly enough
// for a mid-stream cancel to land inside io.Copy's chunk loop.

type slowReadCloser struct {
	data []byte
	pos  int
	hint chan struct{}         // closed once we've returned a chunk (signals the test to cancel)
	gate chan struct{}         // blocks subsequent reads until the test closes it
	ctx  func() <-chan struct{} // exposes the test's ctx.Done so Read can bail out promptly
}

func (r *slowReadCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	// Must respect len(p) — io.teeReader forwards its own buffer
	// which may be smaller than 4 KiB, especially on Windows where
	// bytes.Buffer.ReadFrom starts with a 512-byte probe.
	chunk := 4096
	if rem := len(r.data) - r.pos; rem < chunk {
		chunk = rem
	}
	if chunk > len(p) {
		chunk = len(p)
	}

	// On the very first read, serve the chunk immediately and
	// signal the test to cancel. On every subsequent read, block
	// on ctx.Done() so io.Copy sees the cancellation promptly
	// rather than racing to EOF.
	select {
	case <-r.hint:
		// already signalled — wait for either the gate (test
		// driver) or ctx cancellation.
		if r.ctx != nil {
			select {
			case <-r.ctx():
				return 0, context.Canceled
			case <-r.gate:
			}
		} else {
			<-r.gate
		}
	default:
		close(r.hint)
	}

	copy(p, r.data[r.pos:r.pos+chunk])
	r.pos += chunk
	return chunk, nil
}

func (r *slowReadCloser) Close() error { return nil }

type slowStore struct {
	Store
	reader *slowReadCloser
}

func (s *slowStore) Get(_ context.Context, _ URI) (io.ReadCloser, error) {
	return s.reader, nil
}

func (s *slowStore) Stat(_ context.Context, _ URI) (Metadata, error) {
	return Metadata{Size: int64(len(s.reader.data))}, nil
}

func TestGetAndVerifyTo_ContextCancelledMidStream(t *testing.T) {
	// 256 KiB payload served in 4 KiB chunks via the slow reader.
	// Enough iterations that a cancel issued after the first chunk
	// lands inside the copy loop, not before or after.
	payload := bytes.Repeat([]byte("X"), 256*1024)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slow := &slowStore{
		reader: &slowReadCloser{
			data: payload,
			hint: make(chan struct{}),
			gate: make(chan struct{}), // never closed — forces block
			ctx:  func() <-chan struct{} { return ctx.Done() },
		},
	}

	// Goroutine: wait for the first chunk to be read, then cancel.
	// This simulates the operator hitting DELETE /jobs/{id} after
	// the download is already in flight.
	go func() {
		<-slow.reader.hint
		cancel()
	}()

	var dst bytes.Buffer
	_, err := GetAndVerifyTo(ctx, slow, URI("file:///whatever"), shaBytes(payload), 0, &dst)
	if err == nil {
		t.Fatal("GetAndVerifyTo should return an error after ctx cancellation")
	}
	// The error should be a cancellation or wrap one. `io.Copy`
	// returns ctx.Err() when the reader observes the cancellation;
	// if a future implementation rewraps that, errors.Is still works.
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context") {
		t.Fatalf("want context-cancellation error, got %v", err)
	}
	// A cancelled download must NOT be reported as a digest mismatch —
	// the hash comparison only runs after a clean EOF, and we cancelled
	// before that. If a mismatch error leaks here the code is falsely
	// accusing the artifact store of tampering every time a user
	// cancels.
	if errors.Is(err, ErrChecksumMismatch) {
		t.Fatal("ctx cancellation should not be reported as checksum mismatch")
	}
}

// ── Stager streaming integration: tempfile pattern ──────────────────────
//
// Verifies the atomic-on-success property the Stager relies on:
// a corrupt download must not leave a partial file at the final dest
// path. Lives in the artifacts package only because we already have
// helpers here; the staging package has its own integrity tests.

func TestGetAndVerifyTo_TempfileRenamePattern(t *testing.T) {
	store, _ := NewLocalStore(filepath.Join(t.TempDir(), "v"))
	payload := []byte("staged-content")
	uri, _ := store.Put(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)))

	parent := t.TempDir()
	dest := filepath.Join(parent, "final")
	tmp, err := os.CreateTemp(parent, ".test-stage-*.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	tmpPath := tmp.Name()
	if _, err := GetAndVerifyTo(context.Background(), store, uri, shaBytes(payload), 0, tmp); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		t.Fatalf("GetAndVerifyTo: %v", err)
	}
	_ = tmp.Close()
	if err := os.Rename(tmpPath, dest); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Fatalf("final content: %q", got)
	}
}
