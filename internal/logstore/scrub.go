// internal/logstore/scrub.go
//
// Feature 29 — stdout/stderr secret scrubbing on the write path
// of the log store.
//
// Role
// ────
// Feature 26 redacts secret env VALUES on every coordinator REST
// response and gates plaintext read-back behind an admin reveal
// endpoint. That closes the "operator reads env via GET /jobs/{id}"
// vector but leaves a second vector open: a job that prints its
// own secret env var to stdout writes plaintext into the log
// store, and any operator with log-read access (or feature 28's
// analytics sink) sees it.
//
// This file adds a substitution-based scrubber the coordinator
// runs on every chunk before persisting. For each known secret
// VALUE attached to the job, the scrubber replaces every
// occurrence with the literal string "[REDACTED]".
//
// Threat model
// ────────────
//
//  Catches: accidental leaks — "debug print left in", library
//  logging that echoes an auth header back, test scaffolding
//  that dumps the environment. The common case.
//
//  Does NOT catch: a malicious job script that mutates the
//  value before printing (e.g. base64-encoding it, splitting
//  it across chunks, xor-masking). That's operator-level trust:
//  a script the operator chose to run can exfiltrate anything
//  the operator's job was trusted with. The coordinator does not
//  claim to sandbox against that.
//
//  Boundary edge case: a secret value that happens to land
//  split across two write chunks (rare — node-side runtimes
//  flush line-buffered) would not match on either chunk alone.
//  We accept this miss rate; a determined attacker would fall
//  through to the "malicious script" case above. Feature 29's
//  response-path redactor (applied at GET /jobs/{id}/logs)
//  gives a second chance against the boundary case by
//  concatenating entries before scrubbing.
//
// Safety properties
// ─────────────────
//
//  1. Empty secret values are ignored. Zero-length bytes would
//     match every position in the chunk and produce an infinite
//     redaction loop; a defensive check skips them.
//
//  2. Deterministic output. The scrubber is a pure function of
//     (chunk, secrets); no locking, no ambient state. Safe to
//     call from concurrent goroutines with the same `secrets`
//     slice.
//
//  3. Fail-safe on store failure. The decorator's lookup returns
//     `(nil, false)` on any JobStore read error (the job might
//     already be terminal and evicted). Fail-open on lookup means
//     "don't scrub" — we prefer NOT dropping logs over a perfect
//     redact. Operators who need fail-closed should keep the
//     response-path redactor enabled as a second pass.
//
//  4. Idempotent. Running scrub twice produces the same output —
//     the literal `[REDACTED]` does not contain any secret value
//     by construction, so the second pass is a no-op. Callers
//     can layer the decorator AND the response-path redactor
//     without double-redacting the sentinel.

package logstore

import (
	"bytes"
	"context"
	"time"
)

// redactedSentinel is the replacement bytes emitted in place of
// any matched secret value. Kept as a package-level constant so
// tests can assert the exact shape and future callers (e.g. the
// dashboard log viewer) can key off the literal.
const redactedSentinel = "[REDACTED]"

// RedactedSentinel exposes the sentinel string for callers that
// need to parse it back out (tests, dashboard badge rendering).
func RedactedSentinel() string { return redactedSentinel }

// SecretsLookup resolves a jobID to the list of secret VALUES
// that should be scrubbed from its log chunks. Returns
// (nil, false) when the job isn't known or has no secrets —
// callers treat that as "don't scrub" (fail-open).
//
// Implementations read from the JobStore. Feature 26 populates
// `cpb.Job.Env` (the plaintext dispatch-time values) and
// `cpb.Job.SecretKeys` (the subset of env keys flagged as
// secrets). The lookup materialises Env[k] for each k in
// SecretKeys — the VALUE bytes that are substitution-candidates.
type SecretsLookup func(jobID string) (values []string, ok bool)

