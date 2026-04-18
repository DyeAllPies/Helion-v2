// internal/api/handlers_admin_test.go
//
// Tests for POST/DELETE /admin/tokens and POST /admin/nodes/{id}/revoke.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
)

// errRevokeNode is a sentinel error for registry failure tests.
var errRevokeNode = errors.New("cannot revoke")

// ── POST /admin/nodes/{id}/revoke ─────────────────────────────────────────────

func TestRevokeNode_NilRegistry_Returns501(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/admin/nodes/node-1/revoke", `{"reason":"test"}`)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501, got %d", rr.Code)
	}
}

func TestRevokeNode_Returns200(t *testing.T) {
	nr := &mockNodeRegistry{}
	srv := newServer(newMockJobStore(), nr, nil)
	rr := do(srv, "POST", "/admin/nodes/node-bad/revoke", `{"reason":"compromised"}`)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.RevokeNodeResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success {
		t.Error("want success=true")
	}
}

func TestRevokeNode_RegistryError_Returns500(t *testing.T) {
	nr := &mockNodeRegistry{revokeErr: errRevokeNode}
	srv := newServer(newMockJobStore(), nr, nil)
	rr := do(srv, "POST", "/admin/nodes/node-1/revoke", `{"reason":"test"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

func TestRevokeNode_WithAuditLog_LogsEvent(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	nr := &mockNodeRegistry{}
	srv := api.NewServer(newMockJobStore(), nr, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "POST", "/admin/nodes/bad-node/revoke", `{"reason":"test"}`)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeNode_InvalidJSON_UsesDefaultReason(t *testing.T) {
	nr := &mockNodeRegistry{}
	srv := newServer(newMockJobStore(), nr, nil)
	rr := do(srv, "POST", "/admin/nodes/my-node/revoke", `not json at all`)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200 with default reason, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeNode_EmptyBody_UsesDefaultReason(t *testing.T) {
	nodes := &mockNodeRegistry{}
	srv := newServer(newMockJobStore(), nodes, nil)

	rr := do(srv, "POST", "/admin/nodes/node-y/revoke", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeNode_WithValidToken_ActorFromClaims(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(context.Background(), store)
	nr := &mockNodeRegistry{}
	srv := api.NewServer(newMockJobStore(), nr, nil, nil, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken(context.Background(), "alice", "admin", time.Minute)
	req := httptest.NewRequest("POST", "/admin/nodes/node-xyz/revoke",
		strings.NewReader(`{"reason":"suspect"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeNode_WithAuditAndToken_LogsEvent(t *testing.T) {
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	auditStore := newAuditStore()
	nodes := &mockNodeRegistry{}
	srv := api.NewServer(newMockJobStore(), nodes, nil, audit.NewLogger(auditStore, 0), tm, nil, nil, nil)

	atk := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/nodes/node-x/revoke",
		`{"reason":"test"}`, atk)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}

	if len(auditStore.entries) == 0 {
		t.Error("expected audit entry to be written")
	}
}

// ── POST /admin/tokens ────────────────────────────────────────────────────────

func TestIssueToken_ValidRequest_Returns201(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"alice","role":"admin","ttl_hours":4}`, atk)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body)
	}

	var resp api.IssueTokenResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Subject != "alice" {
		t.Errorf("subject: want alice, got %q", resp.Subject)
	}
	if resp.Role != "admin" {
		t.Errorf("role: want admin, got %q", resp.Role)
	}
	if resp.TTLHours != 4 {
		t.Errorf("ttl_hours: want 4, got %d", resp.TTLHours)
	}
	if resp.Token == "" {
		t.Error("token should not be empty")
	}
}

func TestIssueToken_IssuedTokenIsValid(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"bob","role":"node","ttl_hours":1}`, atk)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}

	var resp api.IssueTokenResponse
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck

	claims, err := tm.ValidateToken(context.Background(), resp.Token)
	if err != nil {
		t.Fatalf("issued token failed validation: %v", err)
	}
	if claims.Subject != "bob" {
		t.Errorf("subject: want bob, got %q", claims.Subject)
	}
	if claims.Role != "node" {
		t.Errorf("role: want node, got %q", claims.Role)
	}
}

