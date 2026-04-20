// Package secretstore is the feature-30 envelope-encryption
// layer for secret env values at rest.
//
// Role
// ────
// Feature 26 redacts secret env values on every response path
// and mediates admin read-back via an audited endpoint. Feature
// 29 scrubs them from log output. But the Job record itself
// still travels to disk via `json.Marshal` with the plaintext
// sitting in `cpb.Job.Env`. An attacker with filesystem access
// to the coordinator — a disk snapshot, a stolen node image, a
// privileged ops user, or a backup archive — reads every
// declared secret by grepping the Badger store.
//
// This package moves secret values into a per-value encrypted
// envelope before they land on disk. Envelope design:
//
//   plaintext  --AES-256-GCM(DEK, nonce_v)-->  ciphertext
//   DEK        --AES-256-GCM(KEK, nonce_d)-->  wrapped_DEK
//
// The KEK ("key encryption key") is a single 32-byte secret the
// coordinator reads from its environment at boot (HELION_
// SECRETSTORE_KEK). The DEK ("data encryption key") is a fresh
// 32-byte value generated per encrypt. Both use AES-256-GCM
// with 12-byte random nonces. The on-disk form carries
// ciphertext + nonces + wrapped_DEK + a KEK version tag for
// rotation.
//
// Security properties
// ───────────────────
//
//   1. **Authenticated encryption.** AES-GCM's 16-byte tag
//      detects any bit-flip in ciphertext, nonce, wrapped-DEK,
//      wrapped-DEK nonce. A tampered envelope fails Decrypt
//      with a non-nil error and no plaintext byte is returned.
//
//   2. **Fresh DEK per encrypt.** Reusing a DEK across values
//      would let an attacker who compromises one envelope
//      learn something about others. Per-value DEKs keep the
//      blast radius of a compromised value to that value.
//
//   3. **Fresh nonces from crypto/rand.** GCM is catastrophically
//      broken by nonce reuse under the same key (a pair of
//      ciphertexts reveals the XOR of their plaintexts). The
//      spec's 12-byte random nonce at 2^32 encrypts per key has
//      a ~2^-32 collision probability — acceptable for our
//      per-value volumes (<<2^32) and the DEK is single-use
//      anyway.
//
//   4. **KEK is held in memory only.** The coordinator reads
//      the KEK once at boot from an env var (or KMS — deferred)
//      and keeps it in a locked map. It never goes into logs,
//      audit events, response bodies, or on-wire messages. A
//      process-memory dump would leak it; that's documented as
//      a key-compromise event.
//
//   5. **Rotation supported.** Every envelope carries a
//      KEKVersion. Adding a new KEK version does NOT
//      invalidate existing envelopes; a rewrap sweep migrates
//      them to the new version in the background. The active
//      KEK is used for new encrypts.
//
//   6. **Fail closed.** A wrong KEK, a tampered blob, a
//      missing KEKVersion entry in the keyring — all produce a
//      non-nil error. Callers that can't decrypt must not
//      proceed.
//
//   7. **Constant-time KEK lookup.** `map[uint32][]byte` is
//      fine here — the version tag is NOT secret (it's on disk
//      alongside the ciphertext), so a timing leak on the
//      lookup reveals nothing the attacker doesn't already have.
//
// Non-goals
// ─────────
//
//   - **Per-tenant KEKs** are deferred per the feature spec.
//     A single coordinator KEK is MVP.
//
//   - **Key derivation from a passphrase** is out of scope.
//     Operators are expected to supply a 32-byte key material
//     directly (base64 or hex). Passphrase-derivation adds
//     KDF complexity without improving the attack surface —
//     the env var IS the secret.
//
//   - **Core-dump protection** (mlock, memfd_secret) is
//     documented in SECURITY.md but not implemented here.
//     Linux-specific and brittle; "coordinator core dump is a
//     key-compromise event" is the operating assumption.
//
//   - **Reads without KEK** are impossible by design. A
//     deployment that had envelope-encrypted records and then
//     loses its KEK has lost those records. No "legacy decrypt
//     without KEK" path exists.
package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ── Constants ──────────────────────────────────────────────

// KEKSize is the byte length of a Key Encryption Key. AES-256
// requires 32 bytes.
const KEKSize = 32

// DEKSize is the byte length of a Data Encryption Key. Same
// as KEKSize — we use AES-256-GCM for both layers.
const DEKSize = 32

// NonceSize is the byte length of an AES-GCM nonce. GCM's
// default nonce size. A non-standard nonce size would need
// explicit `NewGCMWithNonceSize` plumbing; 12 is the spec-
// recommended length.
const NonceSize = 12

// ── Errors ─────────────────────────────────────────────────

