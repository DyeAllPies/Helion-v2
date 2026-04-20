// internal/api/token_cn_binding_test.go
//
// Feature 33 — authMiddleware enforcement of the JWT ↔
// cert-CN binding. The pure auth-side claim round-trip is
// covered in internal/auth; these tests exercise the full
// HTTP flow: tokens with a `required_cn` claim reach the
// middleware with / without a matching operator cert CN,
// and the middleware either allows the request through or
// refuses it with 401 + an EventTokenCertCNMismatch audit
// entry.
//
// Requests with client certs are synthesised by populating
// `r.TLS.PeerCertificates` directly (same pattern as the
// feature-31 revocation tests). Avoids standing up a full
// TLS listener + keeps the tests fast.

package api_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// bindingFixture spins up a server wired with:
//   - the auth token manager (so tokens can be minted + validated)
//   - an in-memory audit store (so we can assert the mismatch
//     event emission)
//   - clientCertTier=warn so the middleware runs through the
//     cert-extraction path without rejecting cert-less requests
//     (letting us exercise "token requires CN but request is
//     cert-less" as a distinct case from "token requires CN but
//     clientCertTier is off so middleware is bypassed")
//   - a real pqcrypto CA so we can mint a peer cert whose
//     subject CN is observable by r.TLS
func bindingFixture(t *testing.T) (srv *api.Server, tm *auth.TokenManager, aStore *inMemoryAuditStore, ca *pqcrypto.CA) {
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

	ca, err = pqcrypto.NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	srv.SetOperatorCA(ca)
	srv.SetClientCertTier(api.ClientCertWarn)

	return srv, tmgr, aStore, ca
}

// mintOperatorLeaf returns an *x509.Certificate for the
// given CN, signed by the test CA. Used to populate the
// fake r.TLS on synthesised requests.
func mintOperatorLeaf(t *testing.T, ca *pqcrypto.CA, cn string) *x509.Certificate {
	t.Helper()
	certPEM, _, err := ca.IssueOperatorCert(cn, time.Hour)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatalf("no PEM block in issued cert")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return leaf
}

// buildRequestWithCert constructs an *http.Request carrying
// a populated TLS connection state so the coordinator's
// clientCertMiddleware extracts the given cert's CN. Set
// `leaf` to nil for a cert-less request.
func buildRequestWithCert(t *testing.T, method, path, bearer string, leaf *x509.Certificate) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if leaf != nil {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{leaf},
		}
	}
	req.RemoteAddr = "192.0.2.10:54321"
	return req
}

// ── Bound token + matching cert → allow ──────────────

func TestAuthMiddleware_RequiredCN_MatchesCertCN_Succeeds(t *testing.T) {
	srv, tm, _, ca := bindingFixture(t)
	tok, err := tm.GenerateTokenWithCN(
		context.Background(), "alice", "admin", "alice@ops", time.Minute)
	if err != nil {
		t.Fatalf("GenerateTokenWithCN: %v", err)
	}
	leaf := mintOperatorLeaf(t, ca, "alice@ops")

	req := buildRequestWithCert(t, "GET", "/jobs", tok, leaf)
	rr := newTestResponseRecorder()
	srv.Handler().ServeHTTP(rr, req)

	// Request passes middleware; may then 200/400/etc. downstream.
	// The contract is: NOT 401.
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("matching CN produced 401: %s", rr.Body.String())
	}
}

// ── Bound token + mismatched cert → 401 + audit ──────

func TestAuthMiddleware_RequiredCN_MismatchesCertCN_Returns401(t *testing.T) {
	srv, tm, aStore, ca := bindingFixture(t)
	tok, err := tm.GenerateTokenWithCN(
		context.Background(), "alice", "admin", "alice@ops", time.Minute)
	if err != nil {
		t.Fatalf("GenerateTokenWithCN: %v", err)
	}
	// Bob's cert, not Alice's.
	leaf := mintOperatorLeaf(t, ca, "bob@ops")

	req := buildRequestWithCert(t, "GET", "/jobs", tok, leaf)
	rr := newTestResponseRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("mismatched CN: want 401, got %d: %s", rr.Code, rr.Body.String())
	}

	// Audit event must fire with required_cn + observed_cn.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventTokenCertCNMismatch {
			continue
		}
		seen = true
		if ev.Details["required_cn"] != "alice@ops" {
			t.Errorf("required_cn: got %v", ev.Details["required_cn"])
		}
		if ev.Details["observed_cn"] != "bob@ops" {
			t.Errorf("observed_cn: got %v", ev.Details["observed_cn"])
		}
		if ev.Details["subject"] != "alice" {
			t.Errorf("subject: got %v", ev.Details["subject"])
		}
		if ev.Actor != "alice" {
			t.Errorf("actor: got %q", ev.Actor)
		}
	}
	if !seen {
		t.Fatalf("no EventTokenCertCNMismatch entry")
	}
}

