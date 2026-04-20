// internal/pqcrypto/revocation_extras_test.go
//
// Coverage for the smaller revocation helpers not exercised
// by the existing integration-style tests:
// SerialHexFromBigInt + trimReason boundary cases.

package pqcrypto

import (
	"math/big"
	"strings"
	"testing"
)

// ── SerialHexFromBigInt ─────────────────────────────────────

func TestSerialHexFromBigInt_NilReturnsEmpty(t *testing.T) {
	if got := SerialHexFromBigInt(nil); got != "" {
		t.Errorf("nil: got %q, want empty", got)
	}
}

func TestSerialHexFromBigInt_Zero(t *testing.T) {
	if got := SerialHexFromBigInt(big.NewInt(0)); got != "0" {
		t.Errorf("zero: got %q, want 0", got)
	}
}

func TestSerialHexFromBigInt_Large(t *testing.T) {
	n := new(big.Int).SetBytes([]byte{0xde, 0xad, 0xbe, 0xef})
	if got := SerialHexFromBigInt(n); got != "deadbeef" {
		t.Errorf("large: got %q, want deadbeef", got)
	}
}

func TestSerialHexFromBigInt_Lowercase(t *testing.T) {
	// go's big.Int.Text(16) lowercases by default, but this
	// test guards against a future regression if someone
	// switches to Text(16, true).
	n := new(big.Int).SetBytes([]byte{0xAB, 0xCD})
	got := SerialHexFromBigInt(n)
	if got != strings.ToLower(got) {
		t.Errorf("not lowercase: %q", got)
	}
}

// ── trimReason ──────────────────────────────────────────────

func TestTrimReason_ShortReason_Unchanged(t *testing.T) {
	if got := trimReason("hello"); got != "hello" {
		t.Errorf("short: got %q", got)
	}
}

func TestTrimReason_WhitespaceTrimmed(t *testing.T) {
	if got := trimReason("  lost device  "); got != "lost device" {
		t.Errorf("whitespace: got %q", got)
	}
}

func TestTrimReason_OversizeTruncated(t *testing.T) {
	long := strings.Repeat("a", maxReasonBytes+100)
	got := trimReason(long)
	if len(got) != maxReasonBytes {
		t.Errorf("length: got %d, want %d", len(got), maxReasonBytes)
	}
}

func TestTrimReason_AtBoundary_Unchanged(t *testing.T) {
	// Exactly at the cap — must not truncate.
	exact := strings.Repeat("a", maxReasonBytes)
	got := trimReason(exact)
	if len(got) != maxReasonBytes {
		t.Errorf("boundary: got %d bytes, want %d", len(got), maxReasonBytes)
	}
}

func TestTrimReason_EmptyString_ReturnsEmpty(t *testing.T) {
	if got := trimReason(""); got != "" {
		t.Errorf("empty: got %q", got)
	}
}

func TestTrimReason_OnlyWhitespace_ReturnsEmpty(t *testing.T) {
	if got := trimReason("   \t\n  "); got != "" {
		t.Errorf("whitespace-only: got %q", got)
	}
}
