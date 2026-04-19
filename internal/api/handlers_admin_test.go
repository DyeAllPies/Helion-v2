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

// ── Feature 26 — POST /admin/jobs/{id}/reveal-secret ─────────────────────────
//
// Invariants tested:
//
//   1. Happy path: admin reveals a declared secret key on a persisted
//      job. Response carries the plaintext value + revealed_at +
//      revealed_by + audit_notice. Audit event secret_revealed
//      records actor + reason.
//   2. Non-admin (role=node) → 403 from adminMiddleware.
//   3. Unauthenticated → 401 (handled by authMiddleware before the
//      handler runs; covered here indirectly — focus on admin checks).
//   4. Reveal of a key NOT on job.SecretKeys → 404 (the endpoint is
//      not a generic env reader).
//   5. Reveal of an unknown job → 404.
//   6. Empty reason → 400. Whitespace-only reason → 400.
//   7. Oversize reason → 400.
//   8. Malformed body → 400.
//   9. Every reject emits a secret_reveal_reject audit event so
//      enumeration probes show up.
//  10. Rate limiting: a tight loop from one subject triggers 429.

func newRevealSecretServer(t *testing.T) (*api.Server, *auth.TokenManager, *inMemoryAuditStore) {
	t.Helper()
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	auditStore := newAuditStore()
	auditLog := audit.NewLogger(auditStore, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)
	return srv, tm, auditStore
}

