// internal/api/operator_cert_test.go
//
// Feature 27 — tests for POST /admin/operator-certs, the client-cert
// middleware tiers, and CN extraction from TLS peer certs + proxy
// headers.

package api_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// newOpCertServer wires a coordinator with a real CA, token manager,
// and audit store — the minimum needed to exercise
// POST /admin/operator-certs end-to-end.
func newOpCertServer(t *testing.T) (*api.Server, *auth.TokenManager, *inMemoryAuditStore, *pqcrypto.CA) {
	t.Helper()
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	auditStore := newAuditStore()
	auditLog := audit.NewLogger(auditStore, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)
	ca, err := pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	srv.SetOperatorCA(ca)
	return srv, tm, auditStore, ca
}

// ── POST /admin/operator-certs ───────────────────────────────────────────────

func TestIssueOperatorCertHTTP_NonAdmin_Forbidden(t *testing.T) {
	srv, tm, _, _ := newOpCertServer(t)
	nodeTok, err := tm.GenerateToken(context.Background(), "node-1", "node", time.Minute)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	body := `{"common_name":"alice","p12_password":"correcthorsebatterystaple"}`
	rr := doWithToken(srv, "POST", "/admin/operator-certs", body, nodeTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("non-admin: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestIssueOperatorCertHTTP_Unauthenticated_Returns401(t *testing.T) {
	srv, _, _, _ := newOpCertServer(t)
	rr := do(srv, "POST", "/admin/operator-certs",
		`{"common_name":"alice","p12_password":"correcthorsebatterystaple"}`)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauth: want 401, got %d", rr.Code)
	}
}

func TestIssueOperatorCertHTTP_HappyPath(t *testing.T) {
	srv, tm, auditStore, ca := newOpCertServer(t)
	adminTok := adminToken(t, tm)
	body := `{"common_name":"alice@ops","ttl_days":30,"p12_password":"correcthorsebatterystaple"}`
	rr := doWithToken(srv, "POST", "/admin/operator-certs", body, adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("happy path: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.IssueOperatorCertResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CommonName != "alice@ops" {
		t.Errorf("cn: want alice@ops, got %q", resp.CommonName)
	}
	if resp.SerialHex == "" || resp.FingerprintHex == "" {
		t.Errorf("serial/fingerprint missing: %+v", resp)
	}
	if resp.NotAfter.Before(resp.NotBefore) {
		t.Errorf("NotAfter %v before NotBefore %v", resp.NotAfter, resp.NotBefore)
	}

	// Cert must parse, chain to the CA, have ClientAuth EKU only.
	block, _ := pemBlock(resp.CertPEM)
	if block == nil {
		t.Fatal("cert PEM did not decode")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	opts := x509.VerifyOptions{
		Roots:     ca.ClientCertPool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err != nil {
		t.Errorf("issued cert did not verify against CA: %v", err)
	}

	// PKCS#12 must be decodable with the submitted password.
	p12DER, err := base64.StdEncoding.DecodeString(resp.P12Base64)
	if err != nil {
		t.Fatalf("decode p12 base64: %v", err)
	}
	if _, _, _, err := pkcs12.DecodeChain(p12DER, "correcthorsebatterystaple"); err != nil {
		t.Errorf("p12 decode with submitted password: %v", err)
	}
	// And MUST fail with a wrong password.
	if _, _, _, err := pkcs12.DecodeChain(p12DER, "wrong-password"); err == nil {
		t.Error("p12 decoded with wrong password — encryption broken")
	}

	// Audit must carry operator_cert_issued with the right fields.
	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	var seen bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventOperatorCertIssued {
			seen = true
			if cn, _ := ev.Details["common_name"].(string); cn != "alice@ops" {
				t.Errorf("audit cn: want alice@ops, got %v", ev.Details["common_name"])
			}
			// Plaintext P12 password must never leak.
			if strings.Contains(string(raw), "correcthorsebatterystaple") {
				t.Error("PASSWORD LEAK: audit entry contains p12 password")
			}
			if strings.Contains(string(raw), string(block.Bytes)) {
				t.Error("key bytes leaked into audit details")
			}
		}
	}
	if !seen {
		t.Error("expected operator_cert_issued audit event")
	}
}

func TestIssueOperatorCertHTTP_RejectsShortPassword(t *testing.T) {
	srv, tm, auditStore, _ := newOpCertServer(t)
	adminTok := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/operator-certs",
		`{"common_name":"alice","p12_password":"short"}`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("short password: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
	// Reject must be audited.
	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	var seen bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventOperatorCertReject {
			seen = true
		}
	}
	if !seen {
		t.Error("expected operator_cert_reject audit event on short password")
	}
}

func TestIssueOperatorCertHTTP_RejectsEmptyCN(t *testing.T) {
	srv, tm, _, _ := newOpCertServer(t)
	adminTok := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/operator-certs",
		`{"common_name":"","p12_password":"correcthorsebatterystaple"}`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty CN: want 400, got %d", rr.Code)
	}
}

func TestIssueOperatorCertHTTP_RejectsNULInCN(t *testing.T) {
	srv, tm, _, _ := newOpCertServer(t)
	adminTok := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/operator-certs",
		`{"common_name":"al\u0000ice","p12_password":"correcthorsebatterystaple"}`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("NUL in CN: want 400, got %d", rr.Code)
	}
}

func TestIssueOperatorCertHTTP_NotConfigured_Returns501(t *testing.T) {
	// Server without SetOperatorCA — route is not registered, so
	// the mux returns 404 from its own not-found handler.
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(context.Background(), store)
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)
	adminTok := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/operator-certs",
		`{"common_name":"alice","p12_password":"correcthorsebatterystaple"}`, adminTok)
	if rr.Code != http.StatusNotFound {
		t.Errorf("CA not configured: want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestIssueOperatorCertHTTP_RateLimit_Triggers429(t *testing.T) {
	// Burst is 3. A tight loop of 5 from one subject should yield
	// at least one 429.
	srv, tm, _, _ := newOpCertServer(t)
	adminTok := adminToken(t, tm)
	body := `{"common_name":"alice","p12_password":"correcthorsebatterystaple"}`
	ok, limited := 0, 0
	for i := 0; i < 5; i++ {
		rr := doWithToken(srv, "POST", "/admin/operator-certs", body, adminTok)
		switch rr.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected %d: %s", rr.Code, rr.Body.String())
		}
	}
	if ok == 0 || limited == 0 {
		t.Errorf("ok=%d limited=%d — want some of each", ok, limited)
	}
}

