// internal/secretstore/versions_test.go
//
// Tests for KeyRing.Versions — the diagnostics accessor used by
// the rotation admin endpoint. Keeps the package's coverage
// aligned with the always-tests-per-slice rule.

package secretstore_test

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

func TestVersions_SingleKEK_ReturnsActiveOnly(t *testing.T) {
	kr, err := secretstore.NewKeyRing(1, freshKEK(t))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	got := kr.Versions()
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("Versions: got %v, want [1]", got)
	}
}

func TestVersions_MultipleKEKs_SortedAscending(t *testing.T) {
	// Add versions out of order; Versions must return them
	// sorted ascending so the admin endpoint response is stable.
	kr, _ := secretstore.NewKeyRing(3, freshKEK(t))
	_ = kr.AddKEK(1, freshKEK(t))
	_ = kr.AddKEK(5, freshKEK(t))
	_ = kr.AddKEK(2, freshKEK(t))

	got := kr.Versions()
	want := []uint32{1, 2, 3, 5}
	if len(got) != len(want) {
		t.Fatalf("len: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Versions[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestVersions_AfterRemoveKEK_ReflectsCurrent(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, freshKEK(t))
	_ = kr.AddKEK(2, freshKEK(t))
	_ = kr.AddKEK(3, freshKEK(t))
	_ = kr.RemoveKEK(2)

	got := kr.Versions()
	want := []uint32{1, 3}
	if len(got) != len(want) || got[0] != 1 || got[1] != 3 {
		t.Errorf("Versions after remove: got %v, want %v", got, want)
	}
}
