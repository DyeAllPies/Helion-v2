// tests/integration/artifacts_live_s3_test.go
//
// Feature 11 — live-MinIO integration tests.
//
// These tests exercise the artifact store against a real S3-compatible
// endpoint (MinIO, brought up by docker-compose --profile ml). They are
// skipped unless MINIO_TEST_ENDPOINT is set so `go test ./...` stays
// hermetic; scripts/run-e2e.sh exports the env after the compose
// cluster is healthy.
//
// What's covered here that the existing unit tests don't cover:
//
//   - Real network + TLS-less handshake to MinIO (unit tests use the
//     in-memory fakeS3).
//   - ConfigFromEnv → Open → Store → Put/Get/Stat/Delete round-trip
//     using the exact env var names the production coordinator reads.
//   - VerifyStore startup probe against a real endpoint (unit test
//     uses an in-memory Store; this confirms the probe talks to the
//     real minio-go client correctly).
//   - GetAndVerifyTo streaming path against a real endpoint, so we
//     catch regressions in how the S3Store's Get reader interacts
//     with the TeeReader + size-cap logic.
//
// What's NOT covered here (tracked in the feature 12/13 audit):
//   - A running node agent using the Stager to upload a job's outputs
//     and the coordinator resolving them on a downstream job. That's
//     the feature 12/13 integration test surface; this file keeps to
//     feature 11's own contract.

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
)

// skipUnlessLiveMinIO skips the test unless the env signalling a
// live MinIO cluster is set. Mirrors the gate
// internal/artifacts/s3_test.go:TestS3_LiveIntegration uses so the
// two run together or not at all.
func skipUnlessLiveMinIO(t *testing.T) artifacts.Config {
	t.Helper()
	if os.Getenv("MINIO_TEST_ENDPOINT") == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set; skipping live-MinIO integration test")
	}
	return artifacts.Config{
		Backend:     "s3",
		S3Endpoint:  os.Getenv("MINIO_TEST_ENDPOINT"),
		S3Bucket:    os.Getenv("MINIO_TEST_BUCKET"),
		S3AccessKey: os.Getenv("MINIO_TEST_ACCESS"),
		S3SecretKey: os.Getenv("MINIO_TEST_SECRET"),
		S3UseSSL:    os.Getenv("MINIO_TEST_SSL") == "1",
	}
}

// TestLiveS3ArtifactRoundtrip opens a Store from a Config that
// mirrors the production env-var shape, runs one full
// Put → Stat → Get round-trip, and asserts the SHA-256 that
// LocalStore's Stat returns equals what we wrote. This is the
// smallest possible test that proves the compose-provisioned MinIO
// is actually reachable and the coordinator's env-var wiring maps
// to a working store.
func TestLiveS3ArtifactRoundtrip(t *testing.T) {
	cfg := skipUnlessLiveMinIO(t)

	store, err := artifacts.Open(cfg)
	if err != nil {
		t.Fatalf("artifacts.Open(%+v): %v", cfg, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Unique per-run key so parallel runs + re-runs don't collide. 0.1 s
	// resolution is enough — `go test` doesn't retry an individual test
	// within the same second.
	key := "integration/roundtrip/" + time.Now().UTC().Format("20060102T150405.000")
	payload := bytes.Repeat([]byte("helion-feature-11-"), 512)

	uri, err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), uri) })

	md, err := store.Stat(ctx, uri)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	want := sha256.Sum256(payload)
	if md.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("SHA256 mismatch: got %q want %q", md.SHA256, hex.EncodeToString(want[:]))
	}
	if md.Size != int64(len(payload)) {
		t.Errorf("Size mismatch: got %d want %d", md.Size, len(payload))
	}

	rc, err := store.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch (%d vs %d bytes)", len(got), len(payload))
	}
}