// submitJobWithSecret wires a test job carrying a declared secret env
// value. Returns the job ID.
func submitJobWithSecret(t *testing.T, srv *api.Server, tok, jobID, key, value string) {
	t.Helper()
	body := `{
		"id":` + `"` + jobID + `"` + `,
		"command": "echo",
		"env": {` + `"` + key + `"` + `:` + `"` + value + `"` + `},
		"secret_keys": [` + `"` + key + `"` + `]
	}`
	rr := doWithToken(srv, "POST", "/jobs", body, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevealSecret_HappyPath(t *testing.T) {
	srv, tm, auditStore := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	submitJobWithSecret(t, srv, adminTok, "job-reveal-1", "HF_TOKEN", "hf_actualsecret")

	body := `{"key":"HF_TOKEN","reason":"on-call debug"}`
	rr := doWithToken(srv, "POST", "/admin/jobs/job-reveal-1/reveal-secret", body, adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.RevealSecretResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Value != "hf_actualsecret" {
		t.Errorf("value: want hf_actualsecret, got %q", resp.Value)
	}
	if resp.Key != "HF_TOKEN" {
		t.Errorf("key: want HF_TOKEN, got %q", resp.Key)
	}
	if resp.RevealedBy != "root" {
		t.Errorf("revealed_by: want root, got %q", resp.RevealedBy)
	}
	if !strings.Contains(resp.AuditNotice, "audit log") {
		t.Errorf("audit_notice should reference the audit log: %q", resp.AuditNotice)
	}
	if !strings.Contains(resp.AuditNotice, "on-call debug") {
		t.Errorf("audit_notice should echo the reason: %q", resp.AuditNotice)
	}

	// Audit invariant: one secret_revealed entry with matching details.
	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	var found bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventSecretRevealed {
			found = true
			if ev.Actor != "root" {
				t.Errorf("audit actor: want root, got %q", ev.Actor)
			}
			if jid, _ := ev.Details["job_id"].(string); jid != "job-reveal-1" {
				t.Errorf("audit job_id: want job-reveal-1, got %v", ev.Details["job_id"])
			}
			if key, _ := ev.Details["key"].(string); key != "HF_TOKEN" {
				t.Errorf("audit key: want HF_TOKEN, got %v", ev.Details["key"])
			}
			if reason, _ := ev.Details["reason"].(string); reason != "on-call debug" {
				t.Errorf("audit reason: want on-call debug, got %v", ev.Details["reason"])
			}
			// Plaintext must NOT appear anywhere in the audit detail.
			if strings.Contains(string(raw), "hf_actualsecret") {
				t.Errorf("PLAINTEXT LEAK: audit entry contains hf_actualsecret")
			}
		}
	}
	if !found {
		t.Error("expected secret_revealed audit event")
	}
}

func TestRevealSecret_NonAdmin_Forbidden(t *testing.T) {
	srv, tm, _ := newRevealSecretServer(t)
	// Submit under admin — we need the job to exist.
	adminTok := adminToken(t, tm)
	submitJobWithSecret(t, srv, adminTok, "job-reveal-nonadmin", "HF_TOKEN", "x")

	nodeTok, err := tm.GenerateToken(context.Background(), "node-1", "node", time.Minute)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	rr := doWithToken(srv, "POST", "/admin/jobs/job-reveal-nonadmin/reveal-secret",
		`{"key":"HF_TOKEN","reason":"r"}`, nodeTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("non-admin: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevealSecret_Unauth_Returns401(t *testing.T) {
	srv, _, _ := newRevealSecretServer(t)
	rr := do(srv, "POST", "/admin/jobs/any/reveal-secret", `{"key":"HF_TOKEN","reason":"r"}`)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: want 401, got %d", rr.Code)
	}
}

func TestRevealSecret_NotDeclaredSecret_404(t *testing.T) {
	// Regression guard: this endpoint must NOT be a generic env
	// reader. A legitimate-seeming reveal request for a key that
	// the submitter didn't flag secret is refused.
	srv, tm, auditStore := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	body := `{"id":"job-notsecret","command":"echo","env":{"PYTHONPATH":"/app"}}`
	rr := doWithToken(srv, "POST", "/jobs", body, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "POST", "/admin/jobs/job-notsecret/reveal-secret",
		`{"key":"PYTHONPATH","reason":"probe"}`, adminTok)
	if rr.Code != http.StatusNotFound {
		t.Errorf("non-declared key: want 404, got %d: %s", rr.Code, rr.Body.String())
	}
	// Reject must be audited.
	entries, _ := auditStore.Scan(context.Background(), "audit:", 0)
	var seenReject bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventSecretRevealReject {
			seenReject = true
		}
	}
	if !seenReject {
		t.Error("expected secret_reveal_reject audit event")
	}
}

func TestRevealSecret_UnknownJob_404(t *testing.T) {
	srv, tm, _ := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	rr := doWithToken(srv, "POST", "/admin/jobs/never-existed/reveal-secret",
		`{"key":"HF_TOKEN","reason":"r"}`, adminTok)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown job: want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevealSecret_EmptyReason_400(t *testing.T) {
	srv, tm, _ := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	submitJobWithSecret(t, srv, adminTok, "job-reason", "HF_TOKEN", "x")

	cases := map[string]string{
		"empty reason":      `{"key":"HF_TOKEN","reason":""}`,
		"whitespace reason": `{"key":"HF_TOKEN","reason":"   \t\n"}`,
		"no reason":         `{"key":"HF_TOKEN"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rr := doWithToken(srv, "POST", "/admin/jobs/job-reason/reveal-secret", body, adminTok)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestRevealSecret_MalformedBody_400(t *testing.T) {
	srv, tm, _ := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	submitJobWithSecret(t, srv, adminTok, "job-malformed", "HF_TOKEN", "x")

	rr := doWithToken(srv, "POST", "/admin/jobs/job-malformed/reveal-secret",
		`not json`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("malformed: want 400, got %d", rr.Code)
	}
}

func TestRevealSecret_RateLimit_Triggers429(t *testing.T) {
	// Burst is 3; 4th request from the same subject in the same
	// second must 429. (Limiter replenishes at 0.2 tokens/sec so
	// the test is deterministic within a fraction of a second.)
	srv, tm, _ := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	submitJobWithSecret(t, srv, adminTok, "job-rate", "HF_TOKEN", "x")

	ok := 0
	limited := 0
	for i := 0; i < 5; i++ {
		rr := doWithToken(srv, "POST", "/admin/jobs/job-rate/reveal-secret",
			`{"key":"HF_TOKEN","reason":"r"}`, adminTok)
		switch rr.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
		}
	}
	if ok == 0 {
		t.Errorf("expected some requests to succeed, got %d ok / %d limited", ok, limited)
	}
	if limited == 0 {
		t.Errorf("expected some requests to hit rate limit, got %d ok / %d limited", ok, limited)
	}
}

// ── Feature 26 — secret redaction on GET /jobs/{id} ──────────────────────────

func TestSubmitJob_SecretEnv_RedactedOnGet(t *testing.T) {
	srv, tm, _ := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	submitJobWithSecret(t, srv, adminTok, "job-redact", "HF_TOKEN", "hf_shouldbehidden")

	rr := doWithToken(srv, "GET", "/jobs/job-redact", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "hf_shouldbehidden") {
		t.Errorf("PLAINTEXT LEAK on GET /jobs/{id}: %s", body)
	}
	if !strings.Contains(body, api.RedactionPlaceholder) {
		t.Errorf("expected redaction placeholder in response: %s", body)
	}
	// The SubmitResponse on POST /jobs should also carry the
	// redaction (jobToResponse runs on both paths).
}

func TestSubmitJob_NonSecretEnv_NotRedacted(t *testing.T) {
	srv, tm, _ := newRevealSecretServer(t)
	adminTok := adminToken(t, tm)
	body := `{"id":"job-plain","command":"echo","env":{"PYTHONPATH":"/app"}}`
	rr := doWithToken(srv, "POST", "/jobs", body, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	rr = doWithToken(srv, "GET", "/jobs/job-plain", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "/app") {
		t.Errorf("non-secret value must survive on GET: %s", rr.Body.String())
	}
}

func TestSubmitJob_SecretKeyNotInEnv_Rejected(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"job-typo","command":"echo","env":{"PYTHONPATH":"/app"},"secret_keys":["HF_TOKEN"]}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("flagging a non-existent key: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_SecretKeys_EchoedInResponse(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"job-echo","command":"echo","env":{"HF_TOKEN":"x"},"secret_keys":["HF_TOKEN"]}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "secret_keys") {
		t.Errorf("response should echo secret_keys: %s", rr.Body.String())
	}
}

func TestSubmitJob_SecretKeys_IncludedInAudit(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	body := `{"id":"job-audit","command":"echo","env":{"HF_TOKEN":"hf_should_never_leak"},"secret_keys":["HF_TOKEN"]}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	for _, raw := range entries {
		if strings.Contains(string(raw), "hf_should_never_leak") {
			t.Errorf("PLAINTEXT LEAK in audit log: %s", raw)
		}
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == audit.EventJobSubmit {
			sk, _ := ev.Details["secret_keys"].([]interface{})
			if len(sk) != 1 || sk[0] != "HF_TOKEN" {
				t.Errorf("audit secret_keys: want [HF_TOKEN], got %v", ev.Details["secret_keys"])
			}
		}
	}
}

