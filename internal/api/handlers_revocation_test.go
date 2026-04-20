// internal/api/handlers_revocation_test.go
//
// Feature 31 — admin + middleware integration tests. Focus on
// behaviour at the HTTP + TLS boundary; the pure pqcrypto
// revocation + CRL tests live in internal/pqcrypto.

package api_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/pqcrypto"
)

// ── fakeRevocationStore ──────────────────────────────────

// fakeRevocationStore is a minimal map-backed stand-in for
// *pqcrypto.BadgerRevocationStore. Keeps the handler tests
// hermetic — pqcrypto has its own tests for the real store.
type fakeRevocationStore struct {
	mu   sync.Mutex
	data map[string]pqcrypto.RevocationRecord
}

func newFakeRevocationStore() *fakeRevocationStore {
	return &fakeRevocationStore{data: map[string]pqcrypto.RevocationRecord{}}
}

func (f *fakeRevocationStore) Revoke(_ context.Context, rec pqcrypto.RevocationRecord) (*pqcrypto.RevocationRecord, bool, error) {
	norm, err := pqcrypto.NormalizeSerialHex(rec.SerialHex)
	if err != nil {
		return nil, false, err
	}
	rec.SerialHex = norm
	if rec.RevokedAt.IsZero() {
		rec.RevokedAt = time.Now().UTC()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.data[norm]; ok {
		copy := existing
		return &copy, false, nil
	}
	f.data[norm] = rec
	copy := rec
	return &copy, true, nil
}

func (f *fakeRevocationStore) IsRevoked(serialHex string) bool {
	norm, err := pqcrypto.NormalizeSerialHex(serialHex)
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[norm]
	return ok
}

func (f *fakeRevocationStore) Get(_ context.Context, serialHex string) (*pqcrypto.RevocationRecord, error) {
	norm, err := pqcrypto.NormalizeSerialHex(serialHex)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.data[norm]
	if !ok {
		return nil, pqcrypto.ErrRevocationNotFound
	}
	copy := rec
	return &copy, nil
}

func (f *fakeRevocationStore) List(_ context.Context) ([]pqcrypto.RevocationRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]pqcrypto.RevocationRecord, 0, len(f.data))
	for _, rec := range f.data {
		out = append(out, rec)
	}
	return out, nil
}

// ── revocationFixture ────────────────────────────────────

