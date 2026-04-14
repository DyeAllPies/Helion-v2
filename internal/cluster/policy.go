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
	"sort"
	"strings"
	"sync"
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

// noNodeMatchesSelectorError is returned when healthy nodes exist but
// none of them satisfy a job's node_selector. Distinct from
// ErrNoHealthyNodes because the dispatch loop surfaces it as a
// terminal "unschedulable" path rather than a retriable backoff —
// retrying won't invent labels on the existing nodes.
type noNodeMatchesSelectorError struct{}

func (noNodeMatchesSelectorError) Error() string {
	return "scheduler: no healthy node satisfies the job's node_selector"
}

// ErrNoNodeMatchesSelector is the sentinel callers should match with
// errors.Is() to detect an unsatisfied node_selector at dispatch time.
var ErrNoNodeMatchesSelector error = noNodeMatchesSelectorError{}

// ── Round-robin ───────────────────────────────────────────────────────────────

// RoundRobinPolicy distributes jobs evenly across healthy nodes in rotation.
//
// Each distinct candidate *set* (identified by the sorted node-ID list)
// has its own counter, so when the scheduler's filter layers narrow the
// healthy pool to a selector-specific or GPU-specific subset, rotation
// within that subset is fair. A single global counter would be biased
// toward whichever index the shared counter happens to land on when a
// given subset is asked.
//
// Counters live in a sync.Map keyed by the candidate-set signature; the
// memory profile is O(distinct candidate sets seen during the process
// lifetime) which in practice is bounded by the number of distinct
// label-selector + GPU-count combinations job submissions exercise.
type RoundRobinPolicy struct {
	counters sync.Map // map[string]*atomic.Int64 — key = sortedNodeIDs
}

// NewRoundRobinPolicy returns a ready-to-use RoundRobinPolicy.
func NewRoundRobinPolicy() *RoundRobinPolicy {
	return &RoundRobinPolicy{}
}

func (p *RoundRobinPolicy) Name() string { return "round-robin" }

// Pick returns the next node in rotation within the candidate set.
//
// Each distinct candidate set gets its own counter, so selector-filtered
// or GPU-filtered subsets rotate fairly among their own members. The
// counter increments unconditionally; the modulo produces a valid index
// into whatever-length slice was passed. If the slice shrinks between
// calls the index wraps cleanly.
func (p *RoundRobinPolicy) Pick(nodes []*cpb.Node) *cpb.Node {
	if len(nodes) == 0 {
		return nil
	}
	counter := p.counterFor(nodes)
	idx := int(counter.Add(1)-1) % len(nodes)
	return nodes[idx]
}

// counterFor returns the counter associated with this exact candidate
// set. The key is a sorted, separator-joined list of node IDs so two
// calls with the same set (in any order) share a counter. A rebind of
// the policy to a different backing Registry produces the same keys
// for the same node IDs — intentional, so a restart doesn't reset
// rotation unnecessarily.
func (p *RoundRobinPolicy) counterFor(nodes []*cpb.Node) *atomic.Int64 {
	key := candidateSetKey(nodes)
	if v, ok := p.counters.Load(key); ok {
		return v.(*atomic.Int64)
	}
	fresh := &atomic.Int64{}
	actual, _ := p.counters.LoadOrStore(key, fresh)
	return actual.(*atomic.Int64)
}

// candidateSetKey renders a stable identifier for a node set: sorted
// node IDs joined with '|'. Fast enough for the handful of nodes a
// typical Helion cluster has; repeated Pick calls on the same set
// re-hit the same sync.Map slot.
func candidateSetKey(nodes []*cpb.Node) string {
	if len(nodes) == 1 {
		return nodes[0].NodeID
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.NodeID)
	}
	sort.Strings(ids)
	return strings.Join(ids, "|")
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
	switch os.Getenv("HELION_SCHEDULER") {
	case "least":
		return NewLeastLoadedPolicy()
	case "resource-aware":
		return NewResourceAwarePolicy()
	default:
		return NewRoundRobinPolicy()
	}
}
