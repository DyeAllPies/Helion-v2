// internal/webauthn/webauthn_test.go
//
// Unit tests for the webauthn package. The WebAuthn
// ceremony itself (register / login verification) is
// covered end-to-end by the handler integration tests in
// internal/api — here we focus on the store contracts +
// helper correctness.

package webauthn_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"

	"github.com/DyeAllPies/Helion-v2/internal/webauthn"
)

// ── Backend matrix: run every test against BOTH stores ───

type backend struct {
	name    string
	factory func(t *testing.T) webauthn.CredentialStore
}

func backends(t *testing.T) []backend {
	return []backend{
		{"MemStore", func(*testing.T) webauthn.CredentialStore { return webauthn.NewMemStore() }},
		{"BadgerStore", func(t *testing.T) webauthn.CredentialStore {
			opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
			db, err := badger.Open(opts)
			if err != nil {
				t.Fatalf("badger.Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			return webauthn.NewBadgerStore(db)
		}},
	}
}

func forEachStore(t *testing.T, fn func(t *testing.T, s webauthn.CredentialStore)) {
	t.Helper()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) { fn(t, b.factory(t)) })
	}
}

// makeRecord returns a minimal but valid CredentialRecord.
// A synthesised credential ID + public key + sign count is
// enough to exercise the store contract; the actual
// cryptographic content is tested by the WebAuthn library.
func makeRecord(id []byte, subject string, signCount uint32) *webauthn.CredentialRecord {
	return &webauthn.CredentialRecord{
		Credential: webauthnlib.Credential{
			ID:        id,
			PublicKey: []byte("fake-pubkey-bytes"),
			Authenticator: webauthnlib.Authenticator{
				SignCount: signCount,
			},
		},
		UserHandle:   webauthn.UserHandleFor(subject),
		OperatorCN:   subject,
		Label:        "test-key",
		RegisteredAt: time.Now().UTC(),
		RegisteredBy: "user:" + subject,
	}
}

// ── Create / Get round trip ──────────────────────────────

func TestStore_CreateGet_RoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		rec := makeRecord([]byte{0x01, 0x02, 0x03}, "alice", 0)
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Get(ctx, rec.Credential.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(got.Credential.ID, rec.Credential.ID) {
			t.Errorf("ID mismatch: got %x", got.Credential.ID)
		}
		if got.OperatorCN != "alice" {
			t.Errorf("OperatorCN: got %q", got.OperatorCN)
		}
	})
}

func TestStore_Create_DuplicateCredentialID(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		rec := makeRecord([]byte{0xaa, 0xbb}, "alice", 0)
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create first: %v", err)
		}
		err := s.Create(ctx, rec)
		if err == nil {
			t.Fatalf("want duplicate error")
		}
	})
}

func TestStore_Get_Missing_ErrCredentialNotFound(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		_, err := s.Get(context.Background(), []byte{0xff, 0xff})
		if !errors.Is(err, webauthn.ErrCredentialNotFound) {
			t.Fatalf("want ErrCredentialNotFound, got %v", err)
		}
	})
}

// ── ListByOperator ──────────────────────────────────────

func TestStore_ListByOperator_ScopesPerUser(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		_ = s.Create(ctx, makeRecord([]byte{0x01}, "alice", 0))
		_ = s.Create(ctx, makeRecord([]byte{0x02}, "alice", 0))
		_ = s.Create(ctx, makeRecord([]byte{0x03}, "bob", 0))

		aliceList, err := s.ListByOperator(ctx, webauthn.UserHandleFor("alice"))
		if err != nil {
			t.Fatalf("ListByOperator: %v", err)
		}
		if len(aliceList) != 2 {
			t.Fatalf("alice: want 2, got %d", len(aliceList))
		}
		bobList, _ := s.ListByOperator(ctx, webauthn.UserHandleFor("bob"))
		if len(bobList) != 1 {
			t.Fatalf("bob: want 1, got %d", len(bobList))
		}
		// Unknown operator → empty.
		noneList, _ := s.ListByOperator(ctx, webauthn.UserHandleFor("carol"))
		if len(noneList) != 0 {
			t.Errorf("carol: want 0, got %d", len(noneList))
		}
	})
}