// ErrInvalidKEK is returned when the raw KEK material fails
// validation (wrong length, unparseable encoding). Wrapped
// with fmt.Errorf for context so upstream logs carry the
// reason. Callers compare with errors.Is.
var ErrInvalidKEK = errors.New("secretstore: invalid KEK material")

// ErrKEKVersionUnknown is returned by Decrypt when the
// envelope names a KEKVersion the keyring does not have. A
// keyring that dropped an older version will hit this on
// legacy envelopes — deployments that rotated MUST keep the
// old KEK around until the rewrap sweep completes.
var ErrKEKVersionUnknown = errors.New("secretstore: KEK version not loaded")

// ErrEnvelopeCorrupt is returned by Decrypt when AES-GCM's
// authenticated-decryption tag fails to verify. Distinct
// sentinel (rather than the raw crypto/cipher error) so a
// wrapping audit-logger can tell "wrong key" from "tampered
// bytes".
var ErrEnvelopeCorrupt = errors.New("secretstore: envelope authentication failed (wrong KEK or tampered bytes)")

// ── EncryptedEnvValue ─────────────────────────────────────

// EncryptedEnvValue is the on-disk form of a single secret
// value. All fields are raw bytes; JSON marshaling encodes
// them as base64 per stdlib default.
//
// Field order is wire-visible (Badger records are JSON);
// adding fields is safe, reordering is NOT safe without a
// version bump because existing records would mismatch.
type EncryptedEnvValue struct {
	// Ciphertext is AES-256-GCM(DEK, Nonce) over the value
	// bytes, with the 16-byte GCM tag appended.
	Ciphertext []byte `json:"ciphertext"`

	// Nonce is the 12-byte random nonce used for the value
	// encryption. Never reused under the same DEK — and since
	// DEKs are single-use, never reused period.
	Nonce []byte `json:"nonce"`

	// WrappedDEK is AES-256-GCM(KEK, WrappedDEKNonce) over
	// the plaintext DEK bytes, with the GCM tag appended.
	WrappedDEK []byte `json:"wrapped_dek"`

	// WrappedDEKNonce is the 12-byte random nonce used for
	// the DEK wrapping. Distinct from Nonce — a per-envelope
	// pair of 12-byte random nonces.
	WrappedDEKNonce []byte `json:"wrapped_dek_nonce"`

	// KEKVersion identifies which KEK in the keyring wrapped
	// the DEK. Lets a coordinator hold multiple KEK versions
	// during a rotation window — legacy envelopes decrypt with
	// their original KEK while new encrypts use the active
	// KEK. Unsigned 32-bit so operators can increment forever
	// without hitting a ceiling at realistic rotation cadence.
	KEKVersion uint32 `json:"kek_version"`
}

// ── KeyRing ────────────────────────────────────────────────

// KeyRing holds one or more KEK versions, one of which is the
// "active" KEK used for new encrypts. Safe for concurrent use
// via an internal RWMutex.
//
// A KeyRing is constructed once at coordinator boot from the
// HELION_SECRETSTORE_KEK(_v<N>) env vars. Adding and removing
// KEKs at runtime happens through AddKEK / SetActive; those
// paths are admin-initiated and require a restart of the
// rotation workflow to reach consistency (see Rewrap).
type KeyRing struct {
	mu     sync.RWMutex
	keks   map[uint32][]byte // version → 32-byte KEK
	active uint32            // version selected for Encrypt
}

// NewKeyRing constructs a KeyRing with a single active KEK.
// activeVersion must be non-zero (zero is reserved for "no
// rotation ever happened", which an EncryptedEnvValue with
// KEKVersion=0 would claim — easier to debug if we disallow
// it outright).
func NewKeyRing(active uint32, kek []byte) (*KeyRing, error) {
	if active == 0 {
		return nil, fmt.Errorf("%w: active version must be non-zero", ErrInvalidKEK)
	}
	if err := validateKEK(kek); err != nil {
		return nil, err
	}
	// Defensive copy so the caller can't wipe the backing
	// slice out from under us.
	stored := append([]byte(nil), kek...)
	return &KeyRing{
		keks:   map[uint32][]byte{active: stored},
		active: active,
	}, nil
}

// AddKEK registers an additional KEK version. Rejects
// duplicate versions (accidentally replacing an in-use KEK
// would make all envelopes sealed under the old bytes
// undecryptable). The new KEK does NOT become active until
// SetActive is called.
func (k *KeyRing) AddKEK(version uint32, kek []byte) error {
	if version == 0 {
		return fmt.Errorf("%w: version must be non-zero", ErrInvalidKEK)
	}
	if err := validateKEK(kek); err != nil {
		return err
	}
	stored := append([]byte(nil), kek...)
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.keks[version]; exists {
		return fmt.Errorf("%w: version %d already registered", ErrInvalidKEK, version)
	}
	k.keks[version] = stored
	return nil
}

