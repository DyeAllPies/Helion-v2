// internal/cluster/service_registry.go
//
// Feature 17 — coordinator-side registry of live inference-service
// endpoints. Populated by grpcserver.ReportServiceEvent; read by the
// HTTP handler at GET /api/services/{id}.
//
// In-memory only. A coordinator restart starts with an empty map;
// the nodes' probers re-emit the current state on the next probe
// tick (ready flips are edge-triggered, but the probers emit their
// first state once the post-restart RPC succeeds, so recovery is
// self-healing within one probe cycle).
//
// Not persisted because:
//   - Entries are live-state signals, not historical records. A
//     stale entry from a previous coordinator process would point
//     at a node:port that may no longer exist.
//   - Rehydrating the mapping from BadgerDB would require walking
//     every running job + asking every node to re-probe, which is
//     more code than just waiting 5 s for the next probe tick.
//   - The registry is coordinator-local; an HA setup with multiple
//     coordinators would want a fan-out anyway.

package cluster

import (
	"sync"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ServiceRegistry holds the current readiness map. Safe for
// concurrent use.
type ServiceRegistry struct {
	mu        sync.RWMutex
	endpoints map[string]cpb.ServiceEndpoint // keyed by jobID
}

// NewServiceRegistry returns an empty registry.
func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{endpoints: make(map[string]cpb.ServiceEndpoint)}
}

// Upsert records the latest readiness state for a service job.
// Intended to be called from grpcserver.ReportServiceEvent.
func (r *ServiceRegistry) Upsert(e cpb.ServiceEndpoint) {
	if e.JobID == "" {
		return
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now()
	}
	r.mu.Lock()
	r.endpoints[e.JobID] = e
	r.mu.Unlock()
}

// Get returns the endpoint recorded for jobID, or (_, false) if no
// service has ever reported ready for this job. A non-ready entry
// is still returned — callers that need the "is currently ready"
// signal should check ep.Ready.
func (r *ServiceRegistry) Get(jobID string) (cpb.ServiceEndpoint, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ep, ok := r.endpoints[jobID]
	return ep, ok
}

// Delete removes the entry for jobID. Called when the underlying
// service job reaches a terminal state (completed/failed/cancelled)
// so stale entries don't linger for clients.
func (r *ServiceRegistry) Delete(jobID string) {
	r.mu.Lock()
	delete(r.endpoints, jobID)
	r.mu.Unlock()
}

// Count returns the number of tracked services. Backs the
// helion_services_total Prometheus gauge.
func (r *ServiceRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.endpoints)
}

// Snapshot returns a defensive copy of every currently tracked
// endpoint. Backs GET /api/services (list). Returns an empty slice
// rather than nil when the registry is empty so the JSON response
// shape is stable (`{"services":[]}` not `{"services":null}`).
func (r *ServiceRegistry) Snapshot() []cpb.ServiceEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]cpb.ServiceEndpoint, 0, len(r.endpoints))
	for _, ep := range r.endpoints {
		out = append(out, ep)
	}
	return out
}
