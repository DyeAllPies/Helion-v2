// internal/analytics/extractors_test.go
//
// Coverage for extractBool / extractInt64 variants. These
// helpers absorb JSON round-trip drift (bool → string,
// int → float64) so the sink writes a stable-typed row even
// when the event arrived via analytics.NATS-style pubsub.

package analytics

import "testing"

func TestExtractBool_AllTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"native true", true, true},
		{"native false", false, false},
		{"string true", "true", true},
		{"string false", "false", false},
		{"string bogus", "yes", false},
		{"int 1", int(1), true},
		{"int 0", int(0), false},
		{"float 1.0", float64(1), true},
		{"float 0.0", float64(0), false},
		{"other type", []byte{1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractBool(map[string]any{"k": c.in}, "k")
			if got != c.want {
				t.Errorf("extractBool(%v): got %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestExtractBool_NilData_False(t *testing.T) {
	if extractBool(nil, "k") {
		t.Error("nil data must return false")
	}
}

func TestExtractBool_MissingKey_False(t *testing.T) {
	if extractBool(map[string]any{"other": true}, "k") {
		t.Error("missing key must return false")
	}
}

func TestExtractInt64_AllTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"native int64", int64(42), 42},
		{"native int", int(7), 7},
		{"int32", int32(9), 9},
		{"uint32", uint32(15), 15},
		{"float64", float64(13), 13},
		{"string bogus", "not-a-number", 0},
		{"nil-value case", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractInt64(map[string]any{"k": c.in}, "k")
			if got != c.want {
				t.Errorf("extractInt64(%v): got %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestExtractInt64_NilDataAndMissingKey_Zero(t *testing.T) {
	if extractInt64(nil, "k") != 0 {
		t.Error("nil data: want 0")
	}
	if extractInt64(map[string]any{"other": 1}, "k") != 0 {
		t.Error("missing key: want 0")
	}
}
