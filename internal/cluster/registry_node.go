// internal/cluster/registry_node.go
//
// nodeEntry is the Registry's in-memory record for one registered node.
// All hot-path mutations use sync/atomic so heartbeat handling can proceed
// without holding Registry.mu.

package cluster

import (
	"fmt"
	"regexp"
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

// nodeEntry is the in-memory record for one registered node.
// Fields updated on every heartbeat are plain int32/int64 values accessed via
// the sync/atomic package — no mutex needed for those updates.
// address is stored as atomic.Value because Register() may update it
// concurrently with snapshot() calls from persist goroutines.
type nodeEntry struct {
	// Immutable after insertion into the map.
	nodeID       string
	registeredAt time.Time

	// address may be updated by Register() on node restart.
	// Stored as atomic.Value to avoid races with concurrent snapshot() calls.
	_address atomic.Value // holds string

	// Mutable; written atomically by HandleHeartbeat.
	_lastSeenNano int64 // Unix nanoseconds; 0 means "never seen"
	_runningJobs  int32
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

func (e *nodeEntry) isHealthy(staleAfter time.Duration) bool {
	ls := e.loadLastSeen()
	return !ls.IsZero() && time.Since(ls) < staleAfter
}

func (e *nodeEntry) snapshot(staleAfter time.Duration) *cpb.Node {
	return &cpb.Node{
		NodeID:       e.nodeID,
		Address:      e.loadAddress(),
		Healthy:      e.isHealthy(staleAfter),
		LastSeen:     e.loadLastSeen(),
		RunningJobs:  e.loadRunning(),
		RegisteredAt: e.registeredAt,
	}
}