// Scrub replaces every occurrence of every non-empty secret
// value in chunk with the redacted sentinel. Pure — no I/O, no
// locking.
//
// Empty `secrets` or zero-length secret entries are skipped; see
// the safety notes in the package doc.
//
// The loop order is stable (iteration over the input slice) so
// a secret whose value happens to be a substring of another
// secret still produces deterministic output — the first
// matching rule from `secrets` wins on the overlap.
func Scrub(chunk []byte, secrets []string) []byte {
	if len(chunk) == 0 || len(secrets) == 0 {
		return chunk
	}
	out := chunk
	replaced := false
	for _, v := range secrets {
		if v == "" {
			continue
		}
		needle := []byte(v)
		// Fast path: no occurrence → no allocation.
		if !bytes.Contains(out, needle) {
			continue
		}
		if !replaced {
			// First match: copy so we don't mutate the caller's
			// slice. ReplaceAll already allocates a new slice
			// (when a match is present), so this copy is only
			// needed on the very first replacement — after that
			// `out` already refers to our own allocation.
			out = bytes.ReplaceAll(out, needle, []byte(redactedSentinel))
			replaced = true
			continue
		}
		out = bytes.ReplaceAll(out, needle, []byte(redactedSentinel))
	}
	return out
}

// ScrubbingStore decorates a Store and scrubs every Append
// chunk before delegating to the underlying store. Reads pass
// through unchanged — the response-path redactor in
// internal/api handles the GET side.
//
// Zero-cost when the job has no secrets: the lookup short-
// circuits on `(nil, false)` and the chunk bytes are written
// verbatim.
//
// Thread safety matches the decorated Store's — all locking
// belongs to the inner implementation.
type ScrubbingStore struct {
	inner  Store
	lookup SecretsLookup
}

// NewScrubbingStore wraps inner so Append scrubs chunks whose
// job has declared secrets. A nil lookup is equivalent to a
// lookup that always returns (nil, false) — no scrubbing
// happens, which is a valid dev-mode configuration; production
// wiring always passes a real JobStore-backed lookup.
func NewScrubbingStore(inner Store, lookup SecretsLookup) *ScrubbingStore {
	return &ScrubbingStore{inner: inner, lookup: lookup}
}

// Append scrubs entry.Data in-place on a per-chunk copy before
// delegating to the inner store. Preserves entry.JobID,
// entry.Seq, entry.Timestamp unchanged.
func (s *ScrubbingStore) Append(ctx context.Context, entry LogEntry) error {
	if s.lookup == nil {
		return s.inner.Append(ctx, entry)
	}
	secrets, ok := s.lookup(entry.JobID)
	if !ok || len(secrets) == 0 {
		return s.inner.Append(ctx, entry)
	}
	entry.Data = string(Scrub([]byte(entry.Data), secrets))
	return s.inner.Append(ctx, entry)
}

// Get delegates unchanged. The response-path redactor in the
// API layer handles read-side scrubbing for defence in depth
// against chunks written before the decorator was wired.
func (s *ScrubbingStore) Get(ctx context.Context, jobID string) ([]LogEntry, error) {
	return s.inner.Get(ctx, jobID)
}

// ReconcileConfirmed forwards to the inner store when it
// satisfies the feature-28 Reconcilable interface. Necessary so
// the reconciler loop continues to free Badger entries once
// they're durable in PG even when the decorator is in the
// middle of the Store chain.
func (s *ScrubbingStore) ReconcileConfirmed(
	ctx context.Context,
	minAge time.Duration,
	confirmedFn func(jobID string, seq uint64) (bool, error),
) (deleted, scanned int, err error) {
	if r, ok := s.inner.(Reconcilable); ok {
		return r.ReconcileConfirmed(ctx, minAge, confirmedFn)
	}
	// Inner doesn't support reconciliation — no-op rather than
	// erroring so the reconciler loop treats the decorator as
	// transparent.
	return 0, 0, nil
}
