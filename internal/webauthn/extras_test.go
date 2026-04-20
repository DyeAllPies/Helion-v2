// internal/webauthn/extras_test.go
//
// Coverage-focused tests for the smaller webauthn primitives
// that the matrix suite in webauthn_test.go doesn't exercise:
// the User adapter, tier parsing, and Store.List on both
// backends.

package webauthn_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"

	"github.com/DyeAllPies/Helion-v2/internal/webauthn"
)

// ── User adapter ─────────────────────────────────────────────

func TestNewUser_EmbedsHandleAndSubject(t *testing.T) {
	creds := []webauthnlib.Credential{
		{ID: []byte{0x01}, PublicKey: []byte("pk1")},
		{ID: []byte{0x02}, PublicKey: []byte("pk2")},
	}
	u := webauthn.NewUser("alice@ops", creds)

	wantHandle := webauthn.UserHandleFor("alice@ops")
	if !bytes.Equal(u.WebAuthnID(), wantHandle) {
		t.Errorf("WebAuthnID: mismatch")
	}
	if u.WebAuthnName() != "alice@ops" {
		t.Errorf("WebAuthnName: got %q", u.WebAuthnName())
	}
	if u.WebAuthnDisplayName() != "alice@ops" {
		t.Errorf("WebAuthnDisplayName: got %q", u.WebAuthnDisplayName())
	}
	got := u.WebAuthnCredentials()
	if len(got) != 2 {
		t.Fatalf("WebAuthnCredentials: got %d, want 2", len(got))
	}
}

func TestNewUser_EmptyCredentials_ReturnsEmpty(t *testing.T) {
	u := webauthn.NewUser("bob", nil)
	if c := u.WebAuthnCredentials(); len(c) != 0 {
		t.Errorf("want empty creds, got %d", len(c))
	}
}

// ── CredentialRecord accessors ───────────────────────────────

func TestCredentialRecord_IDHelpers(t *testing.T) {
	rec := makeRecord([]byte{0xde, 0xad, 0xbe, 0xef}, "alice", 0)
	if !bytes.Equal(rec.ID(), []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Errorf("ID(): got %x", rec.ID())
	}
	// Base64url no-pad: 0xdeadbeef → "3q2-7w"
	if rec.IDHex() != "3q2-7w" {
		t.Errorf("IDHex(): got %q, want %q", rec.IDHex(), "3q2-7w")
	}
}

// ── Tier parsing ─────────────────────────────────────────────

func TestParseTier_AllCases(t *testing.T) {
	cases := []struct {
		in   string
		want webauthn.Tier
	}{
		{"", webauthn.TierOff},
		{"off", webauthn.TierOff},
		{"OFF", webauthn.TierOff},
		{"  off ", webauthn.TierOff},
		{"warn", webauthn.TierWarn},
		{"Warn", webauthn.TierWarn},
		{"on", webauthn.TierOn},
		{"ON", webauthn.TierOn},
	}
	for _, c := range cases {
		got, err := webauthn.ParseTier(c.in)
		if err != nil {
			t.Errorf("ParseTier(%q): unexpected err: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseTier(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseTier_InvalidValue_Errors(t *testing.T) {
	_, err := webauthn.ParseTier("required")
	if err == nil {
		t.Fatal("want error for unknown tier value")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error should echo input, got %q", err.Error())
	}
}

func TestTier_StringRoundTrip(t *testing.T) {
	cases := []struct {
		t    webauthn.Tier
		want string
	}{
		{webauthn.TierOff, "off"},
		{webauthn.TierWarn, "warn"},
		{webauthn.TierOn, "on"},
	}
	for _, c := range cases {
		if got := c.t.String(); got != c.want {
			t.Errorf("Tier(%d).String() = %q, want %q", c.t, got, c.want)
		}
	}
	// Unknown (out-of-range) tier falls back to "off" rather
	// than panicking — defensive against future enum additions
	// that forget to update String().
	bad := webauthn.Tier(99)
	if got := bad.String(); got != "off" {
		t.Errorf("unknown tier String(): got %q, want %q", got, "off")
	}
}

// ── Store.List (both backends) ───────────────────────────────

func TestStore_List_NewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		// Three credentials across two operators, inserted in
		// ascending-time order. List must return all three in
		// descending-time order.
		a := makeRecord([]byte{0x01}, "alice", 0)
		b := makeRecord([]byte{0x02}, "bob", 0)
		c := makeRecord([]byte{0x03}, "alice", 0)
		b.RegisteredAt = a.RegisteredAt.Add(1)
		c.RegisteredAt = a.RegisteredAt.Add(2)
		for _, r := range []*webauthn.CredentialRecord{a, b, c} {
			if err := s.Create(ctx, r); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		got, err := s.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("List len: got %d, want 3", len(got))
		}
		// Newest (c) first, oldest (a) last.
		if !bytes.Equal(got[0].Credential.ID, c.Credential.ID) {
			t.Errorf("List[0] = %x, want %x (newest)", got[0].Credential.ID, c.Credential.ID)
		}
		if !bytes.Equal(got[2].Credential.ID, a.Credential.ID) {
			t.Errorf("List[2] = %x, want %x (oldest)", got[2].Credential.ID, a.Credential.ID)
		}
	})
}

func TestStore_List_EmptyStore_OkEmpty(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		got, err := s.List(context.Background())
		if err != nil {
			t.Fatalf("List on empty: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("empty store must return empty slice, got %d", len(got))
		}
	})
}

// ── BadgerStore.List read-error branch ───────────────────────

// closedDB wraps a Badger DB we close before List runs so the
// Badger iterator path surfaces its error branch. Gives the
// BadgerStore.List error-return path ≥ 1 execution.
func TestBadgerStore_List_DBClosed_ReturnsError(t *testing.T) {
	opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	s := webauthn.NewBadgerStore(db)
	// Seed one record so List has something to iterate over.
	_ = s.Create(context.Background(), makeRecord([]byte{0x01}, "alice", 0))

	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	_, err = s.List(context.Background())
	if err == nil {
		t.Fatal("want error from List on closed DB, got nil")
	}
}