// ── Bound token + cert-less request → 401 + audit ────

func TestAuthMiddleware_RequiredCN_NoCert_Returns401(t *testing.T) {
	srv, tm, aStore, _ := bindingFixture(t)
	tok, err := tm.GenerateTokenWithCN(
		context.Background(), "alice", "admin", "alice@ops", time.Minute)
	if err != nil {
		t.Fatalf("GenerateTokenWithCN: %v", err)
	}

	req := buildRequestWithCert(t, "GET", "/jobs", tok, nil)
	rr := newTestResponseRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("cert-less: want 401, got %d: %s", rr.Code, rr.Body.String())
	}

	// Audit event: observed_cn is empty string.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventTokenCertCNMismatch {
			continue
		}
		seen = true
		if ev.Details["observed_cn"] != "" {
			t.Errorf("cert-less observed_cn should be empty, got %v", ev.Details["observed_cn"])
		}
	}
	if !seen {
		t.Fatal("no EventTokenCertCNMismatch entry for cert-less request")
	}
}

// ── Unbound token + mismatched cert → allowed ────────

func TestAuthMiddleware_RequiredCNEmpty_DoesNotEnforceBinding(t *testing.T) {
	srv, tm, aStore, ca := bindingFixture(t)
	// Legacy GenerateToken omits the claim.
	tok, err := tm.GenerateToken(context.Background(), "alice", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	// Cert CN is Bob — doesn't match subject, but without
	// a binding the middleware must let this through.
	leaf := mintOperatorLeaf(t, ca, "bob@ops")

	req := buildRequestWithCert(t, "GET", "/jobs", tok, leaf)
	rr := newTestResponseRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("unbound token: middleware should not 401, got 401: %s", rr.Body.String())
	}

	// No mismatch event should have fired.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventTokenCertCNMismatch {
			t.Fatalf("unbound token produced a mismatch audit event")
		}
	}
}

// ── Issue-token handler round-trip ──────────────────

func TestIssueToken_WithBinding_StampsClaim(t *testing.T) {
	srv, tm, _, _ := bindingFixture(t)
	adminTok, err := tm.GenerateToken(context.Background(), "root", "admin", time.Minute)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}

	body := `{"subject":"alice","role":"admin","ttl_hours":1,"bind_to_cert_cn":"alice@ops"}`
	rr := doWithToken(srv, "POST", "/admin/tokens", body, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.IssueTokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.BoundToCertCN != "alice@ops" {
		t.Errorf("response BoundToCertCN: got %q", resp.BoundToCertCN)
	}

	// Decode the minted token via the server's own
	// TokenManager so we're not reimplementing signature
	// checks in the test.
	claims, err := tm.ValidateToken(context.Background(), resp.Token)
	if err != nil {
		t.Fatalf("ValidateToken minted: %v", err)
	}
	if claims.RequiredCN != "alice@ops" {
		t.Errorf("minted token required_cn: got %q", claims.RequiredCN)
	}
}

func TestIssueToken_WithoutBinding_OmitsClaim(t *testing.T) {
	srv, tm, _, _ := bindingFixture(t)
	adminTok, err := tm.GenerateToken(context.Background(), "root", "admin", time.Minute)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}

	body := `{"subject":"alice","role":"admin","ttl_hours":1}`
	rr := doWithToken(srv, "POST", "/admin/tokens", body, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.IssueTokenResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.BoundToCertCN != "" {
		t.Errorf("unbound response leaked BoundToCertCN: %q", resp.BoundToCertCN)
	}
	claims, err := tm.ValidateToken(context.Background(), resp.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.RequiredCN != "" {
		t.Errorf("unbound token leaked required_cn: %q", claims.RequiredCN)
	}
}

func TestIssueToken_TrimsWhitespaceFromBinding(t *testing.T) {
	srv, tm, _, _ := bindingFixture(t)
	adminTok, _ := tm.GenerateToken(context.Background(), "root", "admin", time.Minute)

	body := `{"subject":"alice","role":"admin","ttl_hours":1,"bind_to_cert_cn":"  alice@ops  "}`
	rr := doWithToken(srv, "POST", "/admin/tokens", body, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.IssueTokenResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.BoundToCertCN != "alice@ops" {
		t.Errorf("whitespace not trimmed: %q", resp.BoundToCertCN)
	}
}
