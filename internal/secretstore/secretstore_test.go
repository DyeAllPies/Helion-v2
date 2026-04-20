// internal/secretstore/secretstore_test.go
//
// Feature 30 — envelope encryption unit tests.
//
// The crypto primitives come from the stdlib, so these tests
// focus on the secretstore's contract: round-trip correctness,
// authenticated-decryption semantics, rotation safety, and the
// defensive guards (zero-key rejection, version uniqueness,
// active-version immutability).
//
// None of the tests assert on ciphertext bytes — those depend
// on random nonces + DEKs. We test behaviour, not encoding.

package secretstore_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

// ── test helpers ──────────────────────────────────────────

// freshKEK returns 32 random bytes.
func freshKEK(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secretstore.KEKSize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatalf("rand read: %v", err)
	}
	return k
}

// newRing returns a KeyRing with version=1 and a fresh KEK.
func newRing(t *testing.T) *secretstore.KeyRing {
	t.Helper()
	k, err := secretstore.NewKeyRing(1, freshKEK(t))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	return k
}

// ── Round trip ────────────────────────────────────────────

func TestEnvelope_EncryptDecrypt_RoundTrip(t *testing.T) {
	ring := newRing(t)
	plaintexts := [][]byte{
		[]byte("hf_sekret"),
		[]byte(""),                   // empty value is legal
		[]byte("a"),                  // single byte
		bytes.Repeat([]byte{'x'}, 1<<12), // 4 KiB
	}
	for _, pt := range plaintexts {
		env, err := ring.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		if env.KEKVersion != 1 {
			t.Errorf("KEKVersion = %d, want 1", env.KEKVersion)
		}
		if len(env.Nonce) != secretstore.NonceSize {
			t.Errorf("Nonce length = %d, want %d", len(env.Nonce), secretstore.NonceSize)
		}
		if len(env.WrappedDEKNonce) != secretstore.NonceSize {
			t.Errorf("WrappedDEKNonce length = %d, want %d", len(env.WrappedDEKNonce), secretstore.NonceSize)
		}
		// Sanity: ciphertext must not equal plaintext.
		if bytes.Equal(env.Ciphertext, pt) {
			t.Errorf("ciphertext equals plaintext — encryption broken")
		}
		got, err := ring.Decrypt(env)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if !bytes.Equal(got, pt) {
			t.Errorf("round-trip mismatch: got %q, want %q", got, pt)
		}
	}
}

func TestEnvelope_ManyEncrypts_DistinctNoncesAndDEKs(t *testing.T) {
	// Fresh nonces + fresh DEKs means two Encrypt calls of
	// the SAME plaintext must produce distinct ciphertext.
	// This is the load-bearing safety property for GCM.
	ring := newRing(t)
	pt := []byte("same plaintext")
	a, err := ring.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt a: %v", err)
	}
	b, err := ring.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt b: %v", err)
	}
	if bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Fatalf("two encrypts of same plaintext produced identical ciphertext — nonce reuse?")
	}
	if bytes.Equal(a.Nonce, b.Nonce) {
		t.Fatalf("two encrypts reused the value nonce")
	}
	if bytes.Equal(a.WrappedDEKNonce, b.WrappedDEKNonce) {
		t.Fatalf("two encrypts reused the DEK nonce")
	}
	if bytes.Equal(a.WrappedDEK, b.WrappedDEK) {
		t.Fatalf("two encrypts produced identical wrapped DEK — DEK reuse?")
	}
}

// ── Tamper detection ─────────────────────────────────────

func TestEnvelope_Tamper_DecryptFails(t *testing.T) {
	ring := newRing(t)
	pt := []byte("hf_sekret")

	makeEnv := func(t *testing.T) *secretstore.EncryptedEnvValue {
		t.Helper()
		env, err := ring.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		return env
	}

	cases := []struct {
		name string
		tweak func(env *secretstore.EncryptedEnvValue)
	}{
		{"flip ciphertext byte", func(env *secretstore.EncryptedEnvValue) { env.Ciphertext[0] ^= 0x01 }},
		{"flip nonce byte", func(env *secretstore.EncryptedEnvValue) { env.Nonce[0] ^= 0x01 }},
		{"flip wrapped DEK byte", func(env *secretstore.EncryptedEnvValue) { env.WrappedDEK[0] ^= 0x01 }},
		{"flip wrapped DEK nonce byte", func(env *secretstore.EncryptedEnvValue) { env.WrappedDEKNonce[0] ^= 0x01 }},
		{"truncate ciphertext", func(env *secretstore.EncryptedEnvValue) {
			env.Ciphertext = env.Ciphertext[:len(env.Ciphertext)-1]
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := makeEnv(t)
			c.tweak(env)
			got, err := ring.Decrypt(env)
			if err == nil {
				t.Fatalf("tamper accepted: got plaintext %q", got)
			}
			if got != nil {
				t.Fatalf("tamper returned non-nil plaintext alongside error: %q", got)
			}
			if !errors.Is(err, secretstore.ErrEnvelopeCorrupt) {
				t.Errorf("want ErrEnvelopeCorrupt, got %v", err)
			}
		})
	}
}

