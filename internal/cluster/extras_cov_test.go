// internal/cluster/extras_cov_test.go
//
// Coverage for small helpers previously uncovered:
// truncateLabelMap, noNodeMatchesSelectorError.Error,
// Registry.SetCertIssuer.

package cluster

import (
	"errors"
	"testing"
	"time"
)

// ── truncateLabelMap ────────────────────────────────────────

func TestTruncateLabelMap_UnderCap_ReturnsAll(t *testing.T) {
	in := map[string]string{"a": "1", "b": "2"}
	got := truncateLabelMap(in, 10)
	if len(got) != 2 {
		t.Errorf("under-cap: got %d, want 2", len(got))
	}
}

func TestTruncateLabelMap_AtCap_ReturnsAll(t *testing.T) {
	in := map[string]string{"a": "1", "b": "2", "c": "3"}
	got := truncateLabelMap(in, 3)
	if len(got) != 3 {
		t.Errorf("at cap: got %d, want 3", len(got))
	}
}

func TestTruncateLabelMap_OverCap_Truncated(t *testing.T) {
	in := map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}
	got := truncateLabelMap(in, 2)
	if len(got) != 2 {
		t.Errorf("over-cap: got %d, want 2", len(got))
	}
}

func TestTruncateLabelMap_EmptyInput_EmptyOutput(t *testing.T) {
	got := truncateLabelMap(map[string]string{}, 5)
	if len(got) != 0 {
		t.Errorf("empty: got %d, want 0", len(got))
	}
}

// ── noNodeMatchesSelectorError.Error ────────────────────────

func TestNoNodeMatchesSelectorError_ErrorAndSentinel(t *testing.T) {
	// Cover the Error() method + the errors.Is chain via the
	// ErrNoNodeMatchesSelector sentinel.
	err := ErrNoNodeMatchesSelector
	if err == nil {
		t.Fatal("ErrNoNodeMatchesSelector is nil")
	}
	if err.Error() == "" {
		t.Error("Error() returned empty string")
	}
	var target noNodeMatchesSelectorError
	if !errors.As(err, &target) {
		t.Error("errors.As failed to match noNodeMatchesSelectorError")
	}
}

// ── Registry.SetCertIssuer ──────────────────────────────────

type noopCertIssuer struct{}

func (noopCertIssuer) IssueNodeCert(_ string) ([]byte, []byte, error) {
	return nil, nil, nil
}

func TestRegistry_SetCertIssuer_Stamps(t *testing.T) {
	r := NewRegistry(NopPersister{}, time.Second, nil)
	r.SetCertIssuer(noopCertIssuer{})
	// Set twice to exercise the mutex path.
	r.SetCertIssuer(noopCertIssuer{})
}
