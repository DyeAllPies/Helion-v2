// Package webauthn is the feature-34 FIDO2 / WebAuthn layer
// for the Helion coordinator.
//
// Role
// ────
// Feature 27 raises authentication to cert-mTLS. Feature 33
// binds a JWT to a specific cert CN. But the browser itself
// is still a trusted runtime — a malicious extension, a
// supply-chain compromise of a dashboard dependency, or a
// remote-code-exec against the browser process can all use
// the installed cert to sign arbitrary requests. The private
// key lives in the browser's keychain; the browser is the
// attacker's runtime.
//
// WebAuthn fixes this by moving the key into a hardware
// authenticator (YubiKey, Apple Secure Enclave, Windows
// Hello TPM) that requires physical user interaction (button
// press, fingerprint, face scan) for each signature.
// Malicious in-browser code can ASK for a signature, but the
// hardware refuses without user touch. A compromised browser
// can no longer silently authenticate.
//
// This package owns:
//
//   - CredentialRecord — the on-disk view of a registered
//     credential including the raw `webauthn.Credential`
//     embedding (public key, sign count, attestation
//     metadata).
//   - CredentialStore — persisted credentials keyed by
//     credential_id, with per-operator lookup.
//   - SessionStore — short-lived begin/finish session state
//     (challenge + user handle) held in memory.
//   - user — the `webauthn.User` implementation the library
//     expects.
//
// Safety properties
// ─────────────────
//
//  1. **Hardware-bound signatures.** The authenticator's
//     private key never leaves the device. Every assertion
//     requires a fresh user-presence signal.
//
//  2. **Replay-resistance.** Authenticators monotonically
//     bump a `signCount`; the finish-login path rejects any
//     assertion whose counter is not strictly greater than
//     the stored value (§7.2 of the WebAuthn spec).
//
//  3. **Challenge-bound.** `webauthn.BeginRegistration` /
//     `BeginLogin` produce fresh 32-byte random challenges
//     the authenticator signs. A stale challenge cannot be
//     replayed because the session state is single-use and
//     TTL-capped.
//
//  4. **Audited mutations.** Register / login / revoke each
//     emit a distinct audit event with the credential_id,
//     operator CN, and the admin principal who acted. The
//     library rejects any signature that fails verification
//     BEFORE we record a success event — a failed assertion
//     only produces a failure-event record.
//
//  5. **Public keys are public.** The on-disk CredentialRecord
//     is not secret; an attacker who exfiltrates the Badger
//     store still cannot mint assertions without the
//     hardware device.
//
// Non-goals
// ─────────
//
//   - **Passkeys / cross-device FIDO2.** Apple/Google passkey
//     sync reintroduces a "compromise the cloud account,
//     compromise the credential" story; deferred per the
//     feature 34 spec.
//
//   - **Attestation-trust verification via FIDO MDS.** MDS
//     integration is available via go-webauthn's metadata
//     provider; deferred. We accept any well-formed
//     attestation; the hardware-bound-signature property is
//     the load-bearing control.
//
//   - **User-verification PIN/biometric enforcement.** The
//     library's UserVerification: required option is
//     available but deferred until the operator-guide path
//     is stable.
package webauthn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

// ── Errors ─────────────────────────────────────────────────

// ErrCredentialNotFound is returned by Get / Delete when the
// credential ID does not exist in the store.
var ErrCredentialNotFound = errors.New("webauthn: credential not found")

// ErrSessionNotFound is returned when a login-finish or
// register-finish request references a session that expired
// or was already consumed.
var ErrSessionNotFound = errors.New("webauthn: session not found or expired")

// ErrReplay is returned when the assertion's signCount is not
// strictly greater than the stored value — the WebAuthn
// replay-protection invariant.
var ErrReplay = errors.New("webauthn: replay detected (signCount did not advance)")

// ── CredentialRecord ──────────────────────────────────────

