// internal/logstore/scrub_test.go
//
// Feature 29 — scrubber unit tests. Every table row is a single
// (chunk, secrets) invariant the substitution pass must hold.
// The decorator integration test lives in the api package
// (exercises the full submit-chunk-GET flow).

package logstore_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/logstore"
)

// ── Pure helper ───────────────────────────────────────────

func TestScrub_SingleValue_ReplacedOnce(t *testing.T) {
	chunk := []byte("token=hf_sekret blah\n")
	got := logstore.Scrub(chunk, []string{"hf_sekret"})
	want := []byte("token=[REDACTED] blah\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestScrub_MultipleValues_AllReplaced(t *testing.T) {
	chunk := []byte("aws=AKIA1234 hf=hf_sekret dupe=hf_sekret")
	got := logstore.Scrub(chunk, []string{"hf_sekret", "AKIA1234"})
	s := string(got)
	if strings.Contains(s, "hf_sekret") {
		t.Errorf("hf_sekret leaked: %q", s)
	}
	if strings.Contains(s, "AKIA1234") {
		t.Errorf("AKIA1234 leaked: %q", s)
	}
	if strings.Count(s, "[REDACTED]") != 3 {
		t.Errorf("want 3 redactions, got %d in %q", strings.Count(s, "[REDACTED]"), s)
	}
}

func TestScrub_ValueNotPresent_NoMutation(t *testing.T) {
	// Regression guard: a secret declared on the job but never
	// printed must not affect the chunk at all. Even the return
	// slice should be the same backing array so we don't
	// allocate on the zero-match hot path.
	chunk := []byte("regular output line")
	got := logstore.Scrub(chunk, []string{"never_printed"})
	if !bytes.Equal(got, chunk) {
		t.Fatalf("got %q, want %q", got, chunk)
	}
}

func TestScrub_EmptyValue_Ignored(t *testing.T) {
	// A zero-length secret value would match every byte
	// position with bytes.ReplaceAll (every position is a
	// zero-length substring match), blowing up the chunk and
	// DoS-ing the write path. The scrubber must skip empty
	// values.
	chunk := []byte("short output")
	got := logstore.Scrub(chunk, []string{"", "never_printed"})
	if !bytes.Equal(got, chunk) {
		t.Fatalf("empty-value DoS: got %q, want unchanged", got)
	}
}

func TestScrub_EmptyChunk_NoPanic(t *testing.T) {
	// Defensive: node runtimes flush empty buffers on some
	// edge cases (line-buffer flush with zero bytes). The
	// scrubber must handle that without a panic.
	got := logstore.Scrub(nil, []string{"hf_sekret"})
	if len(got) != 0 {
		t.Fatalf("nil chunk: got %q, want empty", got)
	}
	got = logstore.Scrub([]byte{}, []string{"hf_sekret"})
	if len(got) != 0 {
		t.Fatalf("empty chunk: got %q, want empty", got)
	}
}

func TestScrub_EmptySecrets_PassThrough(t *testing.T) {
	chunk := []byte("anything")
	got := logstore.Scrub(chunk, nil)
	if !bytes.Equal(got, chunk) {
		t.Fatalf("nil secrets: got %q, want unchanged", got)
	}
	got = logstore.Scrub(chunk, []string{})
	if !bytes.Equal(got, chunk) {
		t.Fatalf("empty secrets: got %q, want unchanged", got)
	}
}

func TestScrub_Idempotent(t *testing.T) {
	// Running the scrubber twice must produce the same output
	// as once. The literal "[REDACTED]" sentinel cannot contain
	// any secret value (the secret would have to be "[REDACT]"
	// shape for that pathology, which we consider out-of-scope
	// since an operator declaring their secret as that exact
	// string is deliberately poisoning the scrubber).
	chunk := []byte("token=abc123 end")
	once := logstore.Scrub(chunk, []string{"abc123"})
	twice := logstore.Scrub(once, []string{"abc123"})
	if !bytes.Equal(once, twice) {
		t.Fatalf("not idempotent: once=%q twice=%q", once, twice)
	}
}

func TestScrub_OverlappingValues_DeterministicWinner(t *testing.T) {
	// Two secrets where one is a prefix of the other. First
	// rule from the secrets slice wins. Guards against a
	// non-deterministic map-iteration bug if the impl ever
	// changes to a map-based implementation.
	chunk := []byte("abcdef")
	got := string(logstore.Scrub(chunk, []string{"abc", "abcdef"}))
	// After replacing "abc" first, "abcdef" no longer matches
	// the remaining bytes "def".
	if got != "[REDACTED]def" {
		t.Errorf("overlap: got %q, want %q", got, "[REDACTED]def")
	}
}

// ── Decorator ─────────────────────────────────────────────

func TestScrubbingStore_Append_UsesLookup(t *testing.T) {
	inner := logstore.NewMemLogStore()
	calls := 0
	lookup := func(jobID string) ([]string, bool) {
		calls++
		if jobID == "secret-job" {
			return []string{"hf_sekret"}, true
		}
		return nil, false
	}
	store := logstore.NewScrubbingStore(inner, lookup)

	// Secret-bearing job: written chunk is scrubbed.
	_ = store.Append(context.Background(), logstore.LogEntry{
		JobID: "secret-job",
		Seq:   1,
		Data:  "token=hf_sekret end",
	})
	entries, _ := inner.Get(context.Background(), "secret-job")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if strings.Contains(entries[0].Data, "hf_sekret") {
		t.Errorf("plaintext leaked to inner store: %q", entries[0].Data)
	}
	if !strings.Contains(entries[0].Data, "[REDACTED]") {
		t.Errorf("no redaction: %q", entries[0].Data)
	}

	// Regular job: lookup returns (nil, false), chunk passes
	// through verbatim.
	_ = store.Append(context.Background(), logstore.LogEntry{
		JobID: "plain-job",
		Seq:   1,
		Data:  "regular output",
	})
	entries, _ = inner.Get(context.Background(), "plain-job")
	if len(entries) != 1 || entries[0].Data != "regular output" {
		t.Errorf("plain passthrough broken: %+v", entries)
	}

	if calls != 2 {
		t.Errorf("want 2 lookup calls, got %d", calls)
	}
}

func TestScrubbingStore_NilLookup_Passthrough(t *testing.T) {
	// Dev-mode wiring where no JobStore is configured: nil
	// lookup must not crash. The decorator degrades to a
	// pass-through.
	inner := logstore.NewMemLogStore()
	store := logstore.NewScrubbingStore(inner, nil)
	_ = store.Append(context.Background(), logstore.LogEntry{
		JobID: "j", Seq: 1, Data: "raw",
	})
	entries, _ := inner.Get(context.Background(), "j")
	if len(entries) != 1 || entries[0].Data != "raw" {
		t.Errorf("nil-lookup passthrough broken: %+v", entries)
	}
}

func TestScrubbingStore_Get_PassesThrough(t *testing.T) {
	// Reads are unscrubbed by the decorator — the response-
	// path redactor in the api package handles the GET side.
	// This test guards the contract so a future "double scrub"
	// refactor doesn't accidentally break the API-layer
	// expectation.
	inner := logstore.NewMemLogStore()
	_ = inner.Append(context.Background(), logstore.LogEntry{
		JobID: "j", Seq: 1, Data: "raw",
	})
	store := logstore.NewScrubbingStore(inner, func(string) ([]string, bool) {
		return []string{"raw"}, true
	})
	entries, _ := store.Get(context.Background(), "j")
	if len(entries) != 1 || entries[0].Data != "raw" {
		t.Errorf("Get should pass through unscrubbed, got %+v", entries)
	}
}