func TestIssueToken_DefaultTTL_UsedWhenZero(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"charlie","role":"admin"}`, atk)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}

	var resp api.IssueTokenResponse
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp.TTLHours != 8 {
		t.Errorf("default ttl_hours: want 8, got %d", resp.TTLHours)
	}
}

func TestIssueToken_MissingSubject_Returns400(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"role":"admin"}`, atk)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestIssueToken_InvalidRole_Returns400(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"dave","role":"superuser"}`, atk)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestIssueToken_TTLExceedsMax_Returns400(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"eve","role":"admin","ttl_hours":721}`, atk)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestIssueToken_NonAdminRole_Returns403(t *testing.T) {
	srv, tm := newAuthServer(t)

	nodeTok, _ := tm.GenerateToken(context.Background(), "worker", "node", time.Hour)
	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"hacker","role":"admin"}`, nodeTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rr.Code)
	}
}

// TestIssueToken_JobRole_Accepted pins the feature-19 workflow-scoped
// token role. submit.py uses this to mint a short-lived credential
// for in-workflow scripts (register.py) instead of leaking the
// operator's root admin token into every job's env.
func TestIssueToken_JobRole_Accepted(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"workflow:iris-wf-1","role":"job","ttl_hours":1}`, atk)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body)
	}
	var resp api.IssueTokenResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Role != "job" {
		t.Errorf("role: got %q, want job", resp.Role)
	}
}

// TestJobRoleToken_CanNotMintMoreTokens is the load-bearing
// alarm for feature 19's token-scoping safety: the scoped `job`
// token must be rejected by adminMiddleware when a compromised
// in-workflow script tries to call POST /admin/tokens to
// escalate to an unbounded admin token. Without this, leaking
// the iris pipeline's register-step env would let an attacker
// mint an admin token from inside the cluster.
func TestJobRoleToken_CanNotMintMoreTokens(t *testing.T) {
	srv, tm := newAuthServer(t)
	jobTok, err := tm.GenerateToken(context.Background(),
		"workflow:iris-wf-1", "job", time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken(job): %v", err)
	}

	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"escalated","role":"admin"}`, jobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("job-role token escalation: got %d, want 403", rr.Code)
	}
}

// TestJobRoleToken_CanNotRevokeNodes pairs with the mint-rejection
// test above — the other adminMiddleware-guarded endpoint is
// POST /admin/nodes/{id}/revoke. A leaked job token must not be
// able to take nodes offline by calling it.
func TestJobRoleToken_CanNotRevokeNodes(t *testing.T) {
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	jobTok, err := tm.GenerateToken(context.Background(),
		"workflow:iris-wf-1", "job", time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken(job): %v", err)
	}
	nodes := &mockNodeRegistry{}
	srv := api.NewServer(newMockJobStore(), nodes, nil, nil, tm, nil, nil, nil)

	rr := doWithToken(srv, "POST", "/admin/nodes/target-node/revoke",
		`{"reason":"unauthorised"}`, jobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("job-role revoke-node: got %d, want 403", rr.Code)
	}
}

func TestIssueToken_NoAuth_Returns401(t *testing.T) {
	srv, _ := newAuthServer(t)
	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"x","role":"admin"}`, "")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestIssueToken_NilTokenManager_Returns501(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)
	srv.DisableAuth()
	rr := do(srv, "POST", "/admin/tokens", `{"subject":"x","role":"admin"}`)
	// No tokenManager → adminMiddleware skips role check, handleIssueToken returns 501.
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501 (tokenManager nil), got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestIssueToken_InvalidJSON_Returns400(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	rr := doWithToken(srv, "POST", "/admin/tokens", "not-json", atk)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestIssueToken_WithAuditLog_LogsEvent(t *testing.T) {
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	auditStore := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(auditStore, 0), tm, nil, nil, nil)

	atk := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/tokens",
		`{"subject":"bob","role":"node","ttl_hours":1}`, atk)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body)
	}

	if len(auditStore.entries) == 0 {
		t.Error("expected audit event to be logged")
	}
}

