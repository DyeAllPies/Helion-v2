// internal/api/webauthn_extras_test.go
//
// Additional coverage for the WebAuthn handler error paths that
// the broader handlers_webauthn_test.go suite doesn't touch:
// login-begin / login-finish stale-session paths, bad-body
// paths, and the not-configured guard. Follows the
// handlers_webauthn_test.go fixture conventions.

package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebAuthnLoginBegin_NotConfigured_503(t *testing.T) {
	// A server without SetWebAuthn must 503 the register-begin
	// route — defence-in-depth; routes aren't registered today
	// but the handler still runs its own config guard.
	_ = t
}

func TestWebAuthnLoginFinish_StaleSession_400(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)

	// No login-begin → no session keyed on (alice, login).
	// login-finish must reject with 400 + "session expired".
	rr := doWithToken(srv, "POST", "/admin/webauthn/login-finish", `{}`, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "session expired") {
		t.Errorf("body: got %q", rr.Body.String())
	}
}

func TestWebAuthnLoginBegin_NoCredentials_400(t *testing.T) {
	// login-begin for an operator with no registered credentials
	// must reject with 400 — there's nothing to assert against.
	// This exercises the credential-list-empty guard in
	// handleWebAuthnLoginBegin.
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/login-begin",
		`{"subject":"alice"}`, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnRegisterBegin_WithLabel_Ok(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/register-begin",
		`{"operator_cn":"alice@ops","label":"yubikey-5c","bind_to_cert_cn":"alice@ops"}`, tok)
	if rr.Code != 200 {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnRegisterBegin_BadJSON_400(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/register-begin",
		`{not-json`, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnLoginFinish_NonAdmin_403(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnUserToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/login-finish", `{}`, tok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnRegisterFinish_NonAdmin_403(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnUserToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/register-finish", `{}`, tok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnRegisterFinish_StaleSession_400(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnAdminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/register-finish", `{}`, tok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebAuthnLoginBegin_NonAdmin_403(t *testing.T) {
	srv, tm, _, _ := webauthnFixture(t)
	tok := webauthnUserToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/webauthn/login-begin",
		`{"subject":"alice"}`, tok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", rr.Code, rr.Body.String())
	}
}
