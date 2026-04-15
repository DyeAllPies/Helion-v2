package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func newServiceServer(t *testing.T) (*api.Server, *cluster.ServiceRegistry) {
	t.Helper()
	srv := newServer(newMockJobStore(), nil, nil)
	sr := cluster.NewServiceRegistry()
	srv.SetServiceRegistry(sr)
	return srv, sr
}

func TestGetService_LiveEndpoint_Returns200(t *testing.T) {
	srv, sr := newServiceServer(t)
	sr.Upsert(cpb.ServiceEndpoint{
		JobID:       "inf-1",
		NodeID:      "node-a",
		NodeAddress: "10.0.0.4:9090",
		Port:        8080,
		HealthPath:  "/healthz",
		Ready:       true,
		UpdatedAt:   time.Now(),
	})

	rr := do(srv, "GET", "/api/services/inf-1", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.ServiceEndpointResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.UpstreamURL != "http://10.0.0.4:8080/healthz" {
		t.Errorf("UpstreamURL = %q, want http://10.0.0.4:8080/healthz", resp.UpstreamURL)
	}
	if !resp.Ready {
		t.Error("Ready should be true")
	}
}

func TestGetService_NotFound(t *testing.T) {
	srv, _ := newServiceServer(t)
	rr := do(srv, "GET", "/api/services/never", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

func TestGetService_NotFoundWhenRegistryUnwired(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	// Intentionally do NOT call SetServiceRegistry.
	rr := do(srv, "GET", "/api/services/anything", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}