// CredentialRecord is the Helion on-disk shape for a
// registered WebAuthn credential.
//
// The embedded `Credential` is the full go-webauthn record
// including public key, sign count, transports, and
// attestation metadata. We persist it verbatim so future
// library upgrades can re-verify against FIDO MDS without
// requiring re-registration.
//
// The surrounding fields are Helion-specific audit /
// lookup metadata.
type CredentialRecord struct {
	// Credential is the raw go-webauthn credential struct.
	// Carries the credential ID, public key, sign count,
	// transports, and attestation bytes.
	Credential webauthnlib.Credential `json:"credential"`

	// UserHandle is the opaque byte sequence associated with
	// this credential's owning user account. Derived once at
	// registration (see UserHandleFor) and reused on every
	// subsequent login for the same operator.
	UserHandle []byte `json:"user_handle"`

	// OperatorCN is the JWT subject (human identifier) of
	// the operator who owns this credential. Indexed for
	// list-by-operator queries.
	OperatorCN string `json:"operator_cn"`

	// Label is an operator-supplied nickname ("yubikey-5c-
	// blue", "macbook-touch-id") for the authenticator.
	// Optional; defaults to "" — UIs render the credential
	// ID prefix in that case.
	Label string `json:"label,omitempty"`

	// BoundCertCN, when non-empty, ties this credential to a
	// specific operator mTLS cert CN. The feature-34 +
	// feature-33 pairing: WebAuthn-minted JWTs for a
	// BoundCertCN-attached credential carry `required_cn:
	// <BoundCertCN>` so the three factors (cert key, hardware
	// touch, JWT) must all match the same operator.
	BoundCertCN string `json:"bound_cert_cn,omitempty"`

	// RegisteredAt records when the credential was first
	// stored. Immutable.
	RegisteredAt time.Time `json:"registered_at"`

	// RegisteredBy is the Principal ID of the admin who
	// ran the register-finish flow. Usually equal to
	// "user:<OperatorCN>" for self-registration.
	RegisteredBy string `json:"registered_by"`

	// LastUsedAt is updated on every successful login.
	// Purely audit / ops — authentication decisions never
	// consult this value.
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// ID returns the credential ID bytes. Convenience accessor
// so callers don't have to reach into the embedded struct.
func (r *CredentialRecord) ID() []byte {
	return r.Credential.ID
}

// IDHex returns the credential ID as lowercase base64url
// (no padding). Matches the browser's `credential.id`
// string form.
func (r *CredentialRecord) IDHex() string {
	return EncodeCredentialID(r.Credential.ID)
}

// EncodeCredentialID is the canonical wire encoding for a
// credential ID: base64url with no padding. Matches the
// `BufferSource` → `.toString()` path browsers use. Exported
// so handlers + tests share the same formatter.
func EncodeCredentialID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}

// DecodeCredentialID is the inverse of EncodeCredentialID.
// Tolerates both padded (`base64.URLEncoding`) and unpadded
// (`base64.RawURLEncoding`) forms because some clients emit
// one and some the other.
func DecodeCredentialID(s string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// UserHandleFor derives a deterministic 32-byte user handle
// from an operator's JWT subject. The subject is the stable
// per-operator identifier; hashing it gives us a
// maximum-64-byte opaque value WebAuthn's spec requires AND
// lets a second credential register to the SAME user handle
// (multiple authenticators per operator).
//
// Deliberately NOT the raw subject bytes — that would leak
// the subject value to anyone who reads the WebAuthn request
// payload from the client, which violates the user-handle
// privacy recommendation (§5.4.3).
func UserHandleFor(subject string) []byte {
	sum := sha256.Sum256([]byte("helion-webauthn-user-handle-v1:" + subject))
	return sum[:]
}

// ── CredentialStore ───────────────────────────────────────

// CredentialStore is the narrow persistence interface the
// register + login + admin handlers depend on. The MemStore
// implementation in memstore.go satisfies it for tests + dev;
// the Badger-backed BadgerStore (badger.go) is the production
// wiring.
type CredentialStore interface {
	// Create inserts a new credential. Returns an error if
	// the credential ID already exists — the caller treats
	// that as a signal the authenticator was already
	// registered (common operator mistake + a harmless no-op
	// from the hardware's perspective).
	Create(ctx context.Context, rec *CredentialRecord) error

	// Get returns the credential keyed by credentialID.
	// Returns ErrCredentialNotFound on a missing key.
	Get(ctx context.Context, credentialID []byte) (*CredentialRecord, error)

	// ListByOperator returns every credential owned by the
	// operator whose user handle matches. The matcher uses
	// UserHandleFor on the subject to derive the handle.
	// Ordering: RegisteredAt descending (newest first).
	ListByOperator(ctx context.Context, userHandle []byte) ([]*CredentialRecord, error)

	// List returns every credential in the store, ordered
	// by RegisteredAt descending. Admin-facing.
	List(ctx context.Context) ([]*CredentialRecord, error)

	// Delete removes a credential. Idempotent: deleting a
	// missing ID returns nil so an admin who clicks revoke
	// twice doesn't see an error.
	Delete(ctx context.Context, credentialID []byte) error

	// UpdateSignCount persists a new sign count after a
	// successful assertion. Fails closed on a non-increasing
	// count — the caller is expected to have already rejected
	// the assertion.
	UpdateSignCount(ctx context.Context, credentialID []byte, signCount uint32) error
}

// ── SessionStore ─────────────────────────────────────────

// SessionPurpose names the two begin/finish ceremonies the
// WebAuthn library runs. The session store keys on
// (subject, purpose) so a simultaneous register + login
// from the same operator doesn't collide.
type SessionPurpose string

const (
	PurposeRegister SessionPurpose = "register"
	PurposeLogin    SessionPurpose = "login"
)

// SessionStore holds short-lived go-webauthn session data
// between the begin and finish halves of a ceremony.
// Entries TTL-expire; the handlers treat a missing session
// as authoritative "this was stale, make the caller start
// over".
//
// Thread-safe. Safe to share across a coordinator's entire
// request surface.
type SessionStore struct {
	mu   sync.Mutex
	data map[string]sessionEntry
	ttl  time.Duration
}

type sessionEntry struct {
	data    webauthnlib.SessionData
	expires time.Time
}

// NewSessionStore returns a store whose entries live for
// `ttl` (5 minutes is the spec-recommended upper bound for
// a registration ceremony).
func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &SessionStore{data: map[string]sessionEntry{}, ttl: ttl}
}

