// internal/api/webauthn_login_happy_test.go
//
// Exercises the login-begin happy path (credentials registered,
// ceremony returns assertion options). Complements the
// stale-session / no-credentials / non-admin tests in
// webauthn_extras_test.go.

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"

	wauthn "github.com/DyeAllPies/Helion-v2/internal/webauthn"
)

// seedCredential inserts a synthesised credential for `subject`
// so login-begin's allow-list is non-empty. id disambiguates
// so multiple seeds per fixture don't collide.
func seedCredential(t *testing.T, store wauthn.CredentialStore, subject string, id []byte) {
	t.Helper()
	rec := &wauthn.CredentialRecord{
		Credential: webauthnlib.Credential{
			ID:        id,
			PublicKey: []byte("fake-pubkey-bytes"),
			Authenticator: webauthnlib.Authenticator{
				SignCount: 0,
			},
		},
		UserHandle:   wauthn.UserHandleFor(subject),
		OperatorCN:   subject,
		Label:        "test-key",
		RegisteredAt: time.Now().UTC(),
		RegisteredBy: "user:" + subject,
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

func TestWebAuthnLoginBegin_WithCredential_ReturnsChallenge(t *testing.T) {
	srv, tm, _, credStore := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	seedCredential(t, credStore, "alice", []byte{0xde, 0xad, 0xbe, 0xef})

	rr := doWithToken(srv, "POST", "/admin/webauthn/login-begin", `{}`, tok)
	if rr.Code != 200 {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	if !contains2(rr.Body.String(), `"publicKey"`) {
		t.Errorf("response missing publicKey: %s", rr.Body.String())
	}
}

func TestWebAuthnListCredentials_PopulatedStore_Admin_Ok(t *testing.T) {
	srv, tm, _, credStore := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	seedCredential(t, credStore, "alice", []byte{0x01, 0x02})
	seedCredential(t, credStore, "bob", []byte{0x03, 0x04})
	rr := doWithToken(srv, "GET", "/admin/webauthn/credentials", "", tok)
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnLoginFinish_BogusBody_401(t *testing.T) {
	// Full round-trip past session Pop + ListByOperator: seed
	// a credential, call login-begin to create the session,
	// then call login-finish with a body the library can't
	// parse. Expected 401 "assertion verification failed",
	// which covers the FinishLogin error branch + the audit
	// emission.
	srv, tm, _, credStore := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	seedCredential(t, credStore, "alice", []byte{0xde, 0xad, 0xbe, 0xef})

	rr := doWithToken(srv, "POST", "/admin/webauthn/login-begin", `{}`, tok)
	if rr.Code != 200 {
		t.Fatalf("login-begin: %d %s", rr.Code, rr.Body.String())
	}

	// Bogus assertion — library rejects.
	rr = doWithToken(srv, "POST", "/admin/webauthn/login-finish",
		`{"id":"bogus","rawId":"bogus","type":"public-key","response":{"authenticatorData":"","clientDataJSON":"","signature":"","userHandle":""}}`, tok)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 from bogus assertion, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnRegisterFinish_BogusBody_500(t *testing.T) {
	// Same pattern for register-finish: register-begin to
	// populate the session, then register-finish with a body
	// the library can't parse.
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/webauthn/register-begin",
		`{"operator_cn":"alice"}`, tok)
	if rr.Code != 200 {
		t.Fatalf("register-begin: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "POST", "/admin/webauthn/register-finish",
		`{"id":"bogus","rawId":"bogus","type":"public-key","response":{"attestationObject":"","clientDataJSON":""}}`, tok)
	// Library returns an error → handler maps to 500.
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusBadRequest {
		t.Fatalf("want 500 or 400 from bogus attestation, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnRevokeCredential_ExistingCred_204(t *testing.T) {
	srv, tm, _, credStore := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	seedCredential(t, credStore, "alice", []byte{0xde, 0xad, 0xbe, 0xef})

	// credential ID is 0xdeadbeef → base64url "3q2-7w"
	rr := doWithToken(srv, "DELETE",
		"/admin/webauthn/credentials/3q2-7w", "", tok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}

	// Confirm gone from list.
	rr = doWithToken(srv, "GET", "/admin/webauthn/credentials", "", tok)
	if !contains2(rr.Body.String(), `"total":0`) && !contains2(rr.Body.String(), `"credentials":[]`) {
		// Tolerant check; just ensure 3q2-7w is no longer listed.
		if contains2(rr.Body.String(), "3q2-7w") {
			t.Errorf("credential still listed after revoke: %s", rr.Body.String())
		}
	}
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
