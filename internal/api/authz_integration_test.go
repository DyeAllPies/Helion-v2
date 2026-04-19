// internal/api/authz_integration_test.go
//
// Feature 37 — HTTP-level integration tests for the unified
// authorization policy.
//
// The authz evaluator has its own exhaustive table test in
// internal/authz/authz_test.go. These tests cover the end-to-end
// wiring: does the HTTP handler actually call Allow, does a deny
// produce a 403 with the expected `code` field, does the audit
// trail record the authz_deny event?
//
// Test inventory
// ──────────────
//   TestAuthz_WorkflowRead_NonOwner403
//   TestAuthz_WorkflowCancel_NonOwner403
//   TestAuthz_ListJobs_FiltersOutOthers
//   TestAuthz_ListWorkflows_FiltersOutOthers
//   TestAuthz_DatasetRead_NonOwner403
//   TestAuthz_AuthzDeny_EmitsAuditEvent
//   TestAuthz_ForbiddenResponseCarriesCode

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
)

// authzFixture wires an auth-enabled server with a real JobStore,
// WorkflowStore, and an in-memory audit store so tests can verify
// both HTTP behaviour AND the EventAuthzDeny audit emission.
func authzFixture(t *testing.T) (srv *api.Server, tm *auth.TokenManager, auditStore *inMemoryAuditStore) {
	t.Helper()
	store := newTokenStore()
	tmgr, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	aStore := newAuditStore()
	aLog := audit.NewLogger(aStore, 0)

	cs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(cs)
	s := api.NewServer(adapter, nil, nil, aLog, tmgr, nil, nil, nil)

	ws := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	s.SetWorkflowStore(ws, cs)
	s.SetEventBus(events.NewBus(10, nil))

	return s, tmgr, aStore
}

// tokenFor returns a signed JWT for (subject, role) with a short
// lifetime.
func tokenFor(t *testing.T, tm *auth.TokenManager, subject, role string) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), subject, role, time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken %s/%s: %v", subject, role, err)
	}
	return tok
}

// ── Workflow RBAC ──────────────────────────────────────────────

