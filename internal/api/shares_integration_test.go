// internal/api/shares_integration_test.go
//
// Feature 38 — end-to-end integration tests for group + share
// delegation. Verifies the HTTP-layer wiring: a group share
// lets a non-owner read a workflow; revoking it kicks them
// out; non-owners cannot mutate shares.
//
// The evaluator's rule 6b is already covered by table tests in
// internal/authz/authz_test.go. Here we prove the handlers
// actually consult the Shares slice and that the groups store
// is wired into the middleware correctly.

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/groups"
)

// shareFixture stands up an auth-enabled server with all the
// feature 35-38 wiring: tokenManager, audit log, job store,
// workflow store, and a groups store. Same shape as
// authzFixture from feature 37 tests but adds the groups
// store.
func shareFixture(t *testing.T) (*api.Server, *auth.TokenManager, *inMemoryAuditStore, groups.Store) {
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

	gs := groups.NewMemStore()
	s.SetGroupsStore(gs)

	return s, tmgr, aStore, gs
}

func tokenForShares(t *testing.T, tm *auth.TokenManager, subject, role string) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), subject, role, time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken %s/%s: %v", subject, role, err)
	}
	return tok
}

// ── Workflow share: group-scoped read ──────────────────────

func TestShares_WorkflowSharedWithGroup_MemberCanRead(t *testing.T) {
	srv, tm, _, gs := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	bobTok := tokenForShares(t, tm, "bob", "user")

	// Seed group ml-team with Bob.
	ctx := context.Background()
	_ = gs.Create(ctx, groups.Group{Name: "ml-team", CreatedBy: "user:root"})
	_ = gs.AddMember(ctx, "ml-team", "user:bob")

	// Alice submits a workflow.
	rr := doWithToken(srv, "POST", "/workflows", `{
		"id":"wf-share1","name":"p",
		"jobs":[{"name":"a","command":"echo"}]
	}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	// Bob cannot read (no share yet).
	rr = doWithToken(srv, "GET", "/workflows/wf-share1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bob pre-share: want 403, got %d", rr.Code)
	}

	// Alice shares with group:ml-team.
	body := `{"grantee":"group:ml-team","actions":["read"]}`
	rr = doWithToken(srv, "POST",
		"/admin/resources/workflow/share?id=wf-share1", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("share create: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// Bob can now read via group membership.
	rr = doWithToken(srv, "GET", "/workflows/wf-share1", "", bobTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("bob post-share: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Revoke the share.
	rr = doWithToken(srv, "DELETE",
		"/admin/resources/workflow/share?id=wf-share1&grantee=group:ml-team",
		"", aliceTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("share revoke: want 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Revoke takes effect immediately — next Bob read is 403.
	rr = doWithToken(srv, "GET", "/workflows/wf-share1", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bob post-revoke: want 403, got %d", rr.Code)
	}
}

// ── Non-owner cannot share ─────────────────────────────────

func TestShares_NonOwner_CannotShare(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	bobTok := tokenForShares(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/workflows", `{
		"id":"wf-noshare","name":"p",
		"jobs":[{"name":"a","command":"echo"}]
	}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	// Bob tries to share Alice's workflow with himself (would
	// be an escalation-via-share attempt).
	rr = doWithToken(srv, "POST",
		"/admin/resources/workflow/share?id=wf-noshare",
		`{"grantee":"user:bob","actions":["read"]}`, bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner share: want 403, got %d: %s", rr.Code, rr.Body.String())
	}

	// Confirm no share was added — Bob still cannot read.
	rr = doWithToken(srv, "GET", "/workflows/wf-noshare", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bob GET after denied share attempt: want 403, got %d", rr.Code)
	}
}

// ── Direct share (no group) ────────────────────────────────

func TestShares_DirectShare_AllowsGrantee(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	bobTok := tokenForShares(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"j-share1","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "POST",
		"/admin/resources/job/share?id=j-share1",
		`{"grantee":"user:bob","actions":["read"]}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("share: %d %s", rr.Code, rr.Body.String())
	}

	rr = doWithToken(srv, "GET", "/jobs/j-share1", "", bobTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("bob read via direct share: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── Action scoping: read-share does not grant cancel ───────

func TestShares_ReadShare_DoesNotGrantCancel(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	bobTok := tokenForShares(t, tm, "bob", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"j-rc1","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	rr = doWithToken(srv, "POST",
		"/admin/resources/job/share?id=j-rc1",
		`{"grantee":"user:bob","actions":["read"]}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("share: %d", rr.Code)
	}
	// Bob can read.
	rr = doWithToken(srv, "GET", "/jobs/j-rc1", "", bobTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("bob read: %d", rr.Code)
	}
	// But Bob cannot cancel.
	rr = doWithToken(srv, "POST", "/jobs/j-rc1/cancel", "", bobTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bob cancel via read-share: want 403, got %d", rr.Code)
	}
}

// ── Share mutation emits resource_shared audit event ───────

func TestShares_CreateEmitsAuditEvent(t *testing.T) {
	srv, tm, aStore, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"j-audit1","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d", rr.Code)
	}
	rr = doWithToken(srv, "POST",
		"/admin/resources/job/share?id=j-audit1",
		`{"grantee":"user:bob","actions":["read"]}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("share: %d", rr.Code)
	}

	entries, _ := aStore.Scan(context.Background(), "audit:", 1000)
	seen := false
	for _, raw := range entries {
		var ev audit.Event
		_ = json.Unmarshal(raw, &ev)
		if ev.Type != audit.EventResourceShared {
			continue
		}
		seen = true
		if ev.Details["resource_kind"] != "job" {
			t.Errorf("resource_kind: got %v", ev.Details["resource_kind"])
		}
		if ev.Details["grantee"] != "user:bob" {
			t.Errorf("grantee: got %v", ev.Details["grantee"])
		}
		if ev.Principal != "user:alice" {
			t.Errorf("principal: got %q", ev.Principal)
		}
	}
	if !seen {
		t.Fatal("no EventResourceShared audit entry")
	}
}

// ── Group CRUD end-to-end ──────────────────────────────────

func TestShares_GroupLifecycle_EndToEnd(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	userTok := tokenForShares(t, tm, "alice", "user")

	// Non-admin cannot create a group.
	rr := doWithToken(srv, "POST", "/admin/groups", `{"name":"nope"}`, userTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin create: want 403, got %d", rr.Code)
	}

	// Admin creates.
	rr = doWithToken(srv, "POST", "/admin/groups", `{"name":"ml-team"}`, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin create: %d %s", rr.Code, rr.Body.String())
	}

	// Duplicate → 409.
	rr = doWithToken(srv, "POST", "/admin/groups", `{"name":"ml-team"}`, adminTok)
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate create: want 409, got %d", rr.Code)
	}

	// Add Alice.
	rr = doWithToken(srv, "POST", "/admin/groups/ml-team/members",
		`{"principal_id":"user:alice"}`, adminTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("add member: %d %s", rr.Code, rr.Body.String())
	}

	// List should show alice in members.
	rr = doWithToken(srv, "GET", "/admin/groups/ml-team", "", adminTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("get group: %d", rr.Code)
	}
	var gresp api.GroupResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &gresp)
	if len(gresp.Members) != 1 || gresp.Members[0] != "user:alice" {
		t.Fatalf("members: want [user:alice], got %v", gresp.Members)
	}

	// Remove Alice.
	rr = doWithToken(srv, "DELETE",
		"/admin/groups/ml-team/members/user:alice", "", adminTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove member: %d %s", rr.Code, rr.Body.String())
	}

	// Delete group.
	rr = doWithToken(srv, "DELETE", "/admin/groups/ml-team", "", adminTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete group: %d %s", rr.Code, rr.Body.String())
	}
}

// Dataset + model share paths are exercised end-to-end through
// the workflow + job tests above; the registry-specific wiring
// (BadgerDB + RegisterDataset validators) doesn't change the
// share evaluation path. If a regression appears in registry
// share persistence it would fail the unit tests in
// internal/registry.

// ── Share cap ──────────────────────────────────────────────

func TestShares_RejectsBeyondCap(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")

	rr := doWithToken(srv, "POST", "/jobs",
		`{"id":"j-cap1","command":"echo"}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit: %d", rr.Code)
	}
	for i := 0; i < 32; i++ {
		body := fmt.Sprintf(`{"grantee":"user:u%d","actions":["read"]}`, i)
		rr = doWithToken(srv, "POST",
			"/admin/resources/job/share?id=j-cap1", body, aliceTok)
		if rr.Code != http.StatusCreated {
			t.Fatalf("share %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	// 33rd should 400.
	rr = doWithToken(srv, "POST",
		"/admin/resources/job/share?id=j-cap1",
		`{"grantee":"user:over","actions":["read"]}`, aliceTok)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("33rd share: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}