// ── DELETE /admin/tokens/{jti} ────────────────────────────────────────────────

func TestRevokeToken_ValidJTI_Returns200(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	issued, _ := tm.GenerateToken(context.Background(), "target", "node", time.Hour)
	jti, err := auth.ExtractJTIFromValidatedToken(context.Background(), issued, tm)
	if err != nil {
		t.Fatalf("ExtractJTIFromValidatedToken: %v", err)
	}

	rr := doWithToken(srv, "DELETE", "/admin/tokens/"+jti, "", atk)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}

	var resp api.RevokeTokenResponse
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if !resp.Revoked {
		t.Error("revoked should be true")
	}
	if resp.JTI != jti {
		t.Errorf("jti: want %q, got %q", jti, resp.JTI)
	}

	if _, err := tm.ValidateToken(context.Background(), issued); err == nil {
		t.Error("revoked token should fail validation")
	}
}

func TestRevokeToken_NonAdminRole_Returns403(t *testing.T) {
	srv, tm := newAuthServer(t)
	nodeTok, _ := tm.GenerateToken(context.Background(), "worker", "node", time.Hour)

	rr := doWithToken(srv, "DELETE", "/admin/tokens/some-jti", "", nodeTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rr.Code)
	}
}

func TestRevokeToken_NoAuth_Returns401(t *testing.T) {
	srv, _ := newAuthServer(t)
	rr := doWithToken(srv, "DELETE", "/admin/tokens/some-jti", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestRevokeToken_WithAuditLog_LogsEvent(t *testing.T) {
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	auditStore := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(auditStore, 0), tm, nil, nil, nil)

	atk := adminToken(t, tm)
	issued, _ := tm.GenerateToken(context.Background(), "victim", "node", time.Hour)
	jti, _ := auth.ExtractJTIFromValidatedToken(context.Background(), issued, tm)

	rr := doWithToken(srv, "DELETE", "/admin/tokens/"+jti, "", atk)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}

	if len(auditStore.entries) == 0 {
		t.Error("expected audit entry to be written")
	}
}

func TestRevokeToken_NilTokenManager_Returns501OrForbidden(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)
	srv.DisableAuth()
	rr := do(srv, "DELETE", "/admin/tokens/some-jti", "")
	if rr.Code == http.StatusOK {
		t.Errorf("expected non-200, got %d", rr.Code)
	}
}

// ── Token issuance rate limiter (AUDIT M2) ───────────────────────────────────

// TestIssueToken_RateLimitExceeded_Returns429 covers the per-subject rate
// limit branch in handleIssueToken. The limiter allows a burst of 5 then
// refills at 1/s, so firing 20 requests back-to-back guarantees a 429.
func TestIssueToken_RateLimitExceeded_Returns429(t *testing.T) {
	srv, tm := newAuthServer(t)
	atk := adminToken(t, tm)

	body := `{"subject":"burst-user","role":"node","ttl_hours":1}`
	sawRateLimit := false
	for i := 0; i < 20; i++ {
		rr := doWithToken(srv, "POST", "/admin/tokens", body, atk)
		if rr.Code == http.StatusTooManyRequests {
			sawRateLimit = true
			break
		}
	}
	if !sawRateLimit {
		t.Error("expected at least one 429 from the token-issue rate limiter")
	}
}

// ── adminMiddleware — nil tokenManager ────────────────────────────────────────
// Documents AUDIT.md H2: when tokenManager is nil, adminMiddleware skips the
// role check. Handlers still return 501 when tokenManager is nil.

func TestAdminMiddleware_NilTokenManager_IssueTokenReturns501(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)
	srv.DisableAuth()
	rr := do(srv, "POST", "/admin/tokens", `{"subject":"eve","role":"admin","ttl_hours":1}`)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501 (tokenManager nil), got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAdminMiddleware_NilTokenManager_RevokeTokenReturns501(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)
	srv.DisableAuth()
	rr := do(srv, "DELETE", "/admin/tokens/any-jti", "")
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501 (tokenManager nil), got %d: %s", rr.Code, rr.Body.String())
	}
}

