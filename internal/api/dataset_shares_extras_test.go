// internal/api/dataset_shares_extras_test.go
//
// Dataset + model share handlers — integration-style tests
// that exercise handleCreateShare / handleListShares /
// handleRevokeShare for registry-backed resources (dataset,
// model). The existing shares_integration_test.go covers
// workflow + job shares; this file fills the registry gap.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/groups"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

// registryShareFixture stands up an auth-enabled server with
// groups + registry + job + workflow stores — the full set
// feature 38 depends on for dataset/model share evaluation.
func registryShareFixture(t *testing.T) (*api.Server, *auth.TokenManager) {
	t.Helper()
	tokstore := newTokenStore()
	tmgr, err := auth.NewTokenManager(context.Background(), tokstore)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	aStore := newAuditStore()
	aLog := audit.NewLogger(aStore, 0)

	cs := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(cs)
	srv := api.NewServer(adapter, nil, nil, aLog, tmgr, nil, nil, nil)

	ws := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	srv.SetWorkflowStore(ws, cs)
	srv.SetEventBus(events.NewBus(10, nil))
	srv.SetGroupsStore(groups.NewMemStore())

	opts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv.SetRegistryStore(registry.NewBadgerStore(db))

	return srv, tmgr
}

func mkTokenRS(t *testing.T, tm *auth.TokenManager, subject, role string) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), subject, role, time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken %s/%s: %v", subject, role, err)
	}
	return tok
}

// ── Dataset share lifecycle ──────────────────────────────────

func TestShares_Dataset_CreateListRevoke(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")

	// Alice registers a dataset.
	body := `{
		"name": "iris",
		"version": "v1.0.0",
		"uri":  "s3://helion/datasets/iris/v1.0.0.parquet"
	}`
	rr := doWithToken(srv, "POST", "/api/datasets", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register dataset: %d %s", rr.Code, rr.Body.String())
	}

	// Alice shares it with Bob. Dataset IDs use "name/version" form.
	shareBody := `{"grantee":"user:bob","actions":["read"]}`
	rr = doWithToken(srv, "POST",
		"/admin/resources/dataset/share?id=iris/v1.0.0",
		shareBody, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create share: %d %s", rr.Code, rr.Body.String())
	}

	// Alice lists shares.
	rr = doWithToken(srv, "GET",
		"/admin/resources/dataset/shares?id=iris/v1.0.0", "", aliceTok)
	if rr.Code != 200 {
		t.Fatalf("list shares: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.ShareListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("share count: got %d, want 1", resp.Total)
	}

	// Bob (grantee) can read the dataset via GET /api/datasets/.
	bobTok := mkTokenRS(t, tm, "bob", "user")
	rr = doWithToken(srv, "GET", "/api/datasets/iris/v1.0.0", "", bobTok)
	if rr.Code != 200 {
		t.Fatalf("bob reads shared dataset: %d %s", rr.Code, rr.Body.String())
	}

	// Alice revokes Bob's share.
	rr = doWithToken(srv, "DELETE",
		"/admin/resources/dataset/share?id=iris/v1.0.0&grantee=user:bob",
		"", aliceTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}

	// Bob can no longer read.
	rr = doWithToken(srv, "GET", "/api/datasets/iris/v1.0.0", "", bobTok)
	if rr.Code == 200 {
		t.Errorf("bob reads after revoke: want denied, got 200")
	}
}

// ── Model share lifecycle ────────────────────────────────────

func TestShares_Model_CreateListRevoke(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")

	// Alice registers a model.
	body := `{"name":"mnist","version":"v1","uri":"s3://m","framework":"pytorch"}`
	rr := doWithToken(srv, "POST", "/api/models", body, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register model: %d %s", rr.Code, rr.Body.String())
	}

	// Alice shares it with a group. Creates a group first.
	adminTok := mkTokenRS(t, tm, "root", "admin")
	rr = doWithToken(srv, "POST", "/admin/groups", `{"name":"ml-team"}`, adminTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", rr.Code, rr.Body.String())
	}

	shareBody := `{"grantee":"group:ml-team","actions":["read"]}`
	rr = doWithToken(srv, "POST",
		"/admin/resources/model/share?id=mnist/v1",
		shareBody, aliceTok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create model share: %d %s", rr.Code, rr.Body.String())
	}

	// Alice lists.
	rr = doWithToken(srv, "GET",
		"/admin/resources/model/shares?id=mnist/v1", "", aliceTok)
	if rr.Code != 200 {
		t.Fatalf("list model shares: %d %s", rr.Code, rr.Body.String())
	}
	var resp api.ShareListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("share count: got %d, want 1", resp.Total)
	}

	// Revoke.
	rr = doWithToken(srv, "DELETE",
		"/admin/resources/model/share?id=mnist/v1&grantee=group:ml-team",
		"", aliceTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke model: %d %s", rr.Code, rr.Body.String())
	}
}

// ── Error paths ──────────────────────────────────────────────

func TestShares_Dataset_MalformedID_404(t *testing.T) {
	// splitRegistryID returns ErrNotFound on ids that don't
	// have name/version form. The handler must surface 404.
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	rr := doWithToken(srv, "GET",
		"/admin/resources/dataset/shares?id=no-slash-here", "", aliceTok)
	if rr.Code != 404 {
		t.Errorf("malformed id: want 404, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestShares_Dataset_TrailingSlash_404(t *testing.T) {
	// "name/" is also a malformed id.
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	rr := doWithToken(srv, "GET",
		"/admin/resources/dataset/shares?id=trailing/", "", aliceTok)
	if rr.Code != 404 {
		t.Errorf("trailing slash: want 404, got %d", rr.Code)
	}
}

func TestShares_Dataset_LeadingSlash_404(t *testing.T) {
	// "/version" is also malformed.
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	rr := doWithToken(srv, "GET",
		"/admin/resources/dataset/shares?id=/version", "", aliceTok)
	if rr.Code != 404 {
		t.Errorf("leading slash: want 404, got %d", rr.Code)
	}
}

func TestShares_Revoke_MissingGrantee_400(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	rr := doWithToken(srv, "DELETE",
		"/admin/resources/job/share?id=j-1", "", aliceTok)
	if rr.Code != 400 {
		t.Errorf("revoke without grantee: want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestShares_Revoke_MissingID_400(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	rr := doWithToken(srv, "DELETE",
		"/admin/resources/job/share?grantee=user:bob", "", aliceTok)
	if rr.Code != 400 {
		t.Errorf("revoke without id: want 400, got %d", rr.Code)
	}
}

func TestShares_Create_InvalidJSON_400(t *testing.T) {
	srv, tm := registryShareFixture(t)
	aliceTok := mkTokenRS(t, tm, "alice", "user")
	rr := doWithToken(srv, "POST",
		"/admin/resources/job/share?id=x",
		`{not-json`, aliceTok)
	if rr.Code != 400 {
		t.Errorf("bad json: want 400, got %d", rr.Code)
	}
}