// TestLiveS3ArtifactVerifyStoreProbe exercises the startup probe
// the node agent calls before accepting jobs. A misconfigured bucket
// / bad creds / unreachable endpoint are what this probe is meant
// to catch, so we also run a negative case with a bad bucket name
// and assert it fails with a useful error.
func TestLiveS3ArtifactVerifyStoreProbe(t *testing.T) {
	cfg := skipUnlessLiveMinIO(t)

	t.Run("happy_path", func(t *testing.T) {
		store, err := artifacts.Open(cfg)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := artifacts.VerifyStore(ctx, store); err != nil {
			t.Fatalf("VerifyStore against live MinIO failed: %v", err)
		}
	})

	t.Run("wrong_bucket_surfaces_error", func(t *testing.T) {
		bad := cfg
		bad.S3Bucket = "definitely-not-a-real-bucket-" + time.Now().UTC().Format("20060102150405")
		store, err := artifacts.Open(bad)
		if err != nil {
			// Some backends reject unknown buckets at Open time; that's
			// also an acceptable outcome — the probe is meant to surface
			// misconfig either way. Pass the test.
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := artifacts.VerifyStore(ctx, store); err == nil {
			t.Fatal("VerifyStore should fail against a non-existent bucket")
		}
	})
}

// TestLiveS3LargeObjectRoundtrip uploads and reads back a 10 MiB
// payload. minio-go's Put path may exercise multipart semantics
// above an internal threshold; the unit-level fakeS3 doesn't model
// multipart at all, so a regression in how S3Store constructs the
// PutObject call — e.g. a truncated Content-Length header, a bad
// reader position, or a goroutine that closes the reader early —
// only surfaces against a real endpoint. This test catches that
// class of regression.
//
// 10 MiB is the smallest payload that's meaningfully "big" for CI
// (takes ~0.5 s to stream over the loopback to the containerised
// MinIO); large enough to make any streaming regression visible,
// small enough that a CI run doesn't feel it.
func TestLiveS3LargeObjectRoundtrip(t *testing.T) {
	cfg := skipUnlessLiveMinIO(t)

	store, err := artifacts.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 10 MiB deterministic-but-non-trivial pattern (incrementing
	// byte) so a truncation partway through shows up as both a
	// Size mismatch and a SHA-256 mismatch on Stat.
	payload := make([]byte, 10<<20)
	for i := range payload {
		payload[i] = byte(i)
	}

	key := "integration/large/" + time.Now().UTC().Format("20060102T150405.000")
	uri, err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put (10 MiB): %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), uri) })

	md, err := store.Stat(ctx, uri)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if md.Size != int64(len(payload)) {
		t.Fatalf("Size after 10 MiB Put: got %d, want %d", md.Size, len(payload))
	}
	want := sha256.Sum256(payload)
	if md.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("SHA256 after 10 MiB Put: mismatch")
	}

	// Verify bytes come back byte-for-byte. Hash-compare rather
	// than bytes.Equal to keep any failure log sane.
	rc, err := store.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	hasher := sha256.New()
	n, err := io.Copy(hasher, rc)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("Get returned %d bytes, want %d", n, len(payload))
	}
	if !bytes.Equal(hasher.Sum(nil), want[:]) {
		t.Fatalf("10 MiB round-trip content mismatch")
	}
}

// TestLiveS3GetAndVerifyToStream exercises the streaming-verify path
// used by the Stager when pulling inputs for a job. The unit test
// covers the happy path against a LocalStore; this run proves the
// S3Store's Get reader interacts correctly with the TeeReader + 64
// KiB streaming cap regardless of object size.
func TestLiveS3GetAndVerifyToStream(t *testing.T) {
	cfg := skipUnlessLiveMinIO(t)

	store, err := artifacts.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1 MiB payload — forces the streaming loop to iterate at least 16
	// times (64 KiB chunks), but still fits comfortably in CI memory.
	payload := bytes.Repeat([]byte("A"), 1<<20)
	key := "integration/stream/" + time.Now().UTC().Format("20060102T150405.000")
	uri, err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), uri) })

	want := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(want[:])

	var sink bytes.Buffer
	n, err := artifacts.GetAndVerifyTo(ctx, store, uri, wantHex, int64(len(payload))+1024, &sink)
	if err != nil {
		t.Fatalf("GetAndVerifyTo: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("streamed %d bytes, wanted %d", n, len(payload))
	}
	if !bytes.Equal(sink.Bytes(), payload) {
		t.Fatalf("streamed payload mismatch")
	}

	// Negative case: a wrong digest must cause the stream helper to
	// return an error so the caller can reject the bytes.
	var sink2 bytes.Buffer
	_, err = artifacts.GetAndVerifyTo(ctx, store, uri, "deadbeef"+wantHex[8:], int64(len(payload))+1024, &sink2)
	if err == nil {
		t.Fatal("GetAndVerifyTo should have rejected a mismatched digest")
	}
}
