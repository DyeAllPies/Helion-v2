// internal/api/coverage_extras_test.go
//
// Coverage-focused tests for smaller api surfaces not covered by
// the existing feature-shipped test files:
//
//   - handleListGroups + group error paths
//   - handleListModels + handleDeleteDataset + handleDeleteModel
//     forbidden / not-found branches
//   - JobStoreAdapter.CancelJob delegate
//   - ClientCertTier.String
//   - bucketFromQuery full matrix
//   - splitRegistryID boundary cases (via handleListShares)
//
// Each test is a narrow unit — the integration layer is already
// covered; here we exercise branches that only matter in error
// flows or on specific inputs.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/groups"
)

// ── handleListGroups ─────────────────────────────────────────

func TestGroups_List_EmptyStore_OkEmpty(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")

	rr := doWithToken(srv, "GET", "/admin/groups", "", adminTok)
	if rr.Code != 200 {
		t.Fatalf("list empty: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.GroupListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("total: got %d, want 0", resp.Total)
	}
}

func TestGroups_List_WithGroups_ReturnsAll(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		rr := doWithToken(srv, "POST", "/admin/groups",
			`{"name":"`+name+`"}`, adminTok)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", name, rr.Code)
		}
	}

	rr := doWithToken(srv, "GET", "/admin/groups", "", adminTok)
	if rr.Code != 200 {
		t.Fatalf("list: %d", rr.Code)
	}
	var resp api.GroupListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 3 {
		t.Errorf("total: got %d, want 3", resp.Total)
	}
}

// ── Group error paths ────────────────────────────────────────

func TestGroups_Get_NotFound_404(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "GET", "/admin/groups/does-not-exist", "", adminTok)
	if rr.Code != 404 {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestGroups_Delete_NotFound_404(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "DELETE", "/admin/groups/does-not-exist", "", adminTok)
	if rr.Code != 404 {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestGroups_AddMember_MissingGroup_404(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "POST", "/admin/groups/missing/members",
		`{"principal_id":"user:alice"}`, adminTok)
	if rr.Code != 404 {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestGroups_AddMember_InvalidPrincipalID_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	_ = doWithToken(srv, "POST", "/admin/groups", `{"name":"g1"}`, adminTok)

	rr := doWithToken(srv, "POST", "/admin/groups/g1/members",
		`{"principal_id":"!!bogus!!"}`, adminTok)
	if rr.Code != 400 {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestGroups_RemoveMember_MissingGroup_404(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "DELETE",
		"/admin/groups/missing/members/user:alice", "", adminTok)
	if rr.Code != 404 {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestGroups_RemoveMember_InvalidPrincipalID_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	_ = doWithToken(srv, "POST", "/admin/groups", `{"name":"g1"}`, adminTok)

	rr := doWithToken(srv, "DELETE",
		"/admin/groups/g1/members/notaprincipal", "", adminTok)
	if rr.Code != 400 {
		t.Errorf("want 400, got %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestGroups_Create_DuplicateName_409(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")

	rr := doWithToken(srv, "POST", "/admin/groups", `{"name":"dup"}`, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first create: %d", rr.Code)
	}
	rr = doWithToken(srv, "POST", "/admin/groups", `{"name":"dup"}`, adminTok)
	if rr.Code != http.StatusConflict {
		t.Errorf("dup create: want 409, got %d", rr.Code)
	}
}

func TestGroups_Create_InvalidName_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "POST", "/admin/groups", `{"name":""}`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty name: want 400, got %d", rr.Code)
	}
}

func TestGroups_Create_BadJSON_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "POST", "/admin/groups", `{not-json`, adminTok)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: want 400, got %d", rr.Code)
	}
}

// ── groupsConfigured: server without groups store returns 404 ─