// revocationFixture stands up an auth-enabled server wired
// with a real pqcrypto.CA (so CRL-sign tests have a valid
// signing context), the fake revocation store, and an audit
// store.
func revocationFixture(t *testing.T) (srv *api.Server, ca *pqcrypto.CA, store *fakeRevocationStore, aStore *inMemoryAuditStore, adminTok, userTok string) {
	t.Helper()
	tokStore := newTokenStore()
	tmgr, err := auth.NewTokenManager(context.Background(), tokStore)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	adminTok, err = tmgr.GenerateToken(context.Background(), "root", "admin", time.Minute)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	userTok, err = tmgr.GenerateToken(context.Background(), "alice", "user", time.Minute)
	if err != nil {
		t.Fatalf("user token: %v", err)
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
	// CRL signer MUST be set before SetRevocationStore so the
	// CRL route registers. See the SetRevocationStore comment.
	srv.SetCRLSigner(ca)
	store = newFakeRevocationStore()
	srv.SetRevocationStore(store)
	return srv, ca, store, aStore, adminTok, userTok
}

// ── POST revoke ──────────────────────────────────────────

func TestRevoke_HappyPath_201_ThenIdempotent200(t *testing.T) {
	srv, _, store, _, adminTok, _ := revocationFixture(t)

	rr := doWithToken(srv, "POST",
		"/admin/operator-certs/abcd1234/revoke",
		`{"reason":"alice left","common_name":"alice@ops"}`, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first revoke: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.RevokeOperatorCertResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SerialHex != "abcd1234" {
		t.Errorf("serial normalised?: got %q", resp.SerialHex)
	}
	if resp.Idempotent {
		t.Errorf("first call: idempotent should be false")
	}
	if !store.IsRevoked("abcd1234") {
		t.Errorf("store: IsRevoked returns false after revoke")
	}

	// Repeat with a different reason — must return the
	// original record with idempotent=true.
	rr = doWithToken(srv, "POST",
		"/admin/operator-certs/ABCD1234/revoke",
		`{"reason":"different reason"}`, adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("second revoke: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp2 api.RevokeOperatorCertResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp2)
	if !resp2.Idempotent {
		t.Errorf("second call: idempotent should be true")
	}
	if resp2.Reason != "alice left" {
		t.Errorf("idempotent revoke changed the reason: %q", resp2.Reason)
	}
}

func TestRevoke_NonAdmin_403(t *testing.T) {
	srv, _, _, _, _, userTok := revocationFixture(t)
	rr := doWithToken(srv, "POST",
		"/admin/operator-certs/abcd/revoke",
		`{"reason":"nope"}`, userTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestRevoke_ReasonRequired_400(t *testing.T) {
	srv, _, _, _, adminTok, _ := revocationFixture(t)
	rr := doWithToken(srv, "POST",
		"/admin/operator-certs/abcd/revoke",
		`{"reason":""}`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestRevoke_InvalidSerial_400(t *testing.T) {
	srv, _, _, _, adminTok, _ := revocationFixture(t)
	rr := doWithToken(srv, "POST",
		"/admin/operator-certs/not-hex/revoke",
		`{"reason":"x"}`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevoke_EmitsAuditEvent(t *testing.T) {
	srv, _, _, aStore, adminTok, _ := revocationFixture(t)

	rr := doWithToken(srv, "POST",
		"/admin/operator-certs/deadbeef/revoke",
		`{"reason":"laptop stolen"}`, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("revoke: %d", rr.Code)
	}

	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventOperatorCertRevoked {
			continue
		}
		seen = true
		if ev.Principal != "user:root" {
			t.Errorf("principal: got %q", ev.Principal)
		}
		if ev.Details["serial_hex"] != "deadbeef" {
			t.Errorf("serial_hex: got %v", ev.Details["serial_hex"])
		}
		if ev.Details["reason"] != "laptop stolen" {
			t.Errorf("reason: got %v", ev.Details["reason"])
		}
		if v, _ := ev.Details["idempotent"].(bool); v {
			t.Errorf("first revoke: idempotent should be false")
		}
	}
	if !seen {
		t.Fatal("no EventOperatorCertRevoked entry")
	}
}

// ── List revocations ─────────────────────────────────────

func TestListRevocations_AdminOnly(t *testing.T) {
	srv, _, _, _, adminTok, userTok := revocationFixture(t)
	_ = doWithToken(srv, "POST",
		"/admin/operator-certs/abcd/revoke",
		`{"reason":"x"}`, adminTok)

	rr := doWithToken(srv, "GET", "/admin/operator-certs/revocations", "", userTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("user: want 403, got %d", rr.Code)
	}

	rr = doWithToken(srv, "GET", "/admin/operator-certs/revocations", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.RevocationListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Revocations[0].SerialHex != "abcd" {
		t.Errorf("list response: %+v", resp)
	}
}

// ── GET CRL ──────────────────────────────────────────────

func TestCRLExport_ReturnsSignedPEM(t *testing.T) {
	srv, ca, _, _, adminTok, _ := revocationFixture(t)
	_ = doWithToken(srv, "POST",
		"/admin/operator-certs/feed1234/revoke",
		`{"reason":"x"}`, adminTok)

	rr := doWithToken(srv, "GET", "/admin/ca/crl", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/x-pem-file" {
		t.Errorf("Content-Type: %q", ct)
	}
	block, _ := pem.Decode(rr.Body.Bytes())
	if block == nil || block.Type != "X509 CRL" {
		t.Fatalf("bad PEM: %v", block)
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatalf("ParseRevocationList: %v", err)
	}
	if err := crl.CheckSignatureFrom(ca.Cert); err != nil {
		t.Fatalf("CRL signature does not verify against CA: %v", err)
	}
	// The revoked serial must appear.
	found := false
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber.Text(16) == "feed1234" {
			found = true
			break
		}
	}
	for _, entry := range crl.RevokedCertificates { //nolint:staticcheck
		if entry.SerialNumber.Text(16) == "feed1234" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("revoked serial missing from CRL")
	}
}

func TestCRLExport_NonAdmin_403(t *testing.T) {
	srv, _, _, _, _, userTok := revocationFixture(t)
	rr := doWithToken(srv, "GET", "/admin/ca/crl", "", userTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

// ── clientCertMiddleware rejection ─────────────────────

// TestClientCertMiddleware_RevokedCert_Rejected_On crafts
// a request that LOOKS like it came through TLS with a
// verified peer cert and asserts the middleware rejects the
// matching revoked serial. Avoids standing up a full TLS
// listener — the middleware branches only on r.TLS
// presence, not on an actual handshake.
func TestClientCertMiddleware_RevokedCert_Rejected_On(t *testing.T) {
	srv, ca, store, aStore, _, _ := revocationFixture(t)
	srv.SetClientCertTier(api.ClientCertOn)

	// Mint an operator cert so we have a real serial the
	// middleware can extract.
	certPEM, _, err := ca.IssueOperatorCert("alice@ops", time.Hour)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	serial := pqcrypto.SerialHexFromBigInt(leaf.SerialNumber)

	// Revoke the cert.
	_, _, err = store.Revoke(context.Background(), pqcrypto.RevocationRecord{
		SerialHex:  serial,
		CommonName: "alice@ops",
		RevokedBy:  "user:root",
		Reason:     "test",
	})
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Build a request with r.TLS populated so the middleware's
	// direct-TLS path fires.
	req := newRequestWithClientCert(t, "GET", "/jobs/nope", leaf)
	rr := executeHandler(srv, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for revoked cert, got %d: %s", rr.Code, rr.Body.String())
	}

	// Audit event must fire.
	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventOperatorCertRevokedUsed {
			continue
		}
		seen = true
		if ev.Details["serial_hex"] != serial {
			t.Errorf("serial_hex: got %v, want %q", ev.Details["serial_hex"], serial)
		}
		if ev.Details["enforced"] != true {
			t.Errorf("on-tier should mark enforced=true, got %v", ev.Details["enforced"])
		}
	}
	if !seen {
		t.Fatalf("no EventOperatorCertRevokedUsed entry")
	}
}

// TestClientCertMiddleware_ValidCert_PassesThrough is the
// happy-path control — a cert that wasn't revoked goes all
// the way through.
func TestClientCertMiddleware_ValidCert_PassesThrough(t *testing.T) {
	srv, ca, _, _, _, _ := revocationFixture(t)
	srv.SetClientCertTier(api.ClientCertOn)

	certPEM, _, err := ca.IssueOperatorCert("alice@ops", time.Hour)
	if err != nil {
		t.Fatalf("IssueOperatorCert: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	req := newRequestWithClientCert(t, "GET", "/jobs/nope", leaf)
	rr := executeHandler(srv, req)
	// Any non-401 status means the middleware didn't reject
	// the cert. The handler below produces 401/404/etc. for
	// the missing bearer — any of them are fine; the test
	// just asserts the middleware didn't slam the door.
	if rr.Code == http.StatusUnauthorized &&
		strings.Contains(rr.Body.String(), "client certificate is revoked") {
		t.Fatalf("valid cert rejected as revoked: %s", rr.Body.String())
	}
}

// newRequestWithClientCert builds an *http.Request with a
// non-nil r.TLS carrying the given cert as the peer leaf.
// The middleware's extractVerifiedPeer reads PeerCertificates[0]
// and its SerialNumber — we satisfy both.
func newRequestWithClientCert(t *testing.T, method, path string, cert *x509.Certificate) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
	}
	req.RemoteAddr = "192.0.2.10:54321"
	return req
}

// executeHandler runs the request through the server's full
// handler chain (including clientCertMiddleware). Mirrors
// what `do` / `doWithToken` do but for a pre-built request.
func executeHandler(srv *api.Server, req *http.Request) *testResponseRecorder {
	rr := newTestResponseRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// testResponseRecorder wraps httptest.NewRecorder but gives
// us the same .Code / .Body.String() surface the other tests
// use so the assertions look identical.
type testResponseRecorder struct {
	Code int
	Body *strings.Builder
	hdr  http.Header
}

func newTestResponseRecorder() *testResponseRecorder {
	return &testResponseRecorder{
		Code: http.StatusOK,
		Body: &strings.Builder{},
		hdr:  http.Header{},
	}
}

func (r *testResponseRecorder) Header() http.Header { return r.hdr }

func (r *testResponseRecorder) Write(b []byte) (int, error) {
	return r.Body.Write(b)
}

func (r *testResponseRecorder) WriteHeader(code int) { r.Code = code }

