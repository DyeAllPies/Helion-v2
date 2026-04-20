// internal/api/handlers_secretstore_test.go
//
// Feature 30 — admin-endpoint tests for
// /admin/secretstore/{rotate,status}. These tests focus on
// authorization + response shape; the underlying sweep
// correctness is covered by TestEncryptedPersistence_Rotation
// in internal/cluster.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

// ── fake SecretStoreAdmin ────────────────────────────────

// fakeSecretAdmin is a minimal stand-in for the real
// BadgerJSONPersister admin surface. Isolating the handler
// tests from Badger keeps them fast and deterministic; the
// SecretStoreAdmin interface was created exactly for this.
type fakeSecretAdmin struct {
	ring    *secretstore.KeyRing
	rewrap  int
	scanned int
	err     error
	calls   int
}

func (f *fakeSecretAdmin) RewrapAll(_ context.Context) (int, int, error) {
	f.calls++
	return f.rewrap, f.scanned, f.err
}

func (f *fakeSecretAdmin) KeyRing() *secretstore.KeyRing { return f.ring }

// sentinelErr lets tests plant a typed error without pulling
// errors.New into every case.
type sentinelErr string

func (s sentinelErr) Error() string { return string(s) }

// newTestKeyring builds a KeyRing with a deterministic 32-byte
// KEK (reproducible test output). The actual crypto is
// exercised in the secretstore package tests.
func newTestKeyring(t *testing.T) *secretstore.KeyRing {
	t.Helper()
	kek := make([]byte, secretstore.KEKSize)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	ring, err := secretstore.NewKeyRing(1, kek)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	return ring
}

// ── Rotate happy path ────────────────────────────────────

func TestSecretStore_Rotate_HappyPath(t *testing.T) {
	adm := &fakeSecretAdmin{
		ring:    newTestKeyring(t),
		rewrap:  5,
		scanned: 3,
	}
	srv, adminTok, _, _ := secretStoreFixture(t, adm)

	rr := doWithToken(srv, "POST", "/admin/secretstore/rotate", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.SecretStoreRotateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RewrappedEnvelopes != 5 || resp.ScannedRecords != 3 {
		t.Errorf("counts: got %+v", resp)
	}
	if resp.ActiveVersion != 1 {
		t.Errorf("active: want 1, got %d", resp.ActiveVersion)
	}
	if adm.calls != 1 {
		t.Errorf("RewrapAll calls: %d", adm.calls)
	}
}

// ── Rotate emits audit event ─────────────────────────────

func TestSecretStore_Rotate_EmitsAuditEvent(t *testing.T) {
	adm := &fakeSecretAdmin{
		ring:    newTestKeyring(t),
		rewrap:  2,
		scanned: 1,
	}
	srv, adminTok, _, aStore := secretStoreFixture(t, adm)

	rr := doWithToken(srv, "POST", "/admin/secretstore/rotate", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	raw, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, body := range raw {
		var ev audit.Event
		_ = json.Unmarshal(body, &ev)
		if ev.Type != audit.EventSecretStoreRotate {
			continue
		}
		seen = true
		if ev.Principal != "user:root" {
			t.Errorf("principal: got %q", ev.Principal)
		}
		// Audit-event numeric details deserialise as float64
		// per the stdlib's interface-typed Unmarshal
		// semantics. Coerce before comparing.
		rew, _ := ev.Details["rewrapped_envelopes"].(float64)
		if int(rew) != 2 {
			t.Errorf("rewrapped detail: got %v", ev.Details["rewrapped_envelopes"])
		}
	}
	if !seen {
		t.Fatal("no EventSecretStoreRotate entry")
	}
}

// ── Non-admin forbidden ──────────────────────────────────

func TestSecretStore_Rotate_NonAdmin_Forbidden(t *testing.T) {
	adm := &fakeSecretAdmin{ring: newTestKeyring(t)}
	srv, _, userTok, _ := secretStoreFixture(t, adm)

	rr := doWithToken(srv, "POST", "/admin/secretstore/rotate", "", userTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rr.Code, rr.Body.String())
	}
	if adm.calls != 0 {
		t.Errorf("non-admin triggered sweep: %d calls", adm.calls)
	}
}

// ── Status endpoint ──────────────────────────────────────

func TestSecretStore_Status_ReportsRingState(t *testing.T) {
	adm := &fakeSecretAdmin{ring: newTestKeyring(t)}
	srv, adminTok, _, _ := secretStoreFixture(t, adm)

	rr := doWithToken(srv, "GET", "/admin/secretstore/status", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.SecretStoreStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled {
		t.Errorf("enabled: want true")
	}
	if resp.ActiveVersion != 1 {
		t.Errorf("active: want 1, got %d", resp.ActiveVersion)
	}
}

// ── Sweep failure surfaces 500 ───────────────────────────

func TestSecretStore_Rotate_SweepError_Returns500(t *testing.T) {
	adm := &fakeSecretAdmin{
		ring: newTestKeyring(t),
		err:  sentinelErr("badger: i/o error"),
	}
	srv, adminTok, _, _ := secretStoreFixture(t, adm)

	rr := doWithToken(srv, "POST", "/admin/secretstore/rotate", "", adminTok)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── Unconfigured admin ──────────────────────────────────

func TestSecretStore_Rotate_NotConfigured_404(t *testing.T) {
	// No SetSecretStoreAdmin call → routes not registered.
	// The mux returns its default 404.
	srv, adminTok, _, _ := secretStoreFixture(t, nil)

	rr := doWithToken(srv, "POST", "/admin/secretstore/rotate", "", adminTok)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}
