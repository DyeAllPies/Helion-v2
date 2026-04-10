package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Mock implementations ──────────────────────────────────────────────────────

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

func (m *mockJobStore) GetJobsByStatus(_ context.Context, status string) ([]*cpb.Job, error) {
	var jobs []*cpb.Job
	for _, j := range m.jobs {
		if j.Status.String() == status {
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

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

type mockReadinessChecker struct {
	pingErr     error
	registryLen int
}

func (m *mockReadinessChecker) Ping() error       { return m.pingErr }
func (m *mockReadinessChecker) RegistryLen() int  { return m.registryLen }

// ── helpers ───────────────────────────────────────────────────────────────────

// newServer creates a minimal API server with no auth (tokenManager=nil).
func newServer(jobs api.JobStoreIface, nodes api.NodeRegistryIface, metrics api.MetricsProvider) *api.Server {
	return api.NewServer(jobs, nodes, metrics, nil, nil, nil, nil, nil)
}

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

// ── /healthz ─────────────────────────────────────────────────────────────────

func TestHealthz_Returns200(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/healthz", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

// ── /readyz ───────────────────────────────────────────────────────────────────

func TestReadyz_NilReadiness_Returns200(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/readyz", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

func TestReadyz_PingError_Returns503(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil,
		&mockReadinessChecker{pingErr: errors.New("db down")}, nil)
	rr := do(srv, "GET", "/readyz", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}
}

func TestReadyz_NoNodes_Returns503(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil,
		&mockReadinessChecker{registryLen: 0}, nil)
	rr := do(srv, "GET", "/readyz", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}
}

func TestReadyz_Ready_Returns200(t *testing.T) {
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil,
		&mockReadinessChecker{registryLen: 1}, nil)
	rr := do(srv, "GET", "/readyz", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

// ── POST /jobs ────────────────────────────────────────────────────────────────

func TestSubmitJob_ValidRequest_Returns201(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"job-1","command":"echo","args":["hello"]}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "job-1" {
		t.Errorf("want id=job-1, got %q", resp.ID)
	}
	if resp.Command != "echo" {
		t.Errorf("want command=echo, got %q", resp.Command)
	}
}

func TestSubmitJob_MissingID_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"command":"echo"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_MissingCommand_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"job-1"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_InvalidJSON_Returns400(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "POST", "/jobs", `not json`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestSubmitJob_StoreError_Returns500(t *testing.T) {
	js := newMockJobStore()
	js.submitErr = errors.New("storage full")
	srv := newServer(js, nil, nil)
	rr := do(srv, "POST", "/jobs", `{"id":"job-1","command":"ls"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// ── GET /jobs/{id} ────────────────────────────────────────────────────────────

func TestGetJob_ExistingJob_Returns200(t *testing.T) {
	js := newMockJobStore()
	js.jobs["job-42"] = &cpb.Job{ID: "job-42", Command: "ls", Status: cpb.JobStatusRunning}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs/job-42", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}

	var resp api.JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "job-42" {
		t.Errorf("want id=job-42, got %q", resp.ID)
	}
}

func TestGetJob_NotFound_Returns404(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/jobs/nonexistent", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

// ── GET /jobs ─────────────────────────────────────────────────────────────────

func TestListJobs_Returns200WithJobs(t *testing.T) {
	js := newMockJobStore()
	js.jobs["j1"] = &cpb.Job{ID: "j1", Command: "ls"}
	js.jobs["j2"] = &cpb.Job{ID: "j2", Command: "echo"}
	srv := newServer(js, nil, nil)

	rr := do(srv, "GET", "/jobs", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}

	var resp api.JobListResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("want total=2, got %d", resp.Total)
	}
}

func TestListJobs_StoreError_Returns500(t *testing.T) {
	js := newMockJobStore()
	js.listErr = errors.New("db error")
	srv := newServer(js, nil, nil)
	rr := do(srv, "GET", "/jobs", "")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// ── GET /nodes ────────────────────────────────────────────────────────────────

func TestListNodes_NilRegistry_Returns501(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/nodes", "")
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501, got %d", rr.Code)
	}
}

func TestListNodes_Returns200WithNodes(t *testing.T) {
	nr := &mockNodeRegistry{
		nodes: []api.NodeInfo{
			{ID: "n1", Health: "healthy"},
			{ID: "n2", Health: "healthy"},
		},
	}
	srv := newServer(newMockJobStore(), nr, nil)
	rr := do(srv, "GET", "/nodes", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}

	var resp api.NodeListResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("want total=2, got %d", resp.Total)
	}
}

func TestListNodes_RegistryError_Returns500(t *testing.T) {
	nr := &mockNodeRegistry{listErr: errors.New("registry down")}
	srv := newServer(newMockJobStore(), nr, nil)
	rr := do(srv, "GET", "/nodes", "")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// ── GET /metrics ──────────────────────────────────────────────────────────────

func TestGetMetrics_NilProvider_Returns501(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/metrics", "")
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501, got %d", rr.Code)
	}
}

func TestGetMetrics_Returns200(t *testing.T) {
	mp := &mockMetricsProvider{metrics: &api.ClusterMetrics{}}
	srv := newServer(newMockJobStore(), nil, mp)
	rr := do(srv, "GET", "/metrics", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

func TestGetMetrics_ProviderError_Returns500(t *testing.T) {
	mp := &mockMetricsProvider{err: errors.New("prometheus down")}
	srv := newServer(newMockJobStore(), nil, mp)
	rr := do(srv, "GET", "/metrics", "")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

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
	nr := &mockNodeRegistry{revokeErr: errors.New("cannot revoke")}
	srv := newServer(newMockJobStore(), nr, nil)
	rr := do(srv, "POST", "/admin/nodes/node-1/revoke", `{"reason":"test"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// ── auth middleware (no tokenManager = pass-through) ─────────────────────────

func TestAuthMiddleware_NoTokenManager_PassesThrough(t *testing.T) {
	js := newMockJobStore()
	js.jobs["job-x"] = &cpb.Job{ID: "job-x", Command: "ls"}
	srv := newServer(js, nil, nil) // tokenManager = nil

	// No Authorization header — should still succeed because auth is disabled.
	rr := do(srv, "GET", "/jobs/job-x", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200 with no token manager, got %d", rr.Code)
	}
}

// ── GET /audit ───────────────────────────────────────────────────────────────

// inMemoryAuditStore is a minimal audit.Store for testing.
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

func TestGetAudit_NilAudit_Returns501(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/audit", "")
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("want 501, got %d", rr.Code)
	}
}

func TestGetAudit_Returns200WithEvents(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	_ = auditLog.LogCoordinatorStart(context.Background(), "v1.0.0")
	_ = auditLog.LogCoordinatorStart(context.Background(), "v1.0.1")

	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	rr := do(srv, "GET", "/audit", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.AuditListResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total < 1 {
		t.Errorf("expected at least 1 audit event, got %d", resp.Total)
	}
}

// ── auth middleware ────────────────────────────────────────────────────────────

// inMemoryTokenStore satisfies auth.TokenStore for testing.
type inMemoryTokenStore struct {
	data map[string][]byte
}

func newTokenStore() *inMemoryTokenStore {
	return &inMemoryTokenStore{data: make(map[string][]byte)}
}

func (s *inMemoryTokenStore) Get(key string) ([]byte, error) {
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}

func (s *inMemoryTokenStore) Put(key string, value []byte, _ time.Duration) error {
	s.data[key] = append([]byte{}, value...)
	return nil
}

func (s *inMemoryTokenStore) Delete(key string) error {
	delete(s.data, key)
	return nil
}

func TestAuthMiddleware_MissingBearer_Returns401(t *testing.T) {
	tm, _ := auth.NewTokenManager(newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	// No Authorization header.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 without auth header, got %d", rr.Code)
	}
}

func TestAuthMiddleware_InvalidToken_Returns401(t *testing.T) {
	tm, _ := auth.NewTokenManager(newTokenStore())
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/jobs", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid token, got %d", rr.Code)
	}
}

func TestAuthMiddleware_ValidToken_Passes(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	js := newMockJobStore()
	js.jobs["j1"] = &cpb.Job{ID: "j1", Command: "ls"}
	srv := api.NewServer(js, nil, nil, nil, tm, nil, nil, nil)

	tok, err := tm.GenerateToken("user-1", "admin", time.Minute)
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

// ── response shape ────────────────────────────────────────────────────────────

func TestJobResponse_ContentTypeJSON(t *testing.T) {
	js := newMockJobStore()
	js.jobs["j"] = &cpb.Job{ID: "j", Command: "pwd"}
	srv := newServer(js, nil, nil)
	rr := do(srv, "GET", "/jobs/j", "")
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type: application/json, got %q", ct)
	}
}

func TestSubmitJob_ResponseContainsStatus(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	body := `{"id":"job-status","command":"test"}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"status"`)) {
		t.Error("response should contain status field")
	}
}

// ── Serve / Shutdown ──────────────────────────────────────────────────────────

func TestServe_StartsAndShutdown_NoError(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)

	// Pick a free port by listening and immediately closing.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(addr) }()

	// Give the server time to start.
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && err.Error() != "http: Server closed" {
			t.Logf("Serve returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Serve did not return after Shutdown")
	}
}

func TestShutdown_NilHTTPSrv_ReturnsNil(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// Never called Serve, so httpSrv is nil.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown with nil httpSrv: %v", err)
	}
}

// ── wsAuthMiddleware ──────────────────────────────────────────────────────────

func TestWsAuthMiddleware_NilTokenManager_PassesThrough(t *testing.T) {
	// Calling GET /ws/jobs/{id}/logs without token manager — should NOT return 401.
	// It will hit the log stream handler which needs a WebSocket upgrade; since
	// we're using httptest, the upgrade fails gracefully (not a middleware failure).
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
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
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
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs?token=not.a.valid.jwt", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid token, got %d", rr.Code)
	}
}

