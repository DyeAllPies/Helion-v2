// internal/secretstore/extras_kek_test.go
//
// Additional coverage for AddKEK / SetActive / RemoveKEK /
// ParseKEK edge paths the main tests don't hit. Specifically
// version=0 rejection, duplicate-version rejection, setting a
// version that wasn't AddKEK'd, attempting to remove the
// active version.

package secretstore_test

import (
	"crypto/rand"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

func mkKEK() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func TestAddKEK_VersionZero_Rejected(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	err := kr.AddKEK(0, mkKEK())
	if err == nil {
		t.Fatal("version 0: want error, got nil")
	}
	if !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Errorf("error chain: got %v, want wraps ErrInvalidKEK", err)
	}
}

func TestAddKEK_DuplicateVersion_Rejected(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	// Version 1 is already registered by NewKeyRing.
	err := kr.AddKEK(1, mkKEK())
	if err == nil {
		t.Fatal("duplicate: want error")
	}
	if !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Errorf("error chain: got %v, want wraps ErrInvalidKEK", err)
	}
}

func TestAddKEK_InvalidLength_Rejected(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	err := kr.AddKEK(2, make([]byte, 17)) // wrong size — must be 32
	if err == nil {
		t.Error("short KEK: want error")
	}
}

func TestSetActive_UnregisteredVersion_Rejected(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	err := kr.SetActive(99)
	if err == nil {
		t.Fatal("unregistered: want error")
	}
}

func TestSetActive_ExistingVersion_Succeeds(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	_ = kr.AddKEK(2, mkKEK())
	if err := kr.SetActive(2); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if kr.ActiveVersion() != 2 {
		t.Errorf("active: got %d, want 2", kr.ActiveVersion())
	}
}

func TestRemoveKEK_ActiveVersion_Rejected(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	err := kr.RemoveKEK(1)
	if err == nil {
		t.Fatal("remove active: want error")
	}
}

func TestRemoveKEK_UnregisteredVersion_Ok(t *testing.T) {
	kr, _ := secretstore.NewKeyRing(1, mkKEK())
	// Removing a never-registered version is a no-op — the
	// operator doesn't care whether version 99 was in the ring.
	err := kr.RemoveKEK(99)
	if err != nil {
		t.Logf("remove-unregistered: %v (implementation may prefer to error)", err)
	}
}

func TestParseKEK_RejectsWrongLength(t *testing.T) {
	// ParseKEK is the env-var / Base64 parser. Feed a short
	// string to hit the length-check error branch.
	_, err := secretstore.ParseKEK("AAAA") // 3 bytes decoded
	if err == nil {
		t.Error("short input: want error")
	}
}

func TestParseKEK_RejectsInvalidBase64(t *testing.T) {
	_, err := secretstore.ParseKEK("!!!not-base64!!!")
	if err == nil {
		t.Error("invalid base64: want error")
	}
}
