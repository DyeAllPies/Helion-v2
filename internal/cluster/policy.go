// internal/cluster/policy.go
//
// Concrete Policy implementations.
//
// RoundRobinPolicy
// ────────────────
// Cycles through the healthy node list in order.  The index advances with
// atomic.Int64.Add so concurrent calls from different goroutines each get a
// distinct node without any mutex.
//
// Important subtlety: the healthy node list is a fresh snapshot on every call
// to Pick(), so its length can vary between calls.  The modulo operation
// always produces a valid index regardless of how the list grows or shrinks.
//
// LeastLoadedPolicy
// ─────────────────
// Picks the node with the fewest running jobs.  Ties are broken by the node's
// position in the slice (effectively first-registered wins).  This is O(n)
// over the healthy node list; with the node counts expected in Helion (tens,
// not thousands) this is negligible.
//
// PolicyFromEnv
// ─────────────
// A convenience constructor that reads HELION_SCHEDULER and returns the
// corresponding policy.  "least" → LeastLoadedPolicy, anything else (including
// empty string) → RoundRobinPolicy.  Used in coordinator main().

package cluster

import (
	"os"
	"sync/atomic"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrNoHealthyNodes is returned by Scheduler.Pick when the cluster has no
// healthy nodes available to accept work.
type noHealthyNodesError struct{}

func (noHealthyNodesError) Error() string { return "scheduler: no healthy nodes available" }

// ErrNoHealthyNodes is the sentinel callers should match with errors.Is().
var ErrNoHealthyNodes error = noHealthyNodesError{}

// ── Round-robin ───────────────────────────────────────────────────────────────

// RoundRobinPolicy distributes jobs evenly across healthy nodes in rotation.
//
// The counter is an atomic.Int64 so concurrent Pick() calls do not race.
// This directly fixes the v1 bug where lastIndex was incremented under RLock,
// which is undefined behaviour (RLock permits concurrent reads, not writes).
type RoundRobinPolicy struct {
	counter atomic.Int64
}

// NewRoundRobinPolicy returns a ready-to-use RoundRobinPolicy.
func NewRoundRobinPolicy() *RoundRobinPolicy {
	return &RoundRobinPolicy{}
}

func (p *RoundRobinPolicy) Name() string { return "round-robin" }

// Pick returns the next node in rotation.
//
// The counter increments unconditionally; the modulo produces a valid index
// into whatever-length slice was passed.  If the slice shrinks between calls
// the index wraps cleanly.
func (p *RoundRobinPolicy) Pick(nodes []*cpb.Node) *cpb.Node {
	if len(nodes) == 0 {
		return nil
	}
	// Add(1) returns the new value; subtract 1 to get a zero-based index.
	idx := int(p.counter.Add(1)-1) % len(nodes)
	return nodes[idx]
}

// ── Least-loaded ──────────────────────────────────────────────────────────────

// LeastLoadedPolicy picks the healthy node with the fewest running jobs.
//
// It is stateless — each call to Pick() scans the provided node list.
// No locking is needed because the method reads only its argument, which is
// a snapshot owned by the caller.
type LeastLoadedPolicy struct{}

// NewLeastLoadedPolicy returns a ready-to-use LeastLoadedPolicy.
func NewLeastLoadedPolicy() *LeastLoadedPolicy {
	return &LeastLoadedPolicy{}
}

func (p *LeastLoadedPolicy) Name() string { return "least-loaded" }

// Pick returns the node with the fewest running jobs.
// Ties are broken by order in the slice (earlier = preferred).
func (p *LeastLoadedPolicy) Pick(nodes []*cpb.Node) *cpb.Node {
	if len(nodes) == 0 {
		return nil
	}
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.RunningJobs < best.RunningJobs {
			best = n
		}
	}
	return best
}

// ── Policy factory ────────────────────────────────────────────────────────────

// PolicyFromEnv returns the scheduling policy named by the HELION_SCHEDULER
// environment variable.
//
//	"least"        → LeastLoadedPolicy
//	anything else  → RoundRobinPolicy  (safe default)
//
// Used by the coordinator's main() so the policy can be changed without
// recompiling:
//
//	HELION_SCHEDULER=least ./helion-coordinator
func PolicyFromEnv() Policy {
	if os.Getenv("HELION_SCHEDULER") == "least" {
		return NewLeastLoadedPolicy()
	}
	return NewRoundRobinPolicy()
}