// ── Wrong KEK ────────────────────────────────────────────

func TestEnvelope_WrongKEK_DecryptFails(t *testing.T) {
	// A DEK wrapped under KEK v1 cannot be unwrapped with a
	// different 32-byte KEK, even if version-tagged to match.
	// Simulate by constructing two rings with distinct KEKs
	// at version 1, then trying to cross-decrypt.
	ringA, err := secretstore.NewKeyRing(1, freshKEK(t))
	if err != nil {
		t.Fatalf("ringA: %v", err)
	}
	ringB, err := secretstore.NewKeyRing(1, freshKEK(t))
	if err != nil {
		t.Fatalf("ringB: %v", err)
	}
	env, err := ringA.Encrypt([]byte("hf_sekret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := ringB.Decrypt(env)
	if err == nil {
		t.Fatalf("cross-ring decrypt succeeded: %q", got)
	}
	if !errors.Is(err, secretstore.ErrEnvelopeCorrupt) {
		t.Errorf("want ErrEnvelopeCorrupt, got %v", err)
	}
}

// ── Unknown KEK version ──────────────────────────────────

func TestEnvelope_UnknownKEKVersion_ErrorsClean(t *testing.T) {
	ring := newRing(t)
	env, _ := ring.Encrypt([]byte("hf_sekret"))
	env.KEKVersion = 999 // version not in the ring
	_, err := ring.Decrypt(env)
	if !errors.Is(err, secretstore.ErrKEKVersionUnknown) {
		t.Fatalf("want ErrKEKVersionUnknown, got %v", err)
	}
}

// ── Rotation ─────────────────────────────────────────────

func TestRotation_AddSetActive_NewEncryptsUseNewVersion(t *testing.T) {
	ring := newRing(t)
	oldV1 := ring.ActiveVersion()
	if oldV1 != 1 {
		t.Fatalf("initial active: %d", oldV1)
	}

	// Old envelope is stamped v1.
	oldEnv, _ := ring.Encrypt([]byte("old"))
	if oldEnv.KEKVersion != 1 {
		t.Errorf("old env version: %d", oldEnv.KEKVersion)
	}

	// Add v2 and make it active.
	if err := ring.AddKEK(2, freshKEK(t)); err != nil {
		t.Fatalf("AddKEK: %v", err)
	}
	if err := ring.SetActive(2); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// New envelope is stamped v2.
	newEnv, _ := ring.Encrypt([]byte("new"))
	if newEnv.KEKVersion != 2 {
		t.Errorf("new env version: %d", newEnv.KEKVersion)
	}

	// Old envelope still decrypts (ring still has v1).
	got, err := ring.Decrypt(oldEnv)
	if err != nil {
		t.Fatalf("Decrypt old: %v", err)
	}
	if !bytes.Equal(got, []byte("old")) {
		t.Errorf("old decrypt: got %q", got)
	}
}

func TestRotation_Rewrap_MigratesVersion(t *testing.T) {
	ring := newRing(t)
	env, _ := ring.Encrypt([]byte("hf_sekret"))
	if env.KEKVersion != 1 {
		t.Fatalf("pre-rewrap version: %d", env.KEKVersion)
	}

	_ = ring.AddKEK(2, freshKEK(t))
	_ = ring.SetActive(2)

	rewrapped, err := ring.Rewrap(env)
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if rewrapped.KEKVersion != 2 {
		t.Errorf("rewrapped version: %d, want 2", rewrapped.KEKVersion)
	}

	got, err := ring.Decrypt(rewrapped)
	if err != nil {
		t.Fatalf("Decrypt rewrapped: %v", err)
	}
	if !bytes.Equal(got, []byte("hf_sekret")) {
		t.Errorf("rewrap mangled plaintext: %q", got)
	}

	// Rewrapping an already-current envelope is a no-op.
	noop, err := ring.Rewrap(rewrapped)
	if err != nil {
		t.Fatalf("Rewrap no-op: %v", err)
	}
	if noop != rewrapped {
		t.Errorf("no-op rewrap should return same pointer")
	}
}

func TestRotation_RemoveKEK_RejectsActive(t *testing.T) {
	ring := newRing(t)
	err := ring.RemoveKEK(1)
	if err == nil {
		t.Fatalf("RemoveKEK on active version must error")
	}
	if !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Errorf("want ErrInvalidKEK, got %v", err)
	}
}

func TestRotation_RemoveKEK_DropsOldVersion(t *testing.T) {
	ring := newRing(t)
	_ = ring.AddKEK(2, freshKEK(t))
	_ = ring.SetActive(2)

	if err := ring.RemoveKEK(1); err != nil {
		t.Fatalf("RemoveKEK: %v", err)
	}
	// Envelopes stamped v1 no longer decrypt.
	env, _ := ring.Encrypt([]byte("x")) // v2
	env.KEKVersion = 1
	_, err := ring.Decrypt(env)
	if !errors.Is(err, secretstore.ErrKEKVersionUnknown) {
		t.Errorf("post-remove v1 decrypt: want ErrKEKVersionUnknown, got %v", err)
	}
}

// ── KeyRing construction guards ──────────────────────────

func TestNewKeyRing_RejectsZeroVersion(t *testing.T) {
	_, err := secretstore.NewKeyRing(0, freshKEK(t))
	if err == nil || !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Fatalf("zero version: want ErrInvalidKEK, got %v", err)
	}
}

