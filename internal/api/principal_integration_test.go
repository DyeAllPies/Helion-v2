// internal/api/principal_integration_test.go
//
// Feature 35 — integration tests for the principal plumbing across
// authMiddleware + clientCertMiddleware + audit event emission.
//
// Spec invariants guarded here:
//
//   - authMiddleware stamps a typed Principal into the context
//     alongside the legacy *auth.Claims value.
//   - A cert-CN Principal (operator) stamped by
//     clientCertMiddleware is NOT overwritten by the subsequent
//     JWT resolution in authMiddleware.
//   - Downstream audit.Log calls picks up the Principal from
//     context and populates Event.Principal + Event.PrincipalKind.
//   - Legacy Event.Actor stays bare-string shaped for
//     back-compat (handlers_jobs_test.go:747 et al.).

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
)

// ── authMiddleware populates Principal ─────────────────────────────────────

func TestAuthMiddleware_StampsUserPrincipal(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	tok, err := tm.GenerateToken(context.Background(), "alice", "admin", time.Minute)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	// POST /jobs goes through authMiddleware → handleSubmitJob.
	// The handler emits audit.Log(EventJobSubmit, ...) with
	// r.Context(), which by that point MUST carry a user
	// Principal stamped by authMiddleware.
	body := `{"id":"p-35-user","command":"echo"}`
	rr := doWithToken(srv, "POST", "/jobs", body, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var found bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventJobSubmit {
			continue
		}
		found = true
		if ev.Principal != "user:alice" {
			t.Errorf("Principal: want 'user:alice', got %q", ev.Principal)
		}
		if ev.PrincipalKind != string(principal.KindUser) {
			t.Errorf("PrincipalKind: want 'user', got %q", ev.PrincipalKind)
		}
		// Back-compat: Actor stays bare (handlers_jobs_test.go
		// regression guard).
		if ev.Actor != "alice" {
			t.Errorf("Actor: want 'alice' (bare), got %q", ev.Actor)
		}
	}
	if !found {
		t.Fatal("no job_submit audit event found")
	}
}

func TestAuthMiddleware_NodeRole_StampsNodePrincipal(t *testing.T) {
	// A JWT carrying role=node resolves to KindNode. Feature 37
	// refuses REST actions for node principals — a compromised
	// node's JWT cannot stand up fake jobs via the REST surface.
	// The typed Principal on the emitted audit event must still
	// be `node:gpu-01` (feature 35's Kind stamp is orthogonal to
	// the authz decision), and the event type is now
	// `authz_deny` rather than `job_submit`.
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	tok, err := tm.GenerateToken(context.Background(), "gpu-01", "node", time.Minute)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	body := `{"id":"p-35-node","command":"echo"}`
	rr := doWithToken(srv, "POST", "/jobs", body, tok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("submit: want 403, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var seenDeny bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventAuthzDeny {
			continue
		}
		seenDeny = true
		if ev.Principal != "node:gpu-01" {
			t.Errorf("Principal: want 'node:gpu-01', got %q", ev.Principal)
		}
		if ev.PrincipalKind != string(principal.KindNode) {
			t.Errorf("PrincipalKind: want 'node', got %q", ev.PrincipalKind)
		}
		if ev.Details["code"] != "node_not_allowed" {
			t.Errorf("deny code: want node_not_allowed, got %v", ev.Details["code"])
		}
	}
	if !seenDeny {
		t.Fatal("no authz_deny audit event found")
	}
}

// ── cert-CN takes precedence over JWT ──────────────────────────────────────

