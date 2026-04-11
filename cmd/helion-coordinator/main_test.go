// cmd/helion-coordinator/main_test.go
//
// Tests for package-local helpers in main.go.

package main

import (
	"os"
	"testing"
)

// ── parseNodePins (AUDIT M5) ─────────────────────────────────────────────────

func TestParseNodePins_Empty_ReturnsNil(t *testing.T) {
	pins, err := parseNodePins("")
	if err != nil {
		t.Fatalf("empty: unexpected error: %v", err)
	}
	if pins != nil {
		t.Errorf("empty: want nil map, got %v", pins)
	}
}

func TestParseNodePins_Whitespace_ReturnsNil(t *testing.T) {
	pins, err := parseNodePins("   ")
	if err != nil {
		t.Fatalf("whitespace: unexpected error: %v", err)
	}
	if pins != nil {
		t.Errorf("whitespace: want nil, got %v", pins)
	}
}

func TestParseNodePins_SingleEntry(t *testing.T) {
	fp := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	pins, err := parseNodePins("alpha:" + fp)
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if got := pins["alpha"]; got != fp {
		t.Errorf("alpha: want %q, got %q", fp, got)
	}
}

func TestParseNodePins_MultipleEntries(t *testing.T) {
	fp1 := "0000000000000000000000000000000000000000000000000000000000000001"
	fp2 := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	pins, err := parseNodePins("n1:" + fp1 + ",n2:" + fp2)
	if err != nil {
		t.Fatalf("multi: %v", err)
	}
	if pins["n1"] != fp1 {
		t.Errorf("n1: want %q, got %q", fp1, pins["n1"])
	}
	if pins["n2"] != fp2 {
		t.Errorf("n2: want %q, got %q", fp2, pins["n2"])
	}
}

func TestParseNodePins_TrimsWhitespace(t *testing.T) {
	fp := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	pins, err := parseNodePins("  n1 : " + fp + " ,  ")
	if err != nil {
		t.Fatalf("whitespace: %v", err)
	}
	if pins["n1"] != fp {
		t.Errorf("trimmed: got %q", pins["n1"])
	}
}

func TestParseNodePins_MissingColon_ReturnsError(t *testing.T) {
	_, err := parseNodePins("nocolonhere")
	if err == nil {
		t.Error("expected error for missing ':' separator")
	}
}

func TestParseNodePins_EmptyNodeID_ReturnsError(t *testing.T) {
	fp := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	_, err := parseNodePins(":" + fp)
	if err == nil {
		t.Error("expected error for empty nodeID")
	}
}

func TestParseNodePins_WrongFingerprintLength_ReturnsError(t *testing.T) {
	_, err := parseNodePins("n1:tooshort")
	if err == nil {
		t.Error("expected error for short fingerprint")
	}
}

func TestParseNodePins_NonHexFingerprint_ReturnsError(t *testing.T) {
	// 64 chars but includes 'g' which is not hex.
	bad := "ghijklmnopqrstuvwxyzghijklmnopqrstuvwxyzghijklmnopqrstuvwxyz1234"
	_, err := parseNodePins("n1:" + bad)
	if err == nil {
		t.Error("expected error for non-hex fingerprint")
	}
}

func TestParseNodePins_UppercaseHex_ReturnsError(t *testing.T) {
	// CertFingerprint emits lowercase; uppercase would fail exact-string
	// comparison, so parseNodePins rejects it at configuration time.
	upper := "DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF"
	_, err := parseNodePins("n1:" + upper)
	if err == nil {
		t.Error("expected error for uppercase hex fingerprint")
	}
}

func TestParseNodePins_SkipsEmptyEntries(t *testing.T) {
	fp := "0000000000000000000000000000000000000000000000000000000000000001"
	pins, err := parseNodePins("n1:" + fp + ",,,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pins) != 1 {
		t.Errorf("want 1 pin after skipping empties, got %d", len(pins))
	}
}

// ── isLowerHex ────────────────────────────────────────────────────────────────

func TestIsLowerHex(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0123456789abcdef", true},
		{"", true}, // empty is vacuously true
		{"0123456789ABCDEF", false},
		{"deadbeef!", false},
		{"ghij", false},
	}
	for _, c := range cases {
		if got := isLowerHex(c.in); got != c.want {
			t.Errorf("isLowerHex(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ── envOr ─────────────────────────────────────────────────────────────────────

func TestEnvOr_SetValueReturned(t *testing.T) {
	t.Setenv("HELION_TEST_ENVOR", "from-env")
	if got := envOr("HELION_TEST_ENVOR", "fallback"); got != "from-env" {
		t.Errorf("want 'from-env', got %q", got)
	}
}

func TestEnvOr_UnsetUsesFallback(t *testing.T) {
	_ = os.Unsetenv("HELION_TEST_ENVOR")
	if got := envOr("HELION_TEST_ENVOR", "fallback"); got != "fallback" {
		t.Errorf("want 'fallback', got %q", got)
	}
}

func TestEnvOr_EmptyUsesFallback(t *testing.T) {
	t.Setenv("HELION_TEST_ENVOR", "")
	if got := envOr("HELION_TEST_ENVOR", "fallback"); got != "fallback" {
		t.Errorf("empty env should use fallback, got %q", got)
	}
}

// ── envInt ────────────────────────────────────────────────────────────────────

func TestEnvInt_ValidValueReturned(t *testing.T) {
	t.Setenv("HELION_TEST_ENVINT", "42")
	if got := envInt("HELION_TEST_ENVINT", 10); got != 42 {
		t.Errorf("want 42, got %d", got)
	}
}

func TestEnvInt_UnsetUsesDefault(t *testing.T) {
	_ = os.Unsetenv("HELION_TEST_ENVINT")
	if got := envInt("HELION_TEST_ENVINT", 10); got != 10 {
		t.Errorf("want default 10, got %d", got)
	}
}

func TestEnvInt_InvalidValueUsesDefault(t *testing.T) {
	t.Setenv("HELION_TEST_ENVINT", "not-a-number")
	if got := envInt("HELION_TEST_ENVINT", 10); got != 10 {
		t.Errorf("malformed env should fall back to default, got %d", got)
	}
}

func TestEnvInt_NegativeValueReturned(t *testing.T) {
	// envInt does not clamp — negative values are passed through intact.
	t.Setenv("HELION_TEST_ENVINT", "-5")
	if got := envInt("HELION_TEST_ENVINT", 10); got != -5 {
		t.Errorf("want -5 (no clamp), got %d", got)
	}
}
