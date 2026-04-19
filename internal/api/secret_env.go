// internal/api/secret_env.go
//
// Feature 26 — secret env-var support.
//
// Design deviation from the original spec (docs/planned-features/
// implemented/26-secret-env-vars.md):
//
//   Spec proposed a polymorphic `env` field accepting either
//   map[string]string OR []SubmitEnvVar{Key,Value,Secret}. Shipped
//   form keeps `env: map[string]string` and adds a sibling
//   `secret_keys: []string` that names which keys hold secret values.
//   Same security properties, no polymorphic JSON decoder, automatic
//   back-compat (omit secret_keys → zero secret, unchanged behaviour).
//
// What this file owns:
//
//   1. The redaction placeholder shown on GET / list / dry-run paths.
//   2. redactSecretEnv — builds a response-ready env map with matched
//      values replaced by the placeholder. Never mutates the caller's
//      map; the stored Job record keeps plaintext because the runtime
//      needs it to dispatch.
//   3. validateSecretKeys — rejects secret-key declarations that
//      don't match a real env key, duplicates, empty keys, or keys
//      beyond a sane count cap. Applied on every submit path.
//   4. auditSafeSecretKeys — de-dup + sorted snapshot of the secret
//      key NAMES (never the values) for inclusion in audit details so
//      reviewers can see WHICH keys were flagged.

package api

import (
	"fmt"
	"sort"
	"strings"
)

// RedactionPlaceholder is what the server emits in place of a secret
// env value on any response path (GET /jobs/{id}, list endpoints,
// dry-run preflight). The placeholder is deliberately opaque so a
// client programming against the API does not accidentally mistake
// the placeholder itself for a meaningful value.
const RedactionPlaceholder = "[REDACTED]"

// maxSecretKeys caps how many env keys a single submit may flag as
// secret. The overall env map is already capped at maxEnvLen (128);
// we cap secrets lower because any legitimate job needs a handful of
// tokens at most, and an attacker flagging all 128 keys as secret
// would force the response redactor into a degenerate loop AND would
// hide every value from GET — both bad.
const maxSecretKeys = 32

// redactSecretEnv returns a NEW map suitable for a response body:
// every key listed in secretKeys has its value replaced by
// RedactionPlaceholder. Non-secret keys are copied verbatim. The
// input map is never mutated.
//
// Passing a nil secretKeys slice is the common (no-secrets) case and
// returns a shallow copy of env without any redaction. Returning a
// copy (rather than returning the same map) protects us against a
// later caller that wanted to mutate the returned map without
// polluting the underlying Job record.
//
// If env is nil, returns nil (preserves the omitempty marshaling so
// a job with no env doesn't render an empty {} object).
func redactSecretEnv(env map[string]string, secretKeys []string) map[string]string {
	if env == nil {
		return nil
	}
	if len(secretKeys) == 0 {
		out := make(map[string]string, len(env))
		for k, v := range env {
			out[k] = v
		}
		return out
	}
	set := secretKeySet(secretKeys)
	out := make(map[string]string, len(env))
	for k, v := range env {
		if _, secret := set[k]; secret {
			out[k] = RedactionPlaceholder
			continue
		}
		out[k] = v
	}
	return out
}