func TestAuthMiddleware_OperatorCertWinsOverJWT(t *testing.T) {
	// When clientCertMiddleware stamps a KindOperator principal
	// into context, authMiddleware's later JWT resolution must
	// NOT overwrite it. The cert is the strictly stronger
	// identity (feature 27).
	tm, _ := auth.NewTokenManager(context.Background(), newTokenStore())
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)
	// Activate the cert tier so the middleware runs.
	srv.SetClientCertTier(api.ClientCertWarn)

	tok, _ := tm.GenerateToken(context.Background(), "alice", "admin", time.Minute)

	// Simulate a request arriving via Nginx with verified-cert
	// headers + an Authorization bearer. The JWT subject is
	// "alice"; the cert CN is "alice@ops" (a different string).
	// Principal in the audit event MUST be the cert CN.
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader(`{"id":"p-35-op","command":"echo"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-SSL-Client-Verify", "SUCCESS")
	req.Header.Set("X-SSL-Client-S-DN", "CN=alice@ops,O=Helion")
	req.RemoteAddr = "127.0.0.1:12345" // loopback so the proxy headers are trusted
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var seen bool
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventJobSubmit {
			continue
		}
		seen = true
		if ev.Principal != "operator:alice@ops" {
			t.Errorf("Principal: want 'operator:alice@ops', got %q", ev.Principal)
		}
		if ev.PrincipalKind != string(principal.KindOperator) {
			t.Errorf("PrincipalKind: want 'operator', got %q", ev.PrincipalKind)
		}
	}
	if !seen {
		t.Fatal("no job_submit audit event found")
	}
}

// ── coordinator-internal service principal stamping ───────────────────────

func TestLogCoordinatorStart_StampsServicePrincipal(t *testing.T) {
	// Feature 35: LogCoordinatorStart uses stampServiceIfMissing
	// to default-stamp ServiceCoordinator into ctx when no
	// Principal is present. Event.Actor stays "system" for
	// back-compat (logger_test.go:245 asserts it); Event.Principal
	// carries the typed Kind.
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)

	if err := auditLog.LogCoordinatorStart(context.Background(), "v35-test"); err != nil {
		t.Fatalf("LogCoordinatorStart: %v", err)
	}
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	var ev audit.Event
	_ = json.Unmarshal(entries[0], &ev)
	if ev.Actor != "system" {
		t.Errorf("Actor: want 'system' (back-compat), got %q", ev.Actor)
	}
	if ev.Principal != "service:coordinator" {
		t.Errorf("Principal: want 'service:coordinator', got %q", ev.Principal)
	}
	if ev.PrincipalKind != string(principal.KindService) {
		t.Errorf("PrincipalKind: want 'service', got %q", ev.PrincipalKind)
	}
}

func TestLogCoordinatorStart_PreservesCallerStampedPrincipal(t *testing.T) {
	// If the caller already stamped a specific service principal,
	// stampServiceIfMissing is a no-op — the specific one wins.
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	ctx := principal.NewContext(context.Background(), principal.ServiceDispatcher)

	if err := auditLog.LogCoordinatorStart(ctx, "v35-test"); err != nil {
		t.Fatalf("LogCoordinatorStart: %v", err)
	}
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var ev audit.Event
	_ = json.Unmarshal(entries[0], &ev)
	if ev.Principal != "service:dispatcher" {
		t.Errorf("Principal: caller-stamped should win; got %q", ev.Principal)
	}
}

// ── anonymous doesn't leak Principal fields ────────────────────────────────

func TestAudit_AnonymousContext_NoPrincipalLeak(t *testing.T) {
	// A ctx with Anonymous() Principal (or none at all) must
	// produce Event.Principal == "". The omitempty JSON tag
	// elides it; a reviewer scanning the log doesn't see a
	// misleading "anonymous" entry for every legacy caller.
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)

	// Using Log() directly with a plain context.Background() —
	// no Principal stamped.
	if err := auditLog.Log(context.Background(), "test.event", "alice",
		map[string]interface{}{"k": "v"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	entries, _ := store.Scan(context.Background(), "audit:", 0)
	var ev audit.Event
	_ = json.Unmarshal(entries[0], &ev)
	if ev.Principal != "" {
		t.Errorf("Principal: want empty (anonymous), got %q", ev.Principal)
	}
	if ev.PrincipalKind != "" {
		t.Errorf("PrincipalKind: want empty, got %q", ev.PrincipalKind)
	}
	if ev.Actor != "alice" {
		t.Errorf("Actor unchanged: want 'alice', got %q", ev.Actor)
	}
}