func TestStore_ListByOperator_OrderedNewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		now := time.Now().UTC()
		recA := makeRecord([]byte{0x01}, "alice", 0)
		recA.RegisteredAt = now.Add(-2 * time.Hour)
		recB := makeRecord([]byte{0x02}, "alice", 0)
		recB.RegisteredAt = now.Add(-1 * time.Hour)
		recC := makeRecord([]byte{0x03}, "alice", 0)
		recC.RegisteredAt = now
		_ = s.Create(ctx, recA)
		_ = s.Create(ctx, recB)
		_ = s.Create(ctx, recC)
		list, _ := s.ListByOperator(ctx, webauthn.UserHandleFor("alice"))
		if len(list) != 3 || list[0].Credential.ID[0] != 0x03 ||
			list[1].Credential.ID[0] != 0x02 || list[2].Credential.ID[0] != 0x01 {
			t.Fatalf("ordering broken: %+v", list)
		}
	})
}

// ── Delete idempotency + reverse-index cleanup ──────────

func TestStore_Delete_IsIdempotent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		if err := s.Delete(ctx, []byte{0xff}); err != nil {
			t.Errorf("missing delete: %v", err)
		}
	})
}

func TestStore_Delete_RemovesReverseIndex(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		_ = s.Create(ctx, makeRecord([]byte{0x01}, "alice", 0))
		_ = s.Create(ctx, makeRecord([]byte{0x02}, "alice", 0))
		if err := s.Delete(ctx, []byte{0x01}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// Get on the deleted ID is ErrCredentialNotFound.
		if _, err := s.Get(ctx, []byte{0x01}); !errors.Is(err, webauthn.ErrCredentialNotFound) {
			t.Errorf("post-delete Get: got %v", err)
		}
		// Operator list now has only the remaining cred.
		list, _ := s.ListByOperator(ctx, webauthn.UserHandleFor("alice"))
		if len(list) != 1 || list[0].Credential.ID[0] != 0x02 {
			t.Errorf("reverse-index not cleaned: %+v", list)
		}
	})
}

// ── SignCount advancement + replay ──────────────────────

func TestStore_UpdateSignCount_Advances(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		_ = s.Create(ctx, makeRecord([]byte{0x01}, "alice", 5))
		if err := s.UpdateSignCount(ctx, []byte{0x01}, 7); err != nil {
			t.Fatalf("UpdateSignCount: %v", err)
		}
		got, _ := s.Get(ctx, []byte{0x01})
		if got.Credential.Authenticator.SignCount != 7 {
			t.Errorf("want 7, got %d", got.Credential.Authenticator.SignCount)
		}
	})
}

func TestStore_UpdateSignCount_RefusesReplay(t *testing.T) {
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		_ = s.Create(ctx, makeRecord([]byte{0x01}, "alice", 10))
		err := s.UpdateSignCount(ctx, []byte{0x01}, 9)
		if !errors.Is(err, webauthn.ErrReplay) {
			t.Fatalf("want ErrReplay, got %v", err)
		}
	})
}

func TestStore_UpdateSignCount_AllowsZeroStay(t *testing.T) {
	// Some authenticators (passkeys, some platform ones)
	// never advance the counter — they stay at 0. We must
	// allow that case or legitimate logins would 401.
	forEachStore(t, func(t *testing.T, s webauthn.CredentialStore) {
		ctx := context.Background()
		_ = s.Create(ctx, makeRecord([]byte{0x01}, "alice", 0))
		if err := s.UpdateSignCount(ctx, []byte{0x01}, 0); err != nil {
			t.Errorf("stay-at-zero rejected: %v", err)
		}
	})
}

// ── Helpers ─────────────────────────────────────────────

func TestUserHandleFor_Deterministic(t *testing.T) {
	a := webauthn.UserHandleFor("alice")
	b := webauthn.UserHandleFor("alice")
	c := webauthn.UserHandleFor("alice2")
	if !bytes.Equal(a, b) {
		t.Errorf("same subject → different handles")
	}
	if bytes.Equal(a, c) {
		t.Errorf("different subjects → same handle")
	}
	if len(a) != 32 {
		t.Errorf("handle length: got %d", len(a))
	}
}

