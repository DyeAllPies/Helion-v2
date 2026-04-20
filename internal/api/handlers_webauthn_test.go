// internal/api/handlers_webauthn_test.go
//
// Feature 34 — HTTP-layer integration tests. Focus on
// admin gating, register-begin shape, session TTL, list +
// revoke endpoints, and the `HELION_AUTH_WEBAUTHN_REQUIRED`
// tier enforcement. The actual attestation / assertion
// cryptography is exercised by go-webauthn/webauthn's own
// test suite; simulating a FIDO2 authenticator inside a Go
// unit test would require a COSE-key + CBOR round-trip that
// reimplements the library we're consuming.
//
// End-to-end register-finish + login-finish with a real
// authenticator is left for the operator guide's manual
// walk-through.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	wauthn "github.com/DyeAllPies/Helion-v2/internal/webauthn"
)

// webauthnFixture spins up an auth-enabled server with a
// real WebAuthn instance + MemStore credential store so the
// register-begin / list / revoke paths can be exercised end-
// to-end against live state.
func webauthnFixture(t *testing.T) (srv *api.Server, tm *auth.TokenManager, aStore *inMemoryAuditStore, credStore wauthn.CredentialStore) {
	t.Helper()
	tokStore := newTokenStore()
	tmgr, err := auth.NewTokenManager(context.Background(), tokStore)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	aStore = newAuditStore()
	aLog := audit.NewLogger(aStore, 0)

	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(js)
	srv = api.NewServer(adapter, nil, nil, aLog, tmgr, nil, nil, nil)

	waCfg := &webauthnlib.Config{
		RPID:          "localhost",
		RPDisplayName: "Helion Test",
		RPOrigins:     []string{"https://localhost"},
	}
	waLib, err := webauthnlib.New(waCfg)
	if err != nil {
		t.Fatalf("webauthn.New: %v", err)
	}
	credStore = wauthn.NewMemStore()
	srv.SetWebAuthn(waLib, credStore, wauthn.NewSessionStore(time.Minute))

	return srv, tmgr, aStore, credStore
}

func webauthnAdminToken(t *testing.T, tm *auth.TokenManager) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), "alice", "admin", time.Minute)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	return tok
}

func webauthnUserToken(t *testing.T, tm *auth.TokenManager) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), "alice", "user", time.Minute)
	if err != nil {
		t.Fatalf("user token: %v", err)
	}
	return tok
}

// ── register-begin ──────────────────────────────────────

func TestWebAuthn_RegisterBegin_ReturnsChallenge(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/webauthn/register-begin",
		`{"label":"test-yubikey"}`, tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pk, ok := body["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("response missing publicKey object: %s", rr.Body.String())
	}
	if pk["challenge"] == nil {
		t.Errorf("publicKey.challenge missing")
	}
	rp, _ := pk["rp"].(map[string]any)
	if rp["id"] != "localhost" {
		t.Errorf("rp.id: got %v, want localhost", rp["id"])
	}
}

func TestWebAuthn_RegisterBegin_NonAdmin_Forbidden(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnUserToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/webauthn/register-begin", `{}`, tok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("user: want 403, got %d", rr.Code)
	}
}

func TestWebAuthn_RegisterFinish_StaleSession_400(t *testing.T) {
	srv, tm, aStore, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)

	// Finish without begin → session not found → 400
	rr := doWithToken(srv, "POST", "/admin/webauthn/register-finish", `{}`, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}

	// Audit a reject event.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventWebAuthnRegisterReject {
			seen = true
			if ev.Details["reason"] != "session_expired" {
				t.Errorf("reason: %v", ev.Details["reason"])
			}
		}
	}
	if !seen {
		t.Fatalf("no EventWebAuthnRegisterReject")
	}
}

// ── list + revoke ───────────────────────────────────────

func TestWebAuthn_ListCredentials_ReturnsStored(t *testing.T) {
	srv, tm, _, credStore := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)

	// Seed a credential directly in the store.
	_ = credStore.Create(context.Background(), &wauthn.CredentialRecord{
		Credential: webauthnlib.Credential{
			ID:        []byte{0x01, 0x02, 0x03},
			PublicKey: []byte("fake"),
			Authenticator: webauthnlib.Authenticator{
				AAGUID: []byte{0xde, 0xad, 0xbe, 0xef},
			},
		},
		UserHandle:   wauthn.UserHandleFor("alice"),
		OperatorCN:   "alice",
		Label:        "laptop-yubikey",
		RegisteredAt: time.Now().UTC(),
		RegisteredBy: "user:root",
	})

	rr := doWithToken(srv, "GET", "/admin/webauthn/credentials", "", tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.WebAuthnCredentialsListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total: want 1, got %d", resp.Total)
	}
	got := resp.Credentials[0]
	if got.Label != "laptop-yubikey" || got.OperatorCN != "alice" {
		t.Errorf("item: %+v", got)
	}
	if got.AAGUIDHex != "deadbeef" {
		t.Errorf("AAGUIDHex: got %q", got.AAGUIDHex)
	}
}

