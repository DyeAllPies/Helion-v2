// internal/cluster/registry_node.go
//
// nodeEntry is the Registry's in-memory record for one registered node.
// All hot-path mutations use sync/atomic so heartbeat handling can proceed
// without holding Registry.mu.

package cluster

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// nodeIDPattern accepts 1–63 characters: alphanumeric plus hyphens, must start
// with an alphanumeric character. This blocks path traversal sequences, control
// characters, log-injection payloads, and other unsafe inputs.
//
// AUDIT L3 (fixed): previously there was no input validation on node IDs, so
// an attacker could register a node with an ID containing shell metacharacters,
// path separators, or log-injection sequences. validateNodeID is called at the
// top of Register before any storage or processing occurs.
var nodeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-]{0,62}$`)

// validateNodeID returns an error if id does not match nodeIDPattern.
func validateNodeID(id string) error {
	if !nodeIDPattern.MatchString(id) {
		return fmt.Errorf("node id %q is invalid: must match ^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$", id)
	}
	return nil
}

// Node-label bounds mirror the job-side node_selector bounds in
// internal/api/handlers_jobs.go (maxNodeSelectorEntries, maxNodeSelectorKeyLen,
// maxNodeSelectorValLen). Duplicated as package-local constants so the
// cluster package does not import the api package.
const (
	maxNodeLabelEntries = 32
	maxNodeLabelKeyLen  = 63
	maxNodeLabelValLen  = 253
)

// sanitiseNodeLabels drops label entries whose key or value would not
// round-trip safely through the scheduler's node_selector comparison
// (NUL / control bytes, oversize, empty keys, keys containing '=').
// Returns nil when src is nil or every entry was dropped so consumers
// can do the idiomatic len(labels) == 0 check without a nil guard.
// A dropped entry never aborts registration — the rest of the map is
// accepted; the node remains addressable.
func sanitiseNodeLabels(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	if len(src) > maxNodeLabelEntries {
		// Cap the map; pick an arbitrary stable subset. This is
		// defensive against a compromised / misconfigured node
		// reporting a huge label set — the k8s-compatible cap of 32
		// is comfortably above real-world need.
		src = truncateLabelMap(src, maxNodeLabelEntries)
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		if !validLabelKey(k) || !validLabelValue(v) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validLabelKey(k string) bool {
	if k == "" || len(k) > maxNodeLabelKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		b := k[i]
		if b == '=' || b == 0 || b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

func validLabelValue(v string) bool {
	if len(v) > maxNodeLabelValLen {
		return false
	}
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b == 0 || b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

// truncateLabelMap returns a copy of m with at most n entries. Map
// iteration order is unspecified; the subset is stable only in the
// sense that once built, the entry count is bounded.
func truncateLabelMap(m map[string]string, n int) map[string]string {
	out := make(map[string]string, n)
	for k, v := range m {
		if len(out) >= n {
			break
		}
		out[k] = v
	}
	return out
}

// formatLabelsForAudit renders a label map as a stable "k1=v1,k2=v2"
// string sorted by key. Used in the node.registered audit detail so
// operators can grep for "labels=...gpu=a100..." in the audit stream.
// Sorting keeps the output deterministic across runs — important for
// test assertions and for post-hoc diffing of historical records.
// Returns "{}" for empty maps so the shape is visually distinct
// from "labels=" (which could also mean "no labels key in detail").
func formatLabelsForAudit(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
	}
	b.WriteByte('}')
	return b.String()
}

// nodeEntry is the in-memory record for one registered node.
// Fields updated on every heartbeat are plain int32/int64 values accessed via
// the sync/atomic package — no mutex needed for those updates.
// address is stored as atomic.Value because Register() may update it
// concurrently with snapshot() calls from persist goroutines.
type nodeEntry struct {
	// Immutable after insertion into the map.
	nodeID       string
	registeredAt time.Time

	// labels are reported at Register time and kept for the scheduler's
	// node_selector filter. Frozen after insertion — the Register path
	// reads, merges, and re-inserts the entry atomically under
	// Registry.mu, so snapshot() can read labels without locking.
	labels map[string]string

	// address may be updated by Register() on node restart.
	// Stored as atomic.Value to avoid races with concurrent snapshot() calls.
	_address atomic.Value // holds string

	// Mutable; written atomically by HandleHeartbeat.
	_lastSeenNano int64 // Unix nanoseconds; 0 means "never seen"
	_runningJobs  int32

	// Resource capacity — written atomically by HandleHeartbeat.
	_cpuMillicores   uint32
	_totalMemBytes   uint64
	_maxSlots        uint32
	_totalGpus       uint32
}

func (e *nodeEntry) storeAddress(addr string) {
	e._address.Store(addr)
}

func (e *nodeEntry) loadAddress() string {
	v := e._address.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

func (e *nodeEntry) storeLastSeen(t time.Time) {
	atomic.StoreInt64(&e._lastSeenNano, t.UnixNano())
}

func (e *nodeEntry) loadLastSeen() time.Time {
	ns := atomic.LoadInt64(&e._lastSeenNano)
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (e *nodeEntry) storeRunning(n int32) {
	atomic.StoreInt32(&e._runningJobs, n)
}

func (e *nodeEntry) loadRunning() int32 {
	return atomic.LoadInt32(&e._runningJobs)
}

func (e *nodeEntry) storeCapacity(cpuMilli uint32, memBytes uint64, slots uint32, gpus uint32) {
	atomic.StoreUint32(&e._cpuMillicores, cpuMilli)
	atomic.StoreUint64(&e._totalMemBytes, memBytes)
	atomic.StoreUint32(&e._maxSlots, slots)
	atomic.StoreUint32(&e._totalGpus, gpus)
}

func (e *nodeEntry) loadCpuMillicores() uint32  { return atomic.LoadUint32(&e._cpuMillicores) }
func (e *nodeEntry) loadTotalMemBytes() uint64  { return atomic.LoadUint64(&e._totalMemBytes) }
func (e *nodeEntry) loadMaxSlots() uint32       { return atomic.LoadUint32(&e._maxSlots) }
func (e *nodeEntry) loadTotalGpus() uint32      { return atomic.LoadUint32(&e._totalGpus) }

func (e *nodeEntry) isHealthy(staleAfter time.Duration) bool {
	ls := e.loadLastSeen()
	return !ls.IsZero() && time.Since(ls) < staleAfter
}

func (e *nodeEntry) snapshot(staleAfter time.Duration) *cpb.Node {
	// Defensive copy: snapshot consumers (scheduler policies, dashboard
	// JSON) must not be able to mutate the entry's label map.
	var labels map[string]string
	if len(e.labels) > 0 {
		labels = make(map[string]string, len(e.labels))
		for k, v := range e.labels {
			labels[k] = v
		}
	}
	return &cpb.Node{
		NodeID:        e.nodeID,
		Address:       e.loadAddress(),
		Healthy:       e.isHealthy(staleAfter),
		LastSeen:      e.loadLastSeen(),
		RunningJobs:   e.loadRunning(),
		RegisteredAt:  e.registeredAt,
		CpuMillicores: e.loadCpuMillicores(),
		TotalMemBytes: e.loadTotalMemBytes(),
		MaxSlots:      e.loadMaxSlots(),
		TotalGpus:     e.loadTotalGpus(),
		Labels:        labels,
	}
}