func TestEncodeDecodeCredentialID_RoundTrip(t *testing.T) {
	id := []byte{0x00, 0x01, 0xff, 0xab, 0xcd, 0xef}
	enc := webauthn.EncodeCredentialID(id)
	if _, err := base64.RawURLEncoding.DecodeString(enc); err != nil {
		t.Fatalf("encoded not RawURLEncoded: %v", err)
	}
	got, err := webauthn.DecodeCredentialID(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got, id) {
		t.Errorf("round trip mismatch")
	}
}

func TestDecodeCredentialID_AcceptsPadded(t *testing.T) {
	// Some clients emit standard URL-safe base64 with padding.
	id := []byte{0x01, 0x02, 0x03, 0x04}
	padded := base64.URLEncoding.EncodeToString(id)
	got, err := webauthn.DecodeCredentialID(padded)
	if err != nil {
		t.Fatalf("padded decode: %v", err)
	}
	if !bytes.Equal(got, id) {
		t.Errorf("padded round trip mismatch")
	}
}

// ── SessionStore ────────────────────────────────────────

func TestSessionStore_PutPopRoundTrip(t *testing.T) {
	store := webauthn.NewSessionStore(time.Minute)
	session := webauthnlib.SessionData{UserID: []byte{0x01}, Challenge: "abc"}
	store.Put("alice", webauthn.PurposeLogin, session)
	got, err := store.Pop("alice", webauthn.PurposeLogin)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if !bytes.Equal(got.UserID, session.UserID) || got.Challenge != "abc" {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestSessionStore_Pop_SingleUse(t *testing.T) {
	store := webauthn.NewSessionStore(time.Minute)
	store.Put("alice", webauthn.PurposeLogin, webauthnlib.SessionData{})
	if _, err := store.Pop("alice", webauthn.PurposeLogin); err != nil {
		t.Fatalf("first Pop: %v", err)
	}
	if _, err := store.Pop("alice", webauthn.PurposeLogin); !errors.Is(err, webauthn.ErrSessionNotFound) {
		t.Errorf("second Pop should be ErrSessionNotFound, got %v", err)
	}
}

func TestSessionStore_Pop_ExpiredReturnsNotFound(t *testing.T) {
	store := webauthn.NewSessionStore(10 * time.Millisecond)
	store.Put("alice", webauthn.PurposeLogin, webauthnlib.SessionData{})
	time.Sleep(25 * time.Millisecond)
	_, err := store.Pop("alice", webauthn.PurposeLogin)
	if !errors.Is(err, webauthn.ErrSessionNotFound) {
		t.Errorf("expired Pop: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionStore_PurposesDontCollide(t *testing.T) {
	store := webauthn.NewSessionStore(time.Minute)
	store.Put("alice", webauthn.PurposeRegister, webauthnlib.SessionData{Challenge: "reg"})
	store.Put("alice", webauthn.PurposeLogin, webauthnlib.SessionData{Challenge: "login"})
	reg, err := store.Pop("alice", webauthn.PurposeRegister)
	if err != nil || reg.Challenge != "reg" {
		t.Fatalf("register pop: %v %+v", err, reg)
	}
	login, err := store.Pop("alice", webauthn.PurposeLogin)
	if err != nil || login.Challenge != "login" {
		t.Fatalf("login pop: %v %+v", err, login)
	}
}

func TestSessionStore_Sweep(t *testing.T) {
	store := webauthn.NewSessionStore(10 * time.Millisecond)
	store.Put("a", webauthn.PurposeLogin, webauthnlib.SessionData{})
	store.Put("b", webauthn.PurposeRegister, webauthnlib.SessionData{})
	time.Sleep(25 * time.Millisecond)
	store.Sweep()
	// Both should be gone.
	if _, err := store.Pop("a", webauthn.PurposeLogin); !errors.Is(err, webauthn.ErrSessionNotFound) {
		t.Errorf("a still present after sweep")
	}
	if _, err := store.Pop("b", webauthn.PurposeRegister); !errors.Is(err, webauthn.ErrSessionNotFound) {
		t.Errorf("b still present after sweep")
	}
}
