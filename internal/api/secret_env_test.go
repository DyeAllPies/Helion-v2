// internal/api/secret_env_test.go
//
// Unit tests for the feature-26 helpers. Higher-level HTTP-shape
// tests for POST /jobs + POST /admin/jobs/{id}/reveal-secret live
// in handlers_jobs_test.go / handlers_admin_test.go.

package api

import (
	"strings"
	"testing"
)

// ── redactSecretEnv ───────────────────────────────────────────────────────────

func TestRedactSecretEnv_NilInputReturnsNil(t *testing.T) {
	if got := redactSecretEnv(nil, []string{"HF_TOKEN"}); got != nil {
		t.Errorf("nil env in: want nil out, got %v", got)
	}
}

func TestRedactSecretEnv_NoSecretsCopiesVerbatim(t *testing.T) {
	env := map[string]string{"PYTHONPATH": "/app", "HELION_API_URL": "https://a/b"}
	out := redactSecretEnv(env, nil)
	if len(out) != 2 || out["PYTHONPATH"] != "/app" || out["HELION_API_URL"] != "https://a/b" {
		t.Errorf("no secrets: want verbatim copy, got %v", out)
	}
	// Must be a distinct map — mutating the returned map must NOT
	// pollute the caller's map.
	out["PYTHONPATH"] = "touched"
	if env["PYTHONPATH"] == "touched" {
		t.Error("redactSecretEnv returned aliased map; must return a fresh copy")
	}
}

func TestRedactSecretEnv_MatchedKeysReplaced(t *testing.T) {
	env := map[string]string{
		"PYTHONPATH": "/app",
		"HF_TOKEN":   "hf_sekret_value",
		"AWS_SECRET": "AKIA...",
	}
	out := redactSecretEnv(env, []string{"HF_TOKEN", "AWS_SECRET"})
	if out["PYTHONPATH"] != "/app" {
		t.Errorf("non-secret must survive: got %q", out["PYTHONPATH"])
	}
	if out["HF_TOKEN"] != RedactionPlaceholder {
		t.Errorf("HF_TOKEN: want %q, got %q", RedactionPlaceholder, out["HF_TOKEN"])
	}
	if out["AWS_SECRET"] != RedactionPlaceholder {
		t.Errorf("AWS_SECRET: want %q, got %q", RedactionPlaceholder, out["AWS_SECRET"])
	}
	// Regression guard: redaction must not leak the plaintext anywhere
	// in the returned map. Iterate every value.
	for k, v := range out {
		if strings.Contains(v, "hf_sekret_value") {
			t.Errorf("plaintext leaked via key %q: %q", k, v)
		}
	}
}

func TestRedactSecretEnv_SecretKeyAbsentFromEnvIsNoop(t *testing.T) {
	// If a key is declared secret but not present in env, redactor
	// silently skips — the submit-time validator rejects that case,
	// so by the time we reach the redactor it's an invariant that
	// every secret key is present. Defence-in-depth: don't panic if
	// it happens.
	env := map[string]string{"PYTHONPATH": "/app"}
	out := redactSecretEnv(env, []string{"MISSING_KEY"})
	if out["PYTHONPATH"] != "/app" {
		t.Error("unrelated key must not be affected by a stale secret flag")
	}
}

func TestRedactSecretEnv_DoesNotAddMissingKey(t *testing.T) {
	// Redactor must not invent a "[REDACTED]" entry for a key the
	// original env never had — that would be a forged audit trail.
	env := map[string]string{"PYTHONPATH": "/app"}
	out := redactSecretEnv(env, []string{"PHANTOM"})
	if _, ok := out["PHANTOM"]; ok {
		t.Error("redactor must not invent keys absent from source env")
	}
}

// ── validateSecretKeys ───────────────────────────────────────────────────────

func TestValidateSecretKeys_EmptyOK(t *testing.T) {
	if msg := validateSecretKeys(map[string]string{"A": "b"}, nil); msg != "" {
		t.Errorf("nil secret keys: want no msg, got %q", msg)
	}
}

func TestValidateSecretKeys_HappyPath(t *testing.T) {
	env := map[string]string{"PYTHONPATH": "/app", "HF_TOKEN": "hf_"}
	if msg := validateSecretKeys(env, []string{"HF_TOKEN"}); msg != "" {
		t.Errorf("valid secret: want no msg, got %q", msg)
	}
}

func TestValidateSecretKeys_CountCap(t *testing.T) {
	env := make(map[string]string, maxSecretKeys+1)
	keys := make([]string, maxSecretKeys+1)
	for i := 0; i <= maxSecretKeys; i++ {
		k := "K" + itoa(i)
		env[k] = "v"
		keys[i] = k
	}
	msg := validateSecretKeys(env, keys)
	if msg == "" || !strings.Contains(msg, "32") {
		t.Errorf("over-cap: want complaint naming 32, got %q", msg)
	}
}

