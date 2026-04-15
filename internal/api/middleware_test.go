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

// TestWsEndpoint_WithoutUpgrade_RejectsPlainHTTP verifies that a plain HTTP
// GET to a WS endpoint (without WebSocket upgrade headers) does not return
// 200. Auth is now handled post-upgrade via first-message pattern (AUDIT H2).
func TestWsEndpoint_WithoutUpgrade_RejectsPlainHTTP(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	// Without WebSocket upgrade headers the upgrader rejects the request.
	if rr.Code == http.StatusOK {
		t.Errorf("ws endpoint should not return 200 for plain HTTP")
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

// AUDIT 2026-04-12-01/H2: wsAuthMiddleware removed — WebSocket auth is now
// handled via first-message pattern inside the WS handlers (handlers_ws.go).
// WS endpoints no longer have pre-upgrade HTTP-level auth.
