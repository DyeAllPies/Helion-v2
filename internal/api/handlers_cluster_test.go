// internal/api/handlers_cluster_test.go
//
// Tests for GET /healthz, /readyz, /nodes, /metrics, /audit.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
)

// ── GET /healthz ──────────────────────────────────────────────────────────────

func TestHealthz_Returns200(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/healthz", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

// ── GET /readyz ───────────────────────────────────────────────────────────────

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

func TestNewServer_WithPromHandler_RegistersPrometheusRoute(t *testing.T) {
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

// ── GET /audit ────────────────────────────────────────────────────────────────

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
	srv.DisableAuth()
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

func TestGetAudit_WithPagination_Returns200(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = auditLog.LogCoordinatorStart(ctx, "v1.0")
	}

	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()
	rr := do(srv, "GET", "/audit?page=2&size=2", "")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAudit_ScanError_Returns500(t *testing.T) {
	store := &failOnScanStore{newAuditStore()}
	auditLog := audit.NewLogger(store, 0)
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit", "")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500 on scan error, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAudit_InvalidPage_Returns400(t *testing.T) {
	store := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(store, 0), nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?page=bad", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestGetAudit_ZeroPage_Returns400(t *testing.T) {
	store := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(store, 0), nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?page=0", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestGetAudit_InvalidSize_Returns400(t *testing.T) {
	store := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(store, 0), nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?size=bad", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestGetAudit_SizeZero_Returns400(t *testing.T) {
	store := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(store, 0), nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?size=0", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestGetAudit_SizeTooLarge_Returns400(t *testing.T) {
	store := newAuditStore()
	srv := api.NewServer(newMockJobStore(), nil, nil, audit.NewLogger(store, 0), nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?size=101", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestGetAudit_PageBeyondResults_ReturnsEmpty(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	_ = auditLog.LogCoordinatorStart(context.Background(), "v1.0.0")
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?page=2&size=50", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}

	var resp api.AuditListResponse
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if len(resp.Events) != 0 {
		t.Errorf("want empty events for page beyond results, got %d", len(resp.Events))
	}
}

func TestGetAudit_TypeFilter_PassedToQuery(t *testing.T) {
	store := newAuditStore()
	auditLog := audit.NewLogger(store, 0)
	_ = auditLog.LogCoordinatorStart(context.Background(), "v1.0.0")
	srv := api.NewServer(newMockJobStore(), nil, nil, auditLog, nil, nil, nil, nil)
	srv.DisableAuth()

	rr := do(srv, "GET", "/audit?type=coordinator_start", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}