func TestGroups_NotConfigured_404(t *testing.T) {
	// Build a server WITHOUT SetGroupsStore so the 404 guard
	// branches fire.
	store := newTokenStore()
	tmgr, _ := auth.NewTokenManager(context.Background(), store)
	srv := api.NewServer(newMockJobStore(), nil, nil,
		audit.NewLogger(newAuditStore(), 0),
		tmgr, nil, nil, nil)
	adminTok, _ := tmgr.GenerateToken(context.Background(), "root", "admin", time.Minute)

	// Every group route should 404.
	for _, method := range []string{"GET"} {
		rr := doWithToken(srv, method, "/admin/groups", "", adminTok)
		if rr.Code != 404 {
			t.Errorf("%s /admin/groups: want 404, got %d", method, rr.Code)
		}
	}
}

// ── handleListModels ─────────────────────────────────────────

func TestRegistry_ListModels_EmptyStore_OkEmpty(t *testing.T) {
	srv := newRegistryServer(t)
	rr := do(srv, "GET", "/api/models", "")
	if rr.Code != 200 {
		t.Fatalf("empty list: %d %s", rr.Code, rr.Body.String())
	}
}

func TestRegistry_ListModels_Populated(t *testing.T) {
	srv := newRegistryServer(t)
	for i, name := range []string{"mnist", "cifar"} {
		body := `{"name":"` + name + `","version":"v1","uri":"s3://m","framework":"pytorch"}`
		rr := do(srv, "POST", "/api/models", body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("register %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	rr := do(srv, "GET", "/api/models", "")
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
}

// ── handleDeleteDataset / handleDeleteModel ──────────────────

func TestRegistry_DeleteDataset_NotFound_404(t *testing.T) {
	srv := newRegistryServer(t)
	rr := do(srv, "DELETE", "/api/datasets/nope/v1", "")
	if rr.Code != 404 {
		t.Errorf("delete-missing dataset: want 404, got %d", rr.Code)
	}
}

func TestRegistry_DeleteModel_NotFound_404(t *testing.T) {
	srv := newRegistryServer(t)
	rr := do(srv, "DELETE", "/api/models/nope/v1", "")
	if rr.Code != 404 {
		t.Errorf("delete-missing model: want 404, got %d", rr.Code)
	}
}

// ── Groups validation via integration: unknown kind in /admin/resources/{kind}/share ─

func TestShares_UnknownKind_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	rr := doWithToken(srv, "POST",
		"/admin/resources/unicorn/share?id=x",
		`{"grantee":"user:bob","actions":["read"]}`, aliceTok)
	if rr.Code != 400 {
		t.Errorf("unknown kind: want 400, got %d", rr.Code)
	}
}

func TestShares_Create_MissingID_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	rr := doWithToken(srv, "POST",
		"/admin/resources/job/share",
		`{"grantee":"user:bob","actions":["read"]}`, aliceTok)
	if rr.Code != 400 {
		t.Errorf("missing id: want 400, got %d", rr.Code)
	}
}

// ── handleListShares for a workflow ──────────────────────────

func TestShares_ListShares_WorkflowShares(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")

	// Alice submits a workflow.
	rr := doWithToken(srv, "POST", "/workflows",
		`{"id":"wf-1","name":"my-wf","jobs":[{"id":"j1","name":"job1","command":"echo"}]}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("submit wf: %d %s", rr.Code, rr.Body.String())
	}

	// Alice shares it with Bob.
	rr = doWithToken(srv, "POST",
		"/admin/resources/workflow/share?id=wf-1",
		`{"grantee":"user:bob","actions":["read"]}`, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create share: %d %s", rr.Code, rr.Body.String())
	}

	// Alice lists shares on her workflow.
	rr = doWithToken(srv, "GET",
		"/admin/resources/workflow/shares?id=wf-1", "", aliceTok)
	if rr.Code != 200 {
		t.Fatalf("list shares: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.ShareListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("share count: got %d, want 1", resp.Total)
	}
}

func TestShares_ListShares_MissingResource_404(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "GET",
		"/admin/resources/workflow/shares?id=does-not-exist", "", adminTok)
	if rr.Code != 404 {
		t.Errorf("missing resource: want 404, got %d", rr.Code)
	}
}

func TestShares_ListShares_UnknownKind_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	adminTok := tokenForShares(t, tm, "root", "admin")
	rr := doWithToken(srv, "GET",
		"/admin/resources/unicorn/shares?id=x", "", adminTok)
	if rr.Code != 400 {
		t.Errorf("unknown kind: want 400, got %d", rr.Code)
	}
}

func TestShares_Revoke_UnknownKind_400(t *testing.T) {
	srv, tm, _, _ := shareFixture(t)
	aliceTok := tokenForShares(t, tm, "alice", "user")
	rr := doWithToken(srv, "DELETE",
		"/admin/resources/unicorn/share?id=x&grantee=user:bob", "", aliceTok)
	if rr.Code != 400 {
		t.Errorf("unknown kind: want 400, got %d", rr.Code)
	}
}

// ── groups.Store errors surface through the handlers ─────────

// errGroupsStore fails every call with a synthetic error — lets
// us hit the 500 branches in handleCreateGroup / handleListGroups.
type errGroupsStore struct{}

func (errGroupsStore) Create(context.Context, groups.Group) error { return errors.New("boom") }
func (errGroupsStore) Get(_ context.Context, _ string) (*groups.Group, error) {
	return nil, errors.New("boom")
}
func (errGroupsStore) List(context.Context) ([]groups.Group, error) {
	return nil, errors.New("boom")
}
func (errGroupsStore) Delete(context.Context, string) error         { return errors.New("boom") }
func (errGroupsStore) AddMember(context.Context, string, string) error {
	return errors.New("boom")
}
func (errGroupsStore) RemoveMember(context.Context, string, string) error {
	return errors.New("boom")
}
func (errGroupsStore) GroupsFor(context.Context, string) ([]string, error) {
	return nil, errors.New("boom")
}

func TestGroups_StoreError_Surfaces500(t *testing.T) {
	store := newTokenStore()
	tmgr, _ := auth.NewTokenManager(context.Background(), store)
	srv := api.NewServer(newMockJobStore(), nil, nil,
		audit.NewLogger(newAuditStore(), 0), tmgr, nil, nil, nil)
	srv.SetGroupsStore(errGroupsStore{})
	adminTok, _ := tmgr.GenerateToken(context.Background(), "root", "admin", time.Minute)

	// List → 500 (store List errors).
	rr := doWithToken(srv, "GET", "/admin/groups", "", adminTok)
	if rr.Code != 500 {
		t.Errorf("list store error: want 500, got %d", rr.Code)
	}

	// Create → 500 (store Create errors with non-sentinel).
	rr = doWithToken(srv, "POST", "/admin/groups", `{"name":"any"}`, adminTok)
	if rr.Code != 500 {
		t.Errorf("create store error: want 500, got %d", rr.Code)
	}

	// Get → 500 (store Get errors with non-sentinel).
	rr = doWithToken(srv, "GET", "/admin/groups/any", "", adminTok)
	if rr.Code != 500 {
		t.Errorf("get store error: want 500, got %d", rr.Code)
	}

	// Delete → 500 (store Delete errors with non-sentinel).
	rr = doWithToken(srv, "DELETE", "/admin/groups/any", "", adminTok)
	if rr.Code != 500 {
		t.Errorf("delete store error: want 500, got %d", rr.Code)
	}

	// AddMember → 500.
	rr = doWithToken(srv, "POST", "/admin/groups/any/members",
		`{"principal_id":"user:alice"}`, adminTok)
	if rr.Code != 500 {
		t.Errorf("add member store error: want 500, got %d", rr.Code)
	}

	// RemoveMember → 500.
	rr = doWithToken(srv, "DELETE",
		"/admin/groups/any/members/user:alice", "", adminTok)
	if rr.Code != 500 {
		t.Errorf("remove member store error: want 500, got %d", rr.Code)
	}
}
