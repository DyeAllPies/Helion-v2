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

func TestListServices_EmptyRegistry(t *testing.T) {
	srv, _ := newServiceServer(t)
	rr := do(srv, "GET", "/api/services", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.ServiceListResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 || len(resp.Services) != 0 {
		t.Fatalf("expected empty list, got %+v", resp)
	}
}

func TestListServices_ReturnsAllEndpoints(t *testing.T) {
	srv, sr := newServiceServer(t)
	sr.Upsert(cpb.ServiceEndpoint{
		JobID: "svc-a", NodeID: "n1", NodeAddress: "10.0.0.1:9090",
		Port: 8080, HealthPath: "/h", Ready: true, UpdatedAt: time.Now(),
	})
	sr.Upsert(cpb.ServiceEndpoint{
		JobID: "svc-b", NodeID: "n2", NodeAddress: "10.0.0.2:9090",
		Port: 9000, HealthPath: "/", Ready: false, UpdatedAt: time.Now(),
	})
	rr := do(srv, "GET", "/api/services", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp api.ServiceListResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Total != 2 || len(resp.Services) != 2 {
		t.Fatalf("expected 2 services, got %+v", resp)
	}
}

func TestListServices_NotFoundWhenRegistryUnwired(t *testing.T) {
	srv := newServer(newMockJobStore(), nil, nil)
	rr := do(srv, "GET", "/api/services", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestGetService_IPv6NodeAddress_WrapsBrackets pins the buildUpstreamURL
// IPv6 branch — NodeAddress like "[::1]:9090" must render as
// "http://[::1]:<port><path>", not "http://::1:<port><path>" which
// every HTTP client rejects as malformed. The IPv4 happy path is
// covered by TestGetService_LiveEndpoint_Returns200; without this
// companion a regression dropping the `net.ParseIP(...).To4() == nil`
// bracket-wrap would silently break every IPv6-addressed deployment.
func TestGetService_IPv6NodeAddress_WrapsBrackets(t *testing.T) {
	srv, sr := newServiceServer(t)
	sr.Upsert(cpb.ServiceEndpoint{
		JobID:       "inf-v6",
		NodeID:      "node-v6",
		NodeAddress: "[::1]:9090",
		Port:        8080,
		HealthPath:  "/healthz",
		Ready:       true,
		UpdatedAt:   time.Now(),
	})

	rr := do(srv, "GET", "/api/services/inf-v6", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.ServiceEndpointResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	want := "http://[::1]:8080/healthz"
	if resp.UpstreamURL != want {
		t.Errorf("UpstreamURL = %q, want %q", resp.UpstreamURL, want)
	}
}
