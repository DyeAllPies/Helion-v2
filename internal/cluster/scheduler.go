// internal/cluster/scheduler.go
//
// Scheduler selects a worker node for each incoming job dispatch request.
//
// Design
// ──────
// The scheduler is deliberately thin.  It has exactly one job: given a list of
// healthy nodes, pick one.  Everything else — fetching the healthy node list,
// persisting the job record, sending the dispatch RPC — is the job lifecycle's
// responsibility (job.go, Phase 2 step 4).
//
// This separation means the scheduler is fully testable with a hand-constructed
// []*cpb.Node slice.  No registry, no database, no gRPC required.
//
// v1 race fix: lastIndex
// ──────────────────────
// In v1 the round-robin index was a plain int field on the coordinator struct,
// written while only holding RLock().  That is a data race: RLock permits
// concurrent reads but not concurrent writes.
//
// Fix: the index is now an atomic.Int64 on the RoundRobinPolicy struct.
// Reads and writes are linearisable without any mutex.
//
// Policy interface
// ────────────────
// New policies (priority queue, resource-aware, etc.) implement Policy and
// are wired in at construction time.  The scheduler never switches policy
// mid-run; the coordinator's main() chooses a policy from the
// HELION_SCHEDULER environment variable.

package cluster

import (
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Policy interface ──────────────────────────────────────────────────────────

// NodeSource is the narrow interface the Scheduler uses to obtain the current
// list of healthy nodes.  The Registry satisfies this interface; tests inject
// a simple stub.
type NodeSource interface {
	HealthyNodes() []*cpb.Node
}

// Policy selects a single node from a non-empty slice of healthy candidates.
//
// Implementations must be safe for concurrent use: Pick may be called from
// multiple goroutines simultaneously.
//
// Pick receives only healthy nodes (Healthy == true, RunningJobs reflects the
// current load).  It must never return nil when nodes is non-empty.  It may
// return nil only when nodes is empty, which the Scheduler translates into
// ErrNoHealthyNodes.
type Policy interface {
	// Name returns a human-readable identifier used in logs and metrics.
	Name() string

	// Pick selects one node from nodes.  nodes is guaranteed non-nil; may be
	// empty (in which case the Scheduler does not call Pick at all).
	Pick(nodes []*cpb.Node) *cpb.Node
}

// ── Scheduler ────────────────────────────────────────────────────────────────

// Scheduler wraps a NodeSource and a Policy to select a dispatch target.
type Scheduler struct {
	source NodeSource
	policy Policy
}

// NewScheduler creates a Scheduler.
//
// source is typically *Registry.
// policy is one of RoundRobinPolicy or LeastLoadedPolicy, or a custom
// implementation for tests.
func NewScheduler(source NodeSource, policy Policy) *Scheduler {
	return &Scheduler{source: source, policy: policy}
}

// Pick returns the next node to dispatch a job to.
//
// It fetches the current healthy node list from the NodeSource and delegates
// the selection to the configured Policy.
//
// Returns ErrNoHealthyNodes if the cluster has no healthy nodes at the moment
// of the call.  The caller (job lifecycle) should surface this as a retriable
// error — nodes may recover between attempts.
func (s *Scheduler) Pick() (*cpb.Node, error) {
	nodes := s.source.HealthyNodes()
	if len(nodes) == 0 {
		return nil, ErrNoHealthyNodes
	}
	chosen := s.policy.Pick(nodes)
	if chosen == nil {
		// Defensive: a correctly implemented Policy never returns nil for a
		// non-empty slice, but guard against buggy implementations.
		return nil, ErrNoHealthyNodes
	}
	return chosen, nil
}

// PolicyName returns the name of the active scheduling policy.
func (s *Scheduler) PolicyName() string {
	return s.policy.Name()
}