func TestWebAuthn_ListCredentials_NonAdmin_Forbidden(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnUserToken(t, tm)
	rr := doWithToken(srv, "GET", "/admin/webauthn/credentials", "", tok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestWebAuthn_Revoke_DeletesRecordAndAudits(t *testing.T) {
	srv, tm, aStore, credStore := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)

	credID := []byte{0xaa, 0xbb, 0xcc}
	_ = credStore.Create(context.Background(), &wauthn.CredentialRecord{
		Credential: webauthnlib.Credential{
			ID: credID, PublicKey: []byte("fake"),
		},
		UserHandle:   wauthn.UserHandleFor("alice"),
		OperatorCN:   "alice",
		RegisteredAt: time.Now().UTC(),
	})

	encoded := wauthn.EncodeCredentialID(credID)
	rr := doWithToken(srv, "DELETE",
		"/admin/webauthn/credentials/"+encoded,
		`{"reason":"laptop stolen"}`, tok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Record is gone.
	if _, err := credStore.Get(context.Background(), credID); err == nil {
		t.Errorf("record still present after revoke")
	}

	// Audit fires.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventWebAuthnRevoked {
			seen = true
			if ev.Details["credential_id"] != encoded {
				t.Errorf("credential_id: got %v", ev.Details["credential_id"])
			}
			if ev.Details["reason"] != "laptop stolen" {
				t.Errorf("reason: got %v", ev.Details["reason"])
			}
		}
	}
	if !seen {
		t.Fatal("no EventWebAuthnRevoked audit entry")
	}
}

func TestWebAuthn_Revoke_Missing_Returns404(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "DELETE",
		"/admin/webauthn/credentials/"+wauthn.EncodeCredentialID([]byte{0xff}),
		`{"reason":"nope"}`, tok)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

// ── HELION_AUTH_WEBAUTHN_REQUIRED tier ──────────────────

// TestWebAuthnTier_On_RefusesBearerAdminRequest — with the
// tier set to `on`, every NON-webauthn-bootstrap admin
// endpoint refuses a plain bearer JWT with 401.
func TestWebAuthnTier_On_RefusesBearerAdminRequest(t *testing.T) {
	srv, tm, aStore, _ := webauthnFixture(t)
	srv.SetWebAuthnTier(wauthn.TierOn)

	tok := webauthnAdminToken(t, tm)
	// /admin/tokens is a vanilla admin endpoint (not a
	// webauthn bootstrap route) — it's the right probe.
	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"bob","role":"admin"}`, tok)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("tier=on bearer: want 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "WebAuthn-backed JWT required") {
		t.Errorf("body should mention WebAuthn: %s", rr.Body.String())
	}

	// Audit records the required event with enforced:true.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventWebAuthnRequired {
			seen = true
			if ev.Details["enforced"] != true {
				t.Errorf("enforced: got %v", ev.Details["enforced"])
			}
		}
	}
	if !seen {
		t.Fatal("no EventWebAuthnRequired entry")
	}
}

// TestWebAuthnTier_On_AllowsBootstrapRoutes — register/login
// begin/finish MUST stay callable with a bearer JWT even
// when tier is `on` (otherwise an operator could never
// register their first YubiKey).
func TestWebAuthnTier_On_AllowsBootstrapRoutes(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	srv.SetWebAuthnTier(wauthn.TierOn)

	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/register-begin",
		`{"label":"first-key"}`, tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("bootstrap register-begin: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestWebAuthnTier_Warn_LogsButAllows — `warn` tier serves
// the request but records the event. No 401.
func TestWebAuthnTier_Warn_LogsButAllows(t *testing.T) {
	srv, tm, aStore, _ := webauthnFixture(t)
	srv.SetWebAuthnTier(wauthn.TierWarn)

	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"bob","role":"admin","ttl_hours":1}`, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("warn: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventWebAuthnRequired {
			seen = true
			if ev.Details["enforced"] == true {
				t.Errorf("warn tier should NOT set enforced:true")
			}
		}
	}
	if !seen {
		t.Fatal("warn tier missed the audit")
	}
}

// TestWebAuthnTier_On_AllowsWebAuthnBackedToken — a token
// minted with auth_method=webauthn passes tier-on for any
// admin endpoint.
func TestWebAuthnTier_On_AllowsWebAuthnBackedToken(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	srv.SetWebAuthnTier(wauthn.TierOn)

	tok, err := tm.GenerateTokenWithClaims(context.Background(), auth.TokenClaims{
		Subject:    "alice",
		Role:       "admin",
		AuthMethod: "webauthn",
		TTL:        time.Minute,
	})
	if err != nil {
		t.Fatalf("mint webauthn token: %v", err)
	}

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"bob","role":"admin","ttl_hours":1}`, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("webauthn-backed token: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestWebAuthnTier_Off_Ignored — the default tier doesn't
// even consult auth_method. Back-compat.
func TestWebAuthnTier_Off_Ignored(t *testing.T) {
	srv, tm, aStore, _ := webauthnFixture(t)
	// No SetWebAuthnTier call → TierOff by default.

	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"bob","role":"admin","ttl_hours":1}`, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("default tier: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventWebAuthnRequired {
			t.Fatalf("default tier should not emit EventWebAuthnRequired")
		}
	}
}

// ── login-begin without credentials → 400 ───────────────

func TestWebAuthn_LoginBegin_NoCredentials_400(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/login-begin", ``, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("no-creds login-begin: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no WebAuthn credentials") {
		t.Errorf("body: %s", rr.Body.String())
	}
}

// ── login-finish without session → 400 ─────────────────

func TestWebAuthn_LoginFinish_StaleSession_400(t *testing.T) {
	srv, tm, aStore, credStore := webauthnFixture(t)
	// Need at least one credential for the lookup to get
	// far enough to check the session.
	_ = credStore.Create(context.Background(), &wauthn.CredentialRecord{
		Credential:   webauthnlib.Credential{ID: []byte{0x01}, PublicKey: []byte("fake")},
		UserHandle:   wauthn.UserHandleFor("alice"),
		OperatorCN:   "alice",
		RegisteredAt: time.Now().UTC(),
	})
	tok := webauthnAdminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/webauthn/login-finish", `{}`, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventWebAuthnLoginReject {
			seen = true
		}
	}
	if !seen {
		t.Fatal("no EventWebAuthnLoginReject audit entry")
	}
}
