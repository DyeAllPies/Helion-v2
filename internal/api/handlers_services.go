// internal/api/handlers_services.go
//
// Feature 17 — read-only lookup for live inference-service endpoints.
//
// GET /api/services/{job_id}
//   200 → ServiceEndpointResponse with upstream URL
//   404 → no such live service (either the job isn't a service, the
//         service hasn't reached its first ready state yet, or the
//         job has terminated and its entry was reaped)
//
// Writes happen on the gRPC side (ReportServiceEvent populates the
// cluster.ServiceRegistry). The HTTP handler is purely read-only so
// an attacker who compromises the HTTP surface cannot forge
// upstream URLs.

package api

import (
	"fmt"
	"net"
	"net/http"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// SetServiceRegistry enables the /api/services/{job_id} endpoint by
// injecting the coordinator's ServiceRegistry. Nil (or unset) means
// this deployment didn't opt into inference services; the handler
// returns 404 so the endpoint is invisible.
func (s *Server) SetServiceRegistry(sr *cluster.ServiceRegistry) {
	s.services = sr
	s.mux.HandleFunc("GET /api/services/{job_id}", s.authMiddleware(s.handleGetService))
}

// ServiceEndpointResponse is the JSON shape returned by
// GET /api/services/{job_id}. UpstreamURL is the canonical "what
// should I hit" field; NodeAddress + Port are surfaced separately for
// callers that need to build a non-HTTP connection (e.g. gRPC over
// raw TCP to the same port).
type ServiceEndpointResponse struct {
	JobID       string `json:"job_id"`
	NodeID      string `json:"node_id"`
	NodeAddress string `json:"node_address"`
	Port        uint32 `json:"port"`
	HealthPath  string `json:"health_path"`
	Ready       bool   `json:"ready"`
	UpstreamURL string `json:"upstream_url"`
	UpdatedAt   string `json:"updated_at"`
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	if s.services == nil {
		writeError(w, http.StatusNotFound, "service registry is not configured on this coordinator")
		return
	}
	jobID := r.PathValue("job_id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "job_id is required")
		return
	}
	ep, ok := s.services.Get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "no live service for this job")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, "handleGetService", toServiceEndpointResponse(ep))
}

func toServiceEndpointResponse(ep cpb.ServiceEndpoint) ServiceEndpointResponse {
	return ServiceEndpointResponse{
		JobID:       ep.JobID,
		NodeID:      ep.NodeID,
		NodeAddress: ep.NodeAddress,
		Port:        ep.Port,
		HealthPath:  ep.HealthPath,
		Ready:       ep.Ready,
		UpstreamURL: buildUpstreamURL(ep),
		UpdatedAt:   ep.UpdatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

// buildUpstreamURL stitches NodeAddress + Port into the canonical
// http:// URL. NodeAddress is the "host:port" the node registered at;
// we keep its host component and substitute the service port.
// Uses net.SplitHostPort so IPv6 literals (`[::1]:9090`) are handled
// correctly. On parse failure we fall back to the raw NodeAddress
// as a host — the lookup will likely 404 at the caller, but that is
// better than emitting a URL the caller cannot parse either.
func buildUpstreamURL(ep cpb.ServiceEndpoint) string {
	host, _, err := net.SplitHostPort(ep.NodeAddress)
	if err != nil || host == "" {
		host = ep.NodeAddress
	}
	// Re-wrap IPv6 literals in brackets when composing the final URL.
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return fmt.Sprintf("http://[%s]:%d%s", host, ep.Port, ep.HealthPath)
	}
	return fmt.Sprintf("http://%s:%d%s", host, ep.Port, ep.HealthPath)
}