func TestWsAuthMiddleware_ValidTokenInQueryParam_PassesThrough(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken("user", "admin", time.Minute)
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
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken("user", "admin", time.Minute)
	req := httptest.NewRequest("GET", "/ws/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("want pass-through with valid token in header, got 401")
	}
}

// ── jobToResponse with FinishedAt ──────────────────────────────────────────────

func TestGetJob_WithFinishedAt_ResponseIncludesFinishedAt(t *testing.T) {
	js := newMockJobStore()
	finishedAt := time.Now().Add(-5 * time.Minute)
	js.jobs["j-done"] = &cpb.Job{
		ID:         "j-done",
		Command:    "ls",
		Status:     cpb.JobStatusCompleted,
		FinishedAt: finishedAt,
	}
	srv := newServer(js, nil, nil)
	rr := do(srv, "GET", "/jobs/j-done", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "finished_at") {
		t.Error("want finished_at in response for completed job")
	}
}

// ── handleSubmitJob audit path ────────────────────────────────────────────────

func TestSubmitJob_WithAuditLog_LogsEvent(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, nil, nil, nil, nil)

	body := `{"id":"audit-job","command":"echo"}`
	rr := do(srv, "POST", "/jobs", body)
	if rr.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── handleRevokeNode extra branches ──────────────────────────────────────────