// SetActive selects which registered KEK version is used for
// new Encrypt calls. Existing envelopes stay decryptable with
// their original version as long as that version remains
// loaded. Refuses to set a version that wasn't AddKEK'd.
func (k *KeyRing) SetActive(version uint32) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.keks[version]; !exists {
		return fmt.Errorf("%w: version %d not in keyring", ErrInvalidKEK, version)
	}
	k.active = version
	return nil
}

// RemoveKEK drops an older KEK version from the keyring. ONLY
// safe after a rewrap sweep has migrated every extant envelope
// to a newer version. Removing a version that envelopes still
// reference makes those envelopes permanently undecryptable.
// Refuses to remove the active version (no path to recover
// from that).
func (k *KeyRing) RemoveKEK(version uint32) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if version == k.active {
		return fmt.Errorf("%w: cannot remove active version %d", ErrInvalidKEK, version)
	}
	if _, exists := k.keks[version]; !exists {
		return fmt.Errorf("%w: version %d not in keyring", ErrInvalidKEK, version)
	}
	// Best-effort wipe of the key bytes before dropping.
	kek := k.keks[version]
	for i := range kek {
		kek[i] = 0
	}
	delete(k.keks, version)
	return nil
}

// ActiveVersion returns the version currently used for new
// encrypts. Read under a lock so a concurrent SetActive is
// serialised against reads.
func (k *KeyRing) ActiveVersion() uint32 {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.active
}

// Versions returns every registered KEK version, sorted
// lowest → highest. Useful for diagnostics + the rotation
// admin endpoint's response body.
func (k *KeyRing) Versions() []uint32 {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]uint32, 0, len(k.keks))
	for v := range k.keks {
		out = append(out, v)
	}
	// Simple insertion sort; the slice is small (<10 in
	// practice) and we avoid pulling sort into this file.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ── Encrypt / Decrypt ──────────────────────────────────────

// Encrypt wraps plaintext in an envelope under the active KEK.
// The returned EncryptedEnvValue is safe to JSON-marshal and
// persist. Plaintext may be zero-length — the result is still
// distinguishable from a non-existent secret because the
// EncryptedEnv field is present.
//
// Time-constant in the input length (AES-GCM is); timing
// observers can distinguish an encrypt call from a
// no-secret-found code path, which is exactly the contract we
// want (the existence of a secret IS observable).
func (k *KeyRing) Encrypt(plaintext []byte) (*EncryptedEnvValue, error) {
	k.mu.RLock()
	kek, ok := k.keks[k.active]
	version := k.active
	k.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: active version %d not loaded (programmer error)", ErrInvalidKEK, version)
	}

	// Fresh DEK per encrypt.
	dek := make([]byte, DEKSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("secretstore.Encrypt: DEK gen: %w", err)
	}

	// Encrypt the value with DEK.
	valueNonce, ciphertext, err := gcmSeal(dek, plaintext)
	if err != nil {
		// Best-effort wipe before returning.
		wipe(dek)
		return nil, fmt.Errorf("secretstore.Encrypt: seal value: %w", err)
	}

	// Wrap the DEK with KEK.
	dekNonce, wrappedDEK, err := gcmSeal(kek, dek)
	// The DEK has served its purpose — wipe the plaintext copy
	// before any error return. We already sealed the value
	// above, so the ciphertext survives the wipe.
	wipe(dek)
	if err != nil {
		return nil, fmt.Errorf("secretstore.Encrypt: wrap DEK: %w", err)
	}

	return &EncryptedEnvValue{
		Ciphertext:      ciphertext,
		Nonce:           valueNonce,
		WrappedDEK:      wrappedDEK,
		WrappedDEKNonce: dekNonce,
		KEKVersion:      version,
	}, nil
}

// Decrypt unwraps an envelope and returns the plaintext.
// Errors on:
//
//   - nil envelope
//   - KEKVersion not loaded in the keyring
//   - tampered ciphertext / nonce / wrapped DEK (GCM tag
//     mismatch)
//
// No plaintext bytes are returned when err != nil.
func (k *KeyRing) Decrypt(env *EncryptedEnvValue) ([]byte, error) {
	if env == nil {
		return nil, fmt.Errorf("secretstore.Decrypt: nil envelope")
	}
	k.mu.RLock()
	kek, ok := k.keks[env.KEKVersion]
	k.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: envelope uses version %d", ErrKEKVersionUnknown, env.KEKVersion)
	}
	dek, err := gcmOpen(kek, env.WrappedDEKNonce, env.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("%w: unwrap DEK: %v", ErrEnvelopeCorrupt, err)
	}
	plaintext, err := gcmOpen(dek, env.Nonce, env.Ciphertext)
	// DEK plaintext is done; wipe even on success so a
	// subsequent core-dump doesn't include it.
	wipe(dek)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt value: %v", ErrEnvelopeCorrupt, err)
	}
	return plaintext, nil
}

