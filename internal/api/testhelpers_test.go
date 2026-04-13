// internal/api/testhelpers_test.go
//
// Shared test infrastructure: mock implementations, in-memory stores, and
// helper functions used across all handler test files.

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Mock job store ─────────────────────────────────────────────────────────────

type mockJobStore struct {
	jobs      map[string]*cpb.Job
	submitErr error
	getErr    error
	listErr   error
}

func newMockJobStore() *mockJobStore {
	return &mockJobStore{jobs: make(map[string]*cpb.Job)}
}

func (m *mockJobStore) Submit(_ context.Context, j *cpb.Job) error {
	if m.submitErr != nil {
		return m.submitErr
	}
	m.jobs[j.ID] = j
	return nil
}

func (m *mockJobStore) Get(jobID string) (*cpb.Job, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, errors.New("not found")
	}
	return j, nil
}

func (m *mockJobStore) List(_ context.Context, _ string, _, _ int) ([]*cpb.Job, int, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	var jobs []*cpb.Job
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	return jobs, len(jobs), nil
}

func (m *mockJobStore) CancelJob(_ context.Context, jobID, _ string) error {
	j, ok := m.jobs[jobID]
	if !ok {
		return fmt.Errorf("%w: %s", cluster.ErrJobNotFound, jobID)
	}
	if j.Status.IsTerminal() {
		return fmt.Errorf("%w: %s is %s", cluster.ErrJobAlreadyTerminal, jobID, j.Status)
	}
	j.Status = cpb.JobStatusCancelled
	return nil
}

func (m *mockJobStore) GetJobsByStatus(_ context.Context, status string) ([]*cpb.Job, error) {
	var jobs []*cpb.Job
	for _, j := range m.jobs {
		if j.Status.String() == status {
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

// ── Mock node registry ────────────────────────────────────────────────────────

type mockNodeRegistry struct {
	nodes     []api.NodeInfo
	listErr   error
	revokeErr error
}

func (m *mockNodeRegistry) ListNodes(_ context.Context) ([]api.NodeInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.nodes, nil
}

func (m *mockNodeRegistry) GetNodeHealth(nodeID string) (string, time.Time, error) {
	return "healthy", time.Now(), nil
}

func (m *mockNodeRegistry) GetRunningJobCount(_ string) int { return 0 }

func (m *mockNodeRegistry) RevokeNode(_ context.Context, _, _ string) error {
	return m.revokeErr
}

// ── Mock metrics provider ─────────────────────────────────────────────────────

type mockMetricsProvider struct {
	metrics *api.ClusterMetrics
	err     error
}

func (m *mockMetricsProvider) GetClusterMetrics(_ context.Context) (*api.ClusterMetrics, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.metrics, nil
}

// ── Mock readiness checker ────────────────────────────────────────────────────

type mockReadinessChecker struct {
	pingErr     error
	registryLen int
}

func (m *mockReadinessChecker) Ping() error      { return m.pingErr }
func (m *mockReadinessChecker) RegistryLen() int { return m.registryLen }

// ── In-memory audit store ─────────────────────────────────────────────────────

type inMemoryAuditStore struct {
	entries map[string][]byte
}

func newAuditStore() *inMemoryAuditStore {
	return &inMemoryAuditStore{entries: make(map[string][]byte)}
}

func (s *inMemoryAuditStore) Put(_ context.Context, key string, value []byte) error {
	s.entries[key] = append([]byte{}, value...)
	return nil
}

func (s *inMemoryAuditStore) PutWithTTL(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.entries[key] = append([]byte{}, value...)
	return nil
}

func (s *inMemoryAuditStore) Scan(_ context.Context, prefix string, _ int) ([][]byte, error) {
	var results [][]byte
	for k, v := range s.entries {
		if strings.HasPrefix(k, prefix) {
			results = append(results, v)
		}
	}
	return results, nil
}

// failOnScanStore wraps inMemoryAuditStore but fails on Scan.
type failOnScanStore struct {
	*inMemoryAuditStore
}

func (s *failOnScanStore) Scan(_ context.Context, _ string, _ int) ([][]byte, error) {
	return nil, errors.New("storage unavailable")
}

// ── In-memory token store ─────────────────────────────────────────────────────

// inMemoryTokenStore satisfies auth.TokenStore for testing.
type inMemoryTokenStore struct {
	data map[string][]byte
}

func newTokenStore() *inMemoryTokenStore {
	return &inMemoryTokenStore{data: make(map[string][]byte)}
}

func (s *inMemoryTokenStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}

func (s *inMemoryTokenStore) Put(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.data[key] = append([]byte{}, value...)
	return nil
}

func (s *inMemoryTokenStore) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}

// ── Server construction helpers ───────────────────────────────────────────────

// newServer creates a minimal API server with no auth (tokenManager=nil).
//
// AUDIT H2 (fixed): nil tokenManager no longer silently bypasses auth. Tests
// that want the no-auth path must call DisableAuth() explicitly, which this
// helper does so existing tests continue to work unchanged.
func newServer(jobs api.JobStoreIface, nodes api.NodeRegistryIface, metrics api.MetricsProvider) *api.Server {
	srv := api.NewServer(jobs, nodes, metrics, nil, nil, nil, nil, nil)
	srv.DisableAuth()
	return srv
}

// newAuthServer creates a server with a real in-memory token manager.
func newAuthServer(t *testing.T) (*api.Server, *auth.TokenManager) {
	t.Helper()
	store := newTokenStore()
	tm, err := auth.NewTokenManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)
	return srv, tm
}

// adminToken issues an admin-role token from tm.
func adminToken(t *testing.T, tm *auth.TokenManager) string {
	t.Helper()
	tok, err := tm.GenerateToken(context.Background(), "root", "admin", time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken admin: %v", err)
	}
	return tok
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

// do fires a request against srv and returns the recorded response.
func do(srv *api.Server, method, path string, body string) *httptest.ResponseRecorder {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// doWithToken fires a request with a Bearer token header.
func doWithToken(srv *api.Server, method, path, body, token string) *httptest.ResponseRecorder {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// Silence unused import — time is used by mockNodeRegistry.GetNodeHealth.
var _ = http.StatusOK