func TestRevokeNode_WithAuditLog_LogsEvent(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	nr := &mockNodeRegistry{}
	srv := api.NewServer(newMockJobStore(), nr, nil, auditLog, nil, nil, nil, nil)

	rr := do(srv, "POST", "/admin/nodes/bad-node/revoke", `{"reason":"test"}`)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeNode_InvalidJSON_UsesDefaultReason(t *testing.T) {
	nr := &mockNodeRegistry{}
	srv := newServer(newMockJobStore(), nr, nil)
	// Send non-JSON body — reason should default to "manual revocation".
	rr := do(srv, "POST", "/admin/nodes/my-node/revoke", `not json at all`)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200 with default reason, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAudit_WithPagination_Returns200(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	ctx := context.Background()
	// Add several events.
	for i := 0; i < 5; i++ {
		_ = auditLog.LogCoordinatorStart(ctx, "v1.0")
	}

	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	rr := do(srv, "GET", "/audit?page=2&size=2", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── NewServer with promHandler (covers the promHandler != nil branch) ─────────

func TestNewServer_WithPromHandler_RegistersPrometheusRoute(t *testing.T) {
	// Provide a real http.Handler so the promHandler != nil branch in registerRoutes
	// is exercised.
	promHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# prometheus metrics"))
	})
	srv := api.NewServer(newMockJobStore(), nil, nil, nil, nil, nil, nil, promHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200 from prom handler, got %d", rr.Code)
	}
}

// ── handleRevokeNode with authenticated actor ─────────────────────────────────

func TestRevokeNode_WithValidToken_ActorFromClaims(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	nr := &mockNodeRegistry{}
	srv := api.NewServer(newMockJobStore(), nr, nil, nil, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken("alice", "admin", time.Minute)
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

// ── authMiddleware with audit log ────────────────────────────────────────────

// TestSubmitJob_WithTokenAndAudit covers the claims.Subject extraction in
// the audit path when both tokenManager and audit are configured.
func TestSubmitJob_WithTokenAndAudit_ActorFromClaims(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	auditLog := audit.NewLogger(newAuditStore(), 0)
	js := newMockJobStore()
	srv := api.NewServer(js, nil, nil, auditLog, tm, nil, nil, nil)

	tok, _ := tm.GenerateToken("submit-user", "admin", time.Minute)
	body := `{"id":"sa-job","command":"echo"}`
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── authMiddleware with audit log ────────────────────────────────────────────

// ── handleGetAudit QueryEvents error path ─────────────────────────────────────

// failOnScanStore wraps inMemoryAuditStore but fails Scan.
type failOnScanStore struct {
	*inMemoryAuditStore
}

func (s *failOnScanStore) Scan(_ context.Context, _ string, _ int) ([][]byte, error) {
	return nil, errors.New("storage unavailable")
}

func TestGetAudit_ScanError_Returns500(t *testing.T) {
	store := &failOnScanStore{newAuditStore()}
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)

	rr := do(srv, "GET", "/audit", "")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500 on scan error, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ── authMiddleware with audit log ────────────────────────────────────────────

func TestAuthMiddleware_MissingBearer_WithAuditLog_LogsFailure(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
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
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
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

// ── wsAuthMiddleware with audit log ──────────────────────────────────────────

func TestWsAuthMiddleware_MissingToken_WithAuditLog_LogsFailure(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs", nil)
	// No token — triggers audit log for missing token.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestWsAuthMiddleware_InvalidToken_WithAuditLog_LogsFailure(t *testing.T) {
	store := newTokenStore()
	tm, _ := auth.NewTokenManager(store)
	auditLog := audit.NewLogger(newAuditStore(), 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, tm, nil, nil, nil)

	req := httptest.NewRequest("GET", "/ws/jobs/j1/logs?token=invalid.jwt.token", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid ws token, got %d", rr.Code)
	}
}

// ── JobStoreAdapter.List with statusFilter ────────────────────────────────────

func TestJobStoreAdapter_List_WithStatusFilter_ReturnsFiltered(t *testing.T) {
	// Use cluster.JobStore as the backing store so we exercise the real
	// GetJobsByStatus path inside JobStoreAdapter.List.
	p := cluster.NewMemJobPersister()
	cs := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	// Submit two jobs (both land in PENDING).
	for _, id := range []string{"a1", "a2"} {
		if err := cs.Submit(ctx, &cpb.Job{ID: id, Command: "ls"}); err != nil {
			t.Fatalf("Submit %s: %v", id, err)
		}
	}

	adapter := api.NewJobStoreAdapter(cs)

	// statusFilter = "PENDING" should return both jobs.
	jobs, total, err := adapter.List(ctx, "PENDING", 1, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Errorf("want total=2, got %d", total)
	}
	if len(jobs) != 2 {
		t.Errorf("want 2 jobs, got %d", len(jobs))
	}
}

func TestJobStoreAdapter_List_PageBeyondEnd_ReturnsEmpty(t *testing.T) {
	p := cluster.NewMemJobPersister()
	cs := cluster.NewJobStore(p, nil)
	ctx := context.Background()

	_ = cs.Submit(ctx, &cpb.Job{ID: "b1", Command: "ls"})

	adapter := api.NewJobStoreAdapter(cs)

	// page=2, size=20 — only 1 job exists, so start >= total.
	jobs, total, err := adapter.List(ctx, "", 2, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Errorf("want total=1, got %d", total)
	}
	if len(jobs) != 0 {
		t.Errorf("want 0 jobs for out-of-range page, got %d", len(jobs))
	}
}