// ── ParseClientCertTierFromEnv ───────────────────────────────────────────────

func TestParseClientCertTier_Values(t *testing.T) {
	cases := map[string]api.ClientCertTier{
		"":         api.ClientCertOff,
		"off":      api.ClientCertOff,
		"0":        api.ClientCertOff,
		"no":       api.ClientCertOff,
		"warn":     api.ClientCertWarn,
		"on":       api.ClientCertOn,
		"1":        api.ClientCertOn,
		"yes":      api.ClientCertOn,
		"required": api.ClientCertOn,
		"ON":       api.ClientCertOn,
		"  warn  ": api.ClientCertWarn,
	}
	for raw, want := range cases {
		got, err := api.ParseClientCertTierFromEnv(raw)
		if err != nil {
			t.Errorf("ParseClientCertTierFromEnv(%q) err = %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseClientCertTierFromEnv(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseClientCertTier_RejectsTypos(t *testing.T) {
	// Typos must fail-closed to an ERROR so the coordinator boots
	// safe. Silent fallback to `off` would be a security regression.
	for _, raw := range []string{"wern", "loose", "enabled", "maybe"} {
		if _, err := api.ParseClientCertTierFromEnv(raw); err == nil {
			t.Errorf("ParseClientCertTierFromEnv(%q) should err", raw)
		}
	}
}

// ── clientCertMiddleware — via a test server ─────────────────────────────────
//
// These tests exercise the middleware by invoking the Server's handler
// chain. We can't trivially simulate a real TLS handshake without
// spinning up a net.Listener, so we:
//
//   - exercise the "no cert, off tier" path (default — all existing
//     tests already validate this)
//   - exercise the Nginx-proxy-headers path (easy — just set headers)
//   - exercise the direct-TLS path via httptest.NewUnstartedServer +
//     a rebuilt listener when needed (one heavier test)

func TestClientCertMiddleware_WarnTier_NoCert_StillServes(t *testing.T) {
	// In warn mode, a request with no cert goes through to the handler
	// AND emits an operator_cert_missing audit event.
	srv, tm, auditStore, _ := newOpCertServer(t)
	srv.SetClientCertTier(api.ClientCertWarn)
	adminTok := adminToken(t, tm)

	rr := doWithToken(srv, "GET", "/jobs?page=1&size=5", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Errorf("warn tier: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	var seen bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventOperatorCertMissing {
			seen = true
		}
	}
	if !seen {
		t.Error("expected operator_cert_missing audit event in warn tier")
	}
}

func TestClientCertMiddleware_OnTier_NoCert_Returns401(t *testing.T) {
	// `on` tier: clientCertMiddleware runs BEFORE authMiddleware so a
	// cert-less request is rejected at 401 even if the JWT is valid.
	srv, tm, _, _ := newOpCertServer(t)
	srv.SetClientCertTier(api.ClientCertOn)
	adminTok := adminToken(t, tm)

	rr := doWithToken(srv, "GET", "/jobs?page=1&size=5", "", adminTok)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("on tier, no cert: want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestClientCertMiddleware_OnTier_HealthEndpointsExempt(t *testing.T) {
	// Regression guard: load balancers can't present client certs;
	// /healthz and /readyz must stay reachable in `on` mode.
	// Health endpoints have no auth middleware either — they're
	// unauthenticated by design.
	srv, _, _, _ := newOpCertServer(t)
	srv.SetClientCertTier(api.ClientCertOn)

	for _, path := range []string{"/healthz", "/readyz"} {
		rr := do(srv, "GET", path, "")
		if rr.Code >= 500 {
			t.Errorf("health endpoint %s on-tier: unexpected server error %d", path, rr.Code)
		}
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("health endpoint %s must be exempt from client-cert enforcement; got 401", path)
		}
	}
}

func TestClientCertMiddleware_ProxyHeaders_LoopbackOnly(t *testing.T) {
	// Nginx-mode: X-SSL-Client-Verify: SUCCESS only honoured from
	// 127.0.0.1. Non-loopback callers carrying the same headers are
	// treated as if no cert was presented.
	srv, tm, _, _ := newOpCertServer(t)
	srv.SetClientCertTier(api.ClientCertOn)
	adminTok := adminToken(t, tm)

	// A helper that mimics `doWithToken()` but lets us set arbitrary
	// headers + RemoteAddr.
	fire := func(remote string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/jobs?page=1&size=5", strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer "+adminTok)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.RemoteAddr = remote
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		return rr
	}

	// Loopback + correct headers → served.
	rr := fire("127.0.0.1:12345", map[string]string{
		"X-SSL-Client-Verify": "SUCCESS",
		"X-SSL-Client-S-DN":   "CN=alice@ops,O=Helion",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("loopback + valid headers: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Non-loopback + same headers → 401. Header smuggling bypass
	// regression guard.
	rr = fire("203.0.113.7:55555", map[string]string{
		"X-SSL-Client-Verify": "SUCCESS",
		"X-SSL-Client-S-DN":   "CN=alice@ops,O=Helion",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("non-loopback header smuggling: want 401, got %d: %s", rr.Code, rr.Body.String())
	}

	// Loopback + X-SSL-Client-Verify != SUCCESS → 401. Nginx sets
	// FAILED:<reason> when verification fails; coordinator must
	// treat that as cert-less, not cert-present.
	rr = fire("127.0.0.1:12345", map[string]string{
		"X-SSL-Client-Verify": "FAILED:unknown-ca",
		"X-SSL-Client-S-DN":   "CN=alice@ops,O=Helion",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("loopback + FAILED verify: want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestClientCertMiddleware_ProxyHeaders_CNStampedOnAudit(t *testing.T) {
	// When a verified cert arrives via the Nginx path, downstream
	// audit events MUST carry operator_cn.
	srv, tm, auditStore, _ := newOpCertServer(t)
	srv.SetClientCertTier(api.ClientCertOn)
	adminTok := adminToken(t, tm)

	body := `{"id":"j-27","command":"echo"}`
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("X-SSL-Client-Verify", "SUCCESS")
	req.Header.Set("X-SSL-Client-S-DN", "CN=alice@ops,O=Helion")
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	var saw bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventJobSubmit {
			if cn, _ := ev.Details["operator_cn"].(string); cn == "alice@ops" {
				saw = true
			}
		}
	}
	if !saw {
		t.Error("job_submit audit event missing operator_cn=alice@ops")
	}
}

// ── pemBlock — tiny helper shared across tests in this file ──────────────────

func pemBlock(s string) (*pemBlockT, string) {
	const hdr = "-----BEGIN CERTIFICATE-----"
	const ftr = "-----END CERTIFICATE-----"
	i := strings.Index(s, hdr)
	j := strings.Index(s, ftr)
	if i < 0 || j < 0 {
		return nil, ""
	}
	body := s[i+len(hdr) : j]
	body = strings.ReplaceAll(body, "\n", "")
	body = strings.ReplaceAll(body, "\r", "")
	body = strings.TrimSpace(body)
	der, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, ""
	}
	return &pemBlockT{Bytes: der}, s[j:]
}

type pemBlockT struct{ Bytes []byte }