// Put stores session data keyed by (subject, purpose).
// Overwrites any prior entry — a re-begin for the same
// subject replaces the previous challenge so a hung client
// doesn't block retries.
func (s *SessionStore) Put(subject string, purpose SessionPurpose, session webauthnlib.SessionData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[sessionKey(subject, purpose)] = sessionEntry{
		data:    session,
		expires: time.Now().Add(s.ttl),
	}
}

// Pop returns the stored session for (subject, purpose) and
// removes it. Single-use semantics — a replay of finish
// against the same challenge always fails.
//
// Returns ErrSessionNotFound if the entry is missing OR has
// expired.
func (s *SessionStore) Pop(subject string, purpose SessionPurpose) (webauthnlib.SessionData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(subject, purpose)
	entry, ok := s.data[key]
	if !ok {
		return webauthnlib.SessionData{}, ErrSessionNotFound
	}
	delete(s.data, key)
	if time.Now().After(entry.expires) {
		return webauthnlib.SessionData{}, ErrSessionNotFound
	}
	return entry.data, nil
}

// Sweep removes every expired entry. Callers run this from a
// background cron; the Pop path also filters expired entries
// so the sweep is purely an optimisation.
func (s *SessionStore) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, entry := range s.data {
		if now.After(entry.expires) {
			delete(s.data, k)
		}
	}
}

func sessionKey(subject string, purpose SessionPurpose) string {
	return string(purpose) + ":" + subject
}

// ── user implementation ───────────────────────────────────

// User is the webauthn.User implementation the library
// expects. Constructed per-request from the operator's
// subject + their registered credentials.
type User struct {
	handle      []byte
	name        string
	displayName string
	credentials []webauthnlib.Credential
}

// NewUser builds a go-webauthn User for the given subject.
// `creds` is the set of previously-registered credentials;
// at registration time it's empty, at login time it's the
// result of ListByOperator.
func NewUser(subject string, creds []webauthnlib.Credential) *User {
	return &User{
		handle:      UserHandleFor(subject),
		name:        subject,
		displayName: subject,
		credentials: creds,
	}
}

// WebAuthnID implements webauthn.User.
func (u *User) WebAuthnID() []byte { return u.handle }

// WebAuthnName implements webauthn.User.
func (u *User) WebAuthnName() string { return u.name }

// WebAuthnDisplayName implements webauthn.User.
func (u *User) WebAuthnDisplayName() string { return u.displayName }

// WebAuthnCredentials implements webauthn.User.
func (u *User) WebAuthnCredentials() []webauthnlib.Credential { return u.credentials }

// ── Helpers ────────────────────────────────────────────────

// SortByRegistered sorts records newest-first. Used by
// both the Mem and Badger list paths.
func SortByRegistered(recs []*CredentialRecord) {
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].RegisteredAt.After(recs[j].RegisteredAt)
	})
}

// verifyNotReplay guards the feature-34 replay-resistance
// invariant: the assertion's new signCount must be strictly
// greater than the stored value OR the stored value was
// zero (first-ever use). Some authenticators (passkeys,
// some platform authenticators) return 0 forever; we allow
// that case but otherwise refuse a non-advancing counter.
func verifyNotReplay(stored, observed uint32) error {
	if stored == 0 && observed == 0 {
		return nil // counter not implemented by this authenticator; spec allows
	}
	if observed <= stored {
		return fmt.Errorf("%w: stored=%d observed=%d", ErrReplay, stored, observed)
	}
	return nil
}
