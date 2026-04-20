// internal/api/secretstore_fixture_test.go
//
// Shared fixture for handlers_secretstore_test.go. Builds one
// auth-enabled server + matching token pair so the rotate
// handler tests exercise both admin and non-admin paths
// against the SAME tokenManager.

package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
)

// secretStoreFixture returns (server, adminToken, userToken,
// auditStore). The server is wired with the provided
// SecretStoreAdmin so each test can plant its own fake.
func secretStoreFixture(t *testing.T, adm api.SecretStoreAdmin) (*api.Server, string, string, *inMemoryAuditStore) {
	t.Helper()
	store := newTokenStore()
	tmgr, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	adminTok, err := tmgr.GenerateToken(context.Background(), "root", "admin", time.Minute)
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	userTok, err := tmgr.GenerateToken(context.Background(), "alice", "user", time.Minute)
	if err != nil {
		t.Fatalf("user token: %v", err)
	}
	aStore := newAuditStore()
	aLog := audit.NewLogger(aStore, 0)

	js := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	adapter := api.NewJobStoreAdapter(js)
	srv := api.NewServer(adapter, nil, nil, aLog, tmgr, nil, nil, nil)
	if adm != nil {
		srv.SetSecretStoreAdmin(adm)
	}
	return srv, adminTok, userTok, aStore
}
