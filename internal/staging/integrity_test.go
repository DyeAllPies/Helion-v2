package staging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ── happy path: SHA matches, download verified ──────────────────────────

func TestPrepare_VerifiedDownload_HappyPath(t *testing.T) {
	s, store, _ := newStager(t)
	payload := []byte("trusted training data")
	uri := putArtifact(t, store, "ds/train", payload)

	job := &cpb.Job{
		ID: "verified-ok",
		Inputs: []cpb.ArtifactBinding{
			{
				Name:      "TRAIN",
				URI:       string(uri),
				SHA256:    hexSHA(payload),
				LocalPath: "in/train",
			},
		},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(p.Cleanup)

	got, err := os.ReadFile(filepath.Join(p.WorkingDir, "in", "train"))
	if err != nil {
		t.Fatalf("read staged input: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content: %q", got)
	}
}

// ── tamper: store returns different bytes than the committed SHA ────────

// tamperStore forwards all calls to an inner Store except Get, which
// returns bytes that do not match what was Put. Simulates the scenario
// the integrity check is meant to catch: an attacker who can write to
// the artifact store (leaked S3 creds) but not to the coordinator's
// attested ResolvedOutputs record.
type tamperStore struct {
	artifacts.Store
	tamperedBytes []byte
}

func (t tamperStore) Get(_ context.Context, _ artifacts.URI) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(t.tamperedBytes)), nil
}

func TestPrepare_VerifiedDownload_TamperDetected(t *testing.T) {
	workRoot := filepath.Join(t.TempDir(), "work")
	base, err := artifacts.NewLocalStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	original := []byte("what the upstream uploaded")
	uri, err := base.Put(context.Background(), "ds/x",
		bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	s := NewStager(tamperStore{Store: base, tamperedBytes: []byte("malicious swap")},
		workRoot, false, slog.Default())

	job := &cpb.Job{
		ID: "verified-tamper",
		Inputs: []cpb.ArtifactBinding{
			{
				Name:      "X",
				URI:       string(uri),
				SHA256:    hexSHA(original), // committed by upstream
				LocalPath: "in/x",
			},
		},
	}
	_, err = s.Prepare(context.Background(), job)
	if err == nil {
		t.Fatal("expected tamper to be rejected")
	}
	if !errors.Is(err, artifacts.ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got: %v", err)
	}
	// The workdir must have been rolled back so a retry starts clean.
	entries, _ := os.ReadDir(workRoot)
	for _, e := range entries {
		if strings.Contains(e.Name(), "verified-tamper") {
			t.Fatalf("workdir leaked after tamper: %s", e.Name())
		}
	}
}

// ── fallback: empty SHA leaves verification off ─────────────────────────

func TestPrepare_EmptySHA_FallsBackToPlainGet(t *testing.T) {
	s, store, _ := newStager(t)
	payload := []byte("unverified-but-fine")
	uri := putArtifact(t, store, "ds/plain", payload)

	job := &cpb.Job{
		ID: "no-sha",
		Inputs: []cpb.ArtifactBinding{
			{Name: "P", URI: string(uri), LocalPath: "in/p"}, // SHA256 empty
		},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(p.Cleanup)

	got, _ := os.ReadFile(filepath.Join(p.WorkingDir, "in", "p"))
	if !bytes.Equal(got, payload) {
		t.Fatalf("plain-get fallback corrupted bytes: %q", got)
	}
}

// ── wrong-digest-but-honest-store (upstream lied about SHA) ─────────────
//
// The primary threat model (compromised node) means the upstream could
// supply a SHA that matches the bytes it uploaded — in which case the
// check always passes and catches nothing. This test covers a different
// failure mode: data corruption or configuration error where the
// committed SHA is wrong even though no one is being malicious. The
// bytes at the URI are the "real" bytes (byte-identical to what the
// caller expected), but the SHA the caller committed happens to be
// wrong. GetAndVerify correctly flags this — the committed digest is
// the authoritative expectation.

func TestPrepare_WrongDigest_ContentRejected(t *testing.T) {
	s, store, _ := newStager(t)
	payload := []byte("the real bytes")
	uri := putArtifact(t, store, "ds/r", payload)

	job := &cpb.Job{
		ID: "wrong-digest",
		Inputs: []cpb.ArtifactBinding{
			{
				Name:      "R",
				URI:       string(uri),
				SHA256:    hexSHA([]byte("different-bytes")),
				LocalPath: "in/r",
			},
		},
	}
	_, err := s.Prepare(context.Background(), job)
	if !errors.Is(err, artifacts.ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got: %v", err)
	}
}