// secretKeySet converts the secret-key slice into a lookup set. The
// slice form is what callers store (it round-trips cleanly through
// JSON and BadgerDB); the set form is what the redactor needs for
// O(1) membership. Callers use both — hence the helper.
func secretKeySet(keys []string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

// validateSecretKeys enforces the submit-time invariants on the
// secret_keys list. Returns an empty string on success or a user-
// facing error message the caller turns into a 400.
//
// Rules:
//   - Count cap (maxSecretKeys) — guards against a pathological
//     "flag everything" submit that would render GET responses
//     useless.
//   - Every listed key MUST be a real key in env. Flagging a key
//     that isn't present is either a typo or a probe for which key
//     names the server silently accepts; either way, reject.
//   - No duplicates. A legitimate client has no reason to list the
//     same key twice; accepting it would bloat audit detail and
//     complicate invariants on the downstream side.
//   - No empty strings. The env loop already rejects empty keys,
//     but a typed slice of secret names is a separate input and
//     needs its own check.
func validateSecretKeys(env map[string]string, secretKeys []string) string {
	if len(secretKeys) == 0 {
		return ""
	}
	if len(secretKeys) > maxSecretKeys {
		return fmt.Sprintf("secret_keys must not exceed %d entries", maxSecretKeys)
	}
	seen := make(map[string]struct{}, len(secretKeys))
	for _, k := range secretKeys {
		if k == "" {
			return "secret_keys entries must not be empty"
		}
		if _, dup := seen[k]; dup {
			return fmt.Sprintf("secret_keys: duplicate key %q", k)
		}
		seen[k] = struct{}{}
		if _, ok := env[k]; !ok {
			return fmt.Sprintf("secret_keys: key %q is not present in env — flag is meaningless and likely a typo", k)
		}
	}
	return ""
}

// auditSafeSecretKeys returns a sorted, de-duplicated snapshot of
// the secret key NAMES (never their values) for inclusion in audit
// event details. Reviewers can see which env vars a submitter
// flagged as secret without ever learning the plaintext.
//
// Returns nil for an empty/nil input so the audit event detail omits
// the field cleanly rather than carrying an empty slice.
func auditSafeSecretKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	// De-dup via a set, then produce a stable order so audit queries
	// and equality comparisons are reproducible.
	set := secretKeySet(keys)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// containsSecretKey reports whether haystack names needle. Tiny
// helper used by the reveal-secret handler to authorise the lookup:
// we never reveal a value for a key that wasn't declared secret at
// submit time (otherwise the endpoint becomes a generic env-dump).
func containsSecretKey(haystack []string, needle string) bool {
	for _, k := range haystack {
		if k == needle {
			return true
		}
	}
	return false
}

// validateRevealSecretRequest enforces the submit-time invariants on
// POST /admin/jobs/{id}/reveal-secret bodies. The reason field is
// mandatory: the audit record must carry a human-readable
// justification or the endpoint becomes a "show me the secret"
// button with no accountability story.
func validateRevealSecretRequest(r *RevealSecretRequest) string {
	if r == nil {
		return "request body is required"
	}
	if r.Key == "" {
		return "key is required"
	}
	if strings.ContainsAny(r.Key, "=\x00") {
		return "key must not contain '=' or NUL"
	}
	if len(r.Key) > maxRevealSecretKeyLen {
		return fmt.Sprintf("key must not exceed %d bytes", maxRevealSecretKeyLen)
	}
	if strings.TrimSpace(r.Reason) == "" {
		return "reason is required — include a short human-readable justification; it is audited"
	}
	if len(r.Reason) > maxRevealSecretReasonLen {
		return fmt.Sprintf("reason must not exceed %d bytes", maxRevealSecretReasonLen)
	}
	if strings.ContainsRune(r.Reason, '\x00') {
		return "reason must not contain NUL"
	}
	return ""
}

// Caps on the reveal-secret request fields. Key is bounded by env-
// var sizing already; the reason string is freer-form but still
// caps out well below the body-size ceiling to keep audit entries
// from bloating.
const (
	maxRevealSecretKeyLen    = 256
	maxRevealSecretReasonLen = 512
)

// RevealSecretRequest is the JSON body for
// POST /admin/jobs/{id}/reveal-secret.
//
// Admin-only. Every successful reveal emits a `secret_revealed`
// audit event carrying (job_id, key, actor, reason). The reason
// field is mandatory because the audit story depends on operators
// recording WHY they needed the value.
type RevealSecretRequest struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// RevealSecretResponse is the JSON body for a successful reveal.
// Carries the plaintext value plus a prominent audit notice so the
// client operator sees on-screen that the action was logged.
type RevealSecretResponse struct {
	JobID      string `json:"job_id"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	RevealedAt string `json:"revealed_at"`
	RevealedBy string `json:"revealed_by"`
	// AuditNotice is a human-readable reminder — the dashboard
	// renders it alongside the value. Belt-and-braces: the server's
	// audit event is the real accountability record, but having the
	// notice in the response body means the operator cannot plausibly
	// claim they didn't know.
	AuditNotice string `json:"audit_notice"`
}