func itoa(i int) string {
	// Tiny non-fmt int→string for test key generation.
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

func TestValidateSecretKeys_EmptyEntryRejected(t *testing.T) {
	env := map[string]string{"A": "b"}
	msg := validateSecretKeys(env, []string{""})
	if msg == "" || !strings.Contains(msg, "empty") {
		t.Errorf("empty entry: want 'empty' in msg, got %q", msg)
	}
}

func TestValidateSecretKeys_DuplicateRejected(t *testing.T) {
	env := map[string]string{"HF_TOKEN": "x"}
	msg := validateSecretKeys(env, []string{"HF_TOKEN", "HF_TOKEN"})
	if msg == "" || !strings.Contains(msg, "duplicate") {
		t.Errorf("duplicate: want 'duplicate' in msg, got %q", msg)
	}
}

func TestValidateSecretKeys_MissingKeyRejected(t *testing.T) {
	// Regression guard: flagging a key that isn't in env is either
	// a typo or a probe ("does the server accept FOO_TOKEN as a
	// secret?"). Reject.
	env := map[string]string{"PYTHONPATH": "/app"}
	msg := validateSecretKeys(env, []string{"HF_TOKEN"})
	if msg == "" || !strings.Contains(msg, "not present") {
		t.Errorf("missing key: want 'not present' in msg, got %q", msg)
	}
}

// ── auditSafeSecretKeys ──────────────────────────────────────────────────────

func TestAuditSafeSecretKeys_NilReturnsNil(t *testing.T) {
	if got := auditSafeSecretKeys(nil); got != nil {
		t.Errorf("nil in: want nil out, got %v", got)
	}
}

func TestAuditSafeSecretKeys_DeDupsAndSorts(t *testing.T) {
	got := auditSafeSecretKeys([]string{"HF_TOKEN", "AWS_SECRET", "HF_TOKEN"})
	want := []string{"AWS_SECRET", "HF_TOKEN"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d (values %v)", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("idx %d: got %q want %q", i, got[i], v)
		}
	}
}

// ── validateRevealSecretRequest ──────────────────────────────────────────────

func TestValidateRevealSecretRequest_Nil(t *testing.T) {
	if msg := validateRevealSecretRequest(nil); msg == "" {
		t.Error("nil request: want msg")
	}
}

func TestValidateRevealSecretRequest_EmptyKey(t *testing.T) {
	msg := validateRevealSecretRequest(&RevealSecretRequest{Reason: "debugging"})
	if msg == "" || !strings.Contains(msg, "key") {
		t.Errorf("empty key: want 'key' in msg, got %q", msg)
	}
}

func TestValidateRevealSecretRequest_KeyWithEquals(t *testing.T) {
	msg := validateRevealSecretRequest(&RevealSecretRequest{Key: "A=B", Reason: "r"})
	if msg == "" {
		t.Error("key with '=': want err")
	}
}

func TestValidateRevealSecretRequest_EmptyReason(t *testing.T) {
	msg := validateRevealSecretRequest(&RevealSecretRequest{Key: "HF_TOKEN"})
	if msg == "" || !strings.Contains(msg, "reason") {
		t.Errorf("empty reason: want 'reason' in msg, got %q", msg)
	}
}

func TestValidateRevealSecretRequest_WhitespaceOnlyReason(t *testing.T) {
	// Reason must have actual content; padding the body with spaces
	// must not satisfy the audit requirement.
	msg := validateRevealSecretRequest(&RevealSecretRequest{Key: "HF_TOKEN", Reason: "   \t\n"})
	if msg == "" {
		t.Error("whitespace-only reason: want err")
	}
}

func TestValidateRevealSecretRequest_OversizeKey(t *testing.T) {
	big := strings.Repeat("A", maxRevealSecretKeyLen+1)
	msg := validateRevealSecretRequest(&RevealSecretRequest{Key: big, Reason: "r"})
	if msg == "" {
		t.Error("oversize key: want err")
	}
}

func TestValidateRevealSecretRequest_OversizeReason(t *testing.T) {
	msg := validateRevealSecretRequest(&RevealSecretRequest{
		Key:    "HF_TOKEN",
		Reason: strings.Repeat("x", maxRevealSecretReasonLen+1),
	})
	if msg == "" {
		t.Error("oversize reason: want err")
	}
}

func TestValidateRevealSecretRequest_HappyPath(t *testing.T) {
	if msg := validateRevealSecretRequest(&RevealSecretRequest{
		Key:    "HF_TOKEN",
		Reason: "operator on-call debugging HF model load",
	}); msg != "" {
		t.Errorf("happy path: want no msg, got %q", msg)
	}
}

// ── containsSecretKey ────────────────────────────────────────────────────────

func TestContainsSecretKey(t *testing.T) {
	if !containsSecretKey([]string{"A", "HF_TOKEN"}, "HF_TOKEN") {
		t.Error("HF_TOKEN should be found")
	}
	if containsSecretKey([]string{"A", "HF_TOKEN"}, "AWS_SECRET") {
		t.Error("AWS_SECRET should NOT be found")
	}
	if containsSecretKey(nil, "HF_TOKEN") {
		t.Error("nil slice: nothing matches")
	}
	// Case-sensitivity: env vars are case-sensitive.
	if containsSecretKey([]string{"HF_TOKEN"}, "hf_token") {
		t.Error("contains must be case-sensitive")
	}
}