func TestAuthz_WorkflowRead_NonOwner403(t *testing.T) {
	srv, tm, _ := authzFixture(t)
	aliceTok := tokenFor(t, tm, "alice", "user")
	bobTok := tokenFor(t, tm, "bob", "user")

	// Alice submits a workflow.
	rr := doWithToken(srv, "POST", "/workflows", `{
		"id":"wf-a1","name":"alice-pipeline",
		"jobs":[{"name":"a","command":"echo"}]
	}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("alice submit wf: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// Bob reads Alice's workflow — must be forbidden with code=not_owner.
	rr = doWithToken(srv, "GET", "/workflows/wf-a1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bob reading alice's wf: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["code"] != "not_owner" {
		t.Errorf("403 code = %q; want not_owner", resp["code"])
	}

	// Alice reads her own — must succeed.
	rr = doWithToken(srv, "GET", "/workflows/wf-a1", "", aliceTok)
	if rr.Code != http.StatusOK {
		t.Errorf("alice reading own wf: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthz_WorkflowCancel_NonOwner403(t *testing.T) {
	srv, tm, _ := authzFixture(t)
	aliceTok := tokenFor(t, tm, "alice", "user")
	bobTok := tokenFor(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/workflows", `{
		"id":"wf-c1","name":"p",
		"jobs":[{"name":"a","command":"echo"}]
	}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "DELETE", "/workflows/wf-c1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bob cancel alice's wf: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── List filtering ─────────────────────────────────────────────

func TestAuthz_ListJobs_FiltersOutOthers(t *testing.T) {
	srv, tm, _ := authzFixture(t)
	aliceTok := tokenFor(t, tm, "alice", "user")
	bobTok := tokenFor(t, tm, "bob", "user")

	for _, id := range []string{"alice-j1", "alice-j2"} {
		rr := doWithToken(srv, "POST", "/jobs",
			`{"id":"`+id+`","command":"echo"}`, aliceTok)
		if rr.Code != http.StatusCreated {
			t.Fatalf("submit %s: %d %s", id, rr.Code, rr.Body.String())
		}
	}
	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"bob-j1","command":"echo"}`, bobTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit bob-j1: %d %s", rr.Code, rr.Body.String())
	}

	// Bob lists jobs — should see only bob-j1.
	rr = doWithToken(srv, "GET", "/jobs", "", bobTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("bob list: %d %s", rr.Code, rr.Body.String())
	}
	var listResp api.JobListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listResp.Total != 1 {
		t.Errorf("total = %d; want 1 (filtered)", listResp.Total)
	}
	for _, j := range listResp.Jobs {
		if strings.HasPrefix(j.ID, "alice-") {
			t.Errorf("leak: bob saw %q", j.ID)
		}
	}
}

func TestAuthz_ListWorkflows_FiltersOutOthers(t *testing.T) {
	srv, tm, _ := authzFixture(t)
	aliceTok := tokenFor(t, tm, "alice", "user")
	bobTok := tokenFor(t, tm, "bob", "user")

	body := func(id string) string {
		return `{"id":"` + id + `","name":"p","jobs":[{"name":"a","command":"echo"}]}`
	}
	for _, id := range []string{"wf-af1", "wf-af2"} {
		rr := doWithToken(srv, "POST", "/workflows", body(id), aliceTok)
		if rr.Code != http.StatusCreated {
			t.Fatalf("submit %s: %d %s", id, rr.Code, rr.Body.String())
		}
	}
	rr := doWithToken(srv, "POST", "/workflows", body("wf-bf1"), bobTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit wf-bf1: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "GET", "/workflows", "", bobTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("bob list wf: %d %s", rr.Code, rr.Body.String())
	}
	var listResp api.WorkflowListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listResp.Total != 1 {
		t.Errorf("total = %d; want 1", listResp.Total)
	}
	for _, wf := range listResp.Workflows {
		if strings.HasPrefix(wf.ID, "wf-af") {
			t.Errorf("leak: bob saw %q", wf.ID)
		}
	}
}

// ── Audit emission ─────────────────────────────────────────────

func TestAuthz_AuthzDeny_EmitsAuditEvent(t *testing.T) {
	srv, tm, auditStore := authzFixture(t)
	aliceTok := tokenFor(t, tm, "alice", "user")
	bobTok := tokenFor(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/workflows", `{
		"id":"wf-audit1","name":"p",
		"jobs":[{"name":"a","command":"echo"}]
	}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	// Bob tries to read — deny — must emit EventAuthzDeny.
	rr = doWithToken(srv, "GET", "/workflows/wf-audit1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}

	events, err := auditStore.Scan(context.Background(), "audit:", 1000)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	foundDeny := false
	for _, raw := range events {
		var ev audit.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		if ev.Type == audit.EventAuthzDeny {
			foundDeny = true
			if ev.Details["code"] != "not_owner" {
				t.Errorf("deny code = %v; want not_owner", ev.Details["code"])
			}
			if ev.Details["action"] != "read" {
				t.Errorf("deny action = %v; want read", ev.Details["action"])
			}
			if ev.Details["resource_kind"] != "workflow" {
				t.Errorf("deny resource_kind = %v; want workflow", ev.Details["resource_kind"])
			}
			if ev.Details["resource_owner"] != "user:alice" {
				t.Errorf("deny resource_owner = %v; want user:alice", ev.Details["resource_owner"])
			}
			if ev.Principal != "user:bob" {
				t.Errorf("deny principal = %q; want user:bob", ev.Principal)
			}
		}
	}
	if !foundDeny {
		t.Fatalf("no EventAuthzDeny audit entry found")
	}
}

// ── Response shape ─────────────────────────────────────────────

func TestAuthz_ForbiddenResponseCarriesCode(t *testing.T) {
	srv, tm, _ := authzFixture(t)
	aliceTok := tokenFor(t, tm, "alice", "user")
	bobTok := tokenFor(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"j-code1","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "GET", "/jobs/j-code1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "forbidden" {
		t.Errorf("error field = %q; want forbidden", resp["error"])
	}
	if resp["code"] != "not_owner" {
		t.Errorf("code field = %q; want not_owner", resp["code"])
	}
}

// TestAuthz_NodeRoleJWT_CannotSubmitJobs guards one of feature
// 37's load-bearing promises: a JWT with role=node (maps to
// KindNode principal) is refused on REST submit. Pre-feature-37
// this worked — nodes could submit jobs via the REST surface —
// which is the exploit vector for a compromised node's mTLS
// credential.
func TestAuthz_NodeRoleJWT_CannotSubmitJobs(t *testing.T) {
	srv, tm, _ := authzFixture(t)
	nodeTok := tokenFor(t, tm, "gpu-01", "node")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"j-node-exploit","command":"rm","args":["-rf","/"]}`, nodeTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("node role submit: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["code"] != "node_not_allowed" {
		t.Errorf("deny code = %q; want node_not_allowed", resp["code"])
	}
}
