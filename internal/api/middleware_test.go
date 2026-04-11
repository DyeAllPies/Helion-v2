// internal/api/middleware_test.go
//
// Tests for authMiddleware, wsAuthMiddleware, and adminMiddleware.

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── authMiddleware ────────────────────────────────────────────────────────────

// TestAuthMiddleware_DisableAuth_PassesThrough verifies the explicit
// opt-in no-auth path used by dev tooling and tests. newServer() calls
// DisableAuth() internally, so this request should succeed.
func TestAuthMiddleware_DisableAuth_PassesThrough(t *testing.T) {
	js := newMockJobStore()
	js.jobs["job-x"] = &cpb.Job{ID: "job-x", Command: "ls"}
	srv := newServer(js, nil, nil) // tokenManager = nil, DisableAuth() called

	rr := do(srv, "GET", "/jobs/job-x", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200 with DisableAuth, got %d", rr.Code)
	}
}

// TestAuthMiddleware_NilTokenManager_WithoutOptIn_Returns500 is the AUDIT H2
// regression guard. A Server constructed with nil tokenManager and WITHOUT
// DisableAuth() must now refuse to serve rather than silently pass every
// request through.
func TestAuthMiddleware_NilTokenManager_WithoutOptIn_Returns500(t *testing.T) {
	// Construct directly via api.NewServer — DO NOT use the test helper
	// because newServer calls DisableAuth() on the returned server.
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500 without DisableAuth opt-in, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestWsAuthMiddleware_NilTokenManager_WithoutOptIn_Returns500 — same
// regression guard for the WebSocket auth path.
func TestWsAuthMiddleware_NilTokenManager_WithoutOptIn_Returns500(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("ws middleware: want 500 without DisableAuth, got %d", rr.Code)
	}
}

func TestAuthMiddleware_MissingBearer_Returns401(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	// No Authorization header.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 without bearer, got %d", rr.Code)
	}
}

func TestAuthMiddleware_InvalidToken_Returns401(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid token, got %d", rr.Code)
	}
}

func TestAuthMiddleware_ValidToken_Passes(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(context.Background(), store)
	js := newMockJobStore()
	js.jobs["j1"] = &cpb.Job{ID: "j1", Command: "ls"}
	srv := api.NewServer(js, nil, nil, nil, tm, nil, nil, nil)

	tok, err := tm.GenerateToken(context.Background(), "user-1", "admin", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	req := httptest.NewRequest("GET", "/jobs/j1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200 with valid token, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── authMiddleware with audit log ─────────────────────────────────────────────

func TestAuthMiddleware_MissingBearer_WithAuditLog_LogsFailure(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	// No Authorization header — triggers audit log for missing bearer.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 without auth header, got %d", rr.Code)
	}
}

func TestAuthMiddleware_InvalidToken_WithAuditLog_LogsFailure(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid token, got %d", rr.Code)
	}
}

// ── wsAuthMiddleware ──────────────────────────────────────────────────────────

func TestWsAuthMiddleware_NilTokenManager_PassesThrough(t *testing.T) {
	// Calling GET /ws/jobs/{id}/logs without token manager — should NOT return 401.
	srv := newServer(newMockJobStore(), nil, nil)
	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	// Should not be 401 (wsAuthMiddleware passed through).
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("expected pass-through with nil token manager, got 401")
	}
}

func TestWsAuthMiddleware_MissingToken_Returns401(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs", nil)
	// No token in query param and no Authorization header.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for missing token, got %d", rr.Code)
	}
}

func TestWsAuthMiddleware_InvalidToken_Returns401(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs?token=not.a.valid.jwt", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid token, got %d", rr.Code)
	}
}

func TestWsAuthMiddleware_ValidTokenInQueryParam_PassesThrough(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken(context.Background(), "user", "admin", time.Minute)
	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs?token="+tok, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	// Token is valid — wsAuthMiddleware passes, but the WebSocket upgrade fails
	// (non-WS test request). Should NOT be 401.
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("want pass-through with valid token, got 401")
	}
}

func TestWsAuthMiddleware_ValidTokenInHeader_PassesThrough(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken(context.Background(), "user", "admin", time.Minute)
	req := httptest.NewRequest("GET", "/ws/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("want pass-through with valid token in header, got 401")
	}
}

// ── wsAuthMiddleware with audit log ──────────────────────────────────────────

func TestWsAuthMiddleware_MissingToken_WithAuditLog_LogsFailure(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestWsAuthMiddleware_InvalidToken_WithAuditLog_LogsFailure(t *testing.T) {
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs?token=invalid.jwt.token", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid ws token, got %d", rr.Code)
	}
}