func TestNewKeyRing_RejectsWrongLengthKEK(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		_, err := secretstore.NewKeyRing(1, make([]byte, n))
		if err == nil || !errors.Is(err, secretstore.ErrInvalidKEK) {
			t.Errorf("len=%d: want ErrInvalidKEK, got %v", n, err)
		}
	}
}

func TestNewKeyRing_RejectsAllZeroKEK(t *testing.T) {
	_, err := secretstore.NewKeyRing(1, make([]byte, secretstore.KEKSize))
	if err == nil || !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Fatalf("all-zero KEK: want ErrInvalidKEK, got %v", err)
	}
}

func TestAddKEK_RejectsDuplicateVersion(t *testing.T) {
	ring := newRing(t)
	err := ring.AddKEK(1, freshKEK(t))
	if err == nil {
		t.Fatalf("duplicate version accepted")
	}
	if !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Errorf("want ErrInvalidKEK, got %v", err)
	}
}

func TestSetActive_RejectsUnknownVersion(t *testing.T) {
	ring := newRing(t)
	err := ring.SetActive(99)
	if err == nil || !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Fatalf("unknown SetActive: want ErrInvalidKEK, got %v", err)
	}
}

// ── ParseKEK ─────────────────────────────────────────────

func TestParseKEK_Hex(t *testing.T) {
	raw := freshKEK(t)
	hexStr := hex.EncodeToString(raw)
	got, err := secretstore.ParseKEK(hexStr)
	if err != nil {
		t.Fatalf("ParseKEK hex: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("hex round-trip mismatch")
	}
}

func TestParseKEK_Base64(t *testing.T) {
	raw := freshKEK(t)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		b64 := enc.EncodeToString(raw)
		got, err := secretstore.ParseKEK(b64)
		if err != nil {
			t.Errorf("ParseKEK(%q): %v", b64, err)
			continue
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("base64 round-trip mismatch")
		}
	}
}

func TestParseKEK_TrimsWhitespace(t *testing.T) {
	raw := freshKEK(t)
	hexStr := "  " + hex.EncodeToString(raw) + "\n"
	got, err := secretstore.ParseKEK(hexStr)
	if err != nil {
		t.Fatalf("ParseKEK whitespace: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("whitespace-trimmed round-trip mismatch")
	}
}

func TestParseKEK_RejectsShortInput(t *testing.T) {
	for _, input := range []string{
		"",
		"  ",
		"deadbeef",
		"too short",
	} {
		_, err := secretstore.ParseKEK(input)
		if err == nil || !errors.Is(err, secretstore.ErrInvalidKEK) {
			t.Errorf("%q: want ErrInvalidKEK, got %v", input, err)
		}
	}
}

func TestParseKEK_RejectsAllZeroDecoded(t *testing.T) {
	hexZeros := hex.EncodeToString(make([]byte, secretstore.KEKSize))
	_, err := secretstore.ParseKEK(hexZeros)
	if err == nil || !errors.Is(err, secretstore.ErrInvalidKEK) {
		t.Fatalf("all-zero KEK: want ErrInvalidKEK, got %v", err)
	}
}