// Rewrap decrypts the envelope under its recorded KEKVersion
// and re-encrypts under the active KEK. Used by the rotation
// sweep to migrate envelopes to the new version. Returns the
// same envelope when the recorded version already matches the
// active one (no-op — saves a re-encrypt's allocations).
func (k *KeyRing) Rewrap(env *EncryptedEnvValue) (*EncryptedEnvValue, error) {
	if env == nil {
		return nil, fmt.Errorf("secretstore.Rewrap: nil envelope")
	}
	active := k.ActiveVersion()
	if env.KEKVersion == active {
		return env, nil
	}
	plaintext, err := k.Decrypt(env)
	if err != nil {
		return nil, fmt.Errorf("secretstore.Rewrap: decrypt: %w", err)
	}
	out, err := k.Encrypt(plaintext)
	// Plaintext now lives in a fresh allocation and is
	// about to leave scope; best-effort wipe to shorten the
	// window a core dump could grab it.
	wipe(plaintext)
	if err != nil {
		return nil, fmt.Errorf("secretstore.Rewrap: encrypt: %w", err)
	}
	return out, nil
}

// ── Helpers ────────────────────────────────────────────────

// gcmSeal generates a fresh 12-byte nonce, seals plaintext
// with the given key, and returns the (nonce, ciphertext)
// pair. Abstracted so Encrypt and the DEK-wrapping step share
// the AES-GCM plumbing without duplicated constructors.
func gcmSeal(key, plaintext []byte) (nonce, ciphertext []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("NewGCM: %w", err)
	}
	nonce = make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("nonce: %w", err)
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, nil)
	return nonce, ciphertext, nil
}

// gcmOpen is the inverse of gcmSeal — returns the plaintext
// bytes or a non-nil error on tag mismatch. Returns nil
// plaintext on error (never a partial output).
func gcmOpen(key, nonce, ciphertext []byte) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("nonce wrong size: %d, want %d", len(nonce), NonceSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("NewGCM: %w", err)
	}
	pt, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return pt, nil
}

// validateKEK checks that kek is exactly KEKSize bytes and
// non-zero. All-zero key material is almost certainly an
// uninitialised buffer; reject to catch the most obvious
// operator mistake.
func validateKEK(kek []byte) error {
	if len(kek) != KEKSize {
		return fmt.Errorf("%w: need %d bytes, got %d", ErrInvalidKEK, KEKSize, len(kek))
	}
	allZero := true
	for _, b := range kek {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return fmt.Errorf("%w: all-zero bytes rejected (looks like uninitialised memory)", ErrInvalidKEK)
	}
	return nil
}

// wipe overwrites b with zeros. Best-effort — the Go compiler
// can still elide writes to "dead" memory, and secrets may
// have been copied elsewhere. Useful as a defence-in-depth
// for fresh allocations that are about to leave scope.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ── Key parsing ────────────────────────────────────────────

// ParseKEK decodes a KEK from an operator-supplied string.
// Accepts:
//
//   - 64-character lowercase or uppercase hex (32 bytes).
//   - Standard or URL-safe base64 with or without padding
//     that decodes to exactly 32 bytes.
//
// Rejects anything else with ErrInvalidKEK. Space-trimming is
// done up-front so an operator who copy-pastes with leading
// whitespace doesn't silently land on a wrong-length decode
// path.
//
// Side note: we avoid accepting the literal raw bytes from
// the env var because env-var encoding across shells and
// container runtimes is unreliable for non-ASCII content.
// Base64/hex are terminal-safe.
func ParseKEK(input string) ([]byte, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidKEK)
	}

	// Hex path: exactly 2*KEKSize chars, all hex digits.
	if len(input) == 2*KEKSize {
		if decoded, err := hex.DecodeString(input); err == nil {
			if err := validateKEK(decoded); err != nil {
				return nil, err
			}
			return decoded, nil
		}
	}

	// Base64 path: try standard and URL-safe, both with
	// and without padding. Decode must land on exactly
	// KEKSize bytes or we reject.
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(input)
		if err != nil {
			continue
		}
		if len(decoded) == KEKSize {
			if err := validateKEK(decoded); err != nil {
				return nil, err
			}
			return decoded, nil
		}
	}

	return nil, fmt.Errorf("%w: expected 64-char hex or base64-encoded 32 bytes", ErrInvalidKEK)
}
