// internal/cluster/registry_heartbeat.go
//
// Registry.HandleHeartbeat processes one heartbeat from the gRPC bidi-stream,
// and PruneStaleNodes periodically marks nodes unhealthy when their last
// heartbeat is older than staleAfter.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// HandleHeartbeat processes one HeartbeatMessage from the gRPC bidi-stream.
// msg is *proto.HeartbeatMessage from coordinator.pb.go:
//
//	NodeId      string
//	Timestamp   int64   (Unix nanoseconds)
//	RunningJobs int32
//	CpuPercent  float64
//	MemPercent  float64
//
// Note: HeartbeatMessage has no Address field. The address is set at Register
// time and never changes unless the node re-registers.
//
// This method is lock-free for existing nodes.
func (r *Registry) HandleHeartbeat(ctx context.Context, msg *pb.HeartbeatMessage) error {
	// Always use the coordinator's wall clock for last-seen. Trusting the
	// node-reported Timestamp allows a node to spoof its health status.
	seen := time.Now()

	// Fast path: existing node — look up under RLock, then release immediately.
	r.mu.RLock()
	entry := r.nodes[msg.NodeId]
	r.mu.RUnlock()

	// Reject heartbeats from nodes that have not completed Register. Implicit
	// registration would let a node bypass the credential exchange in Register().
	if entry == nil {
		r.log.Warn("registry: heartbeat from unregistered node, rejecting",
			slog.String("node_id", msg.NodeId))
		return ErrNodeNotRegistered
	}

	// Atomic updates — no lock held from here.
	entry.storeLastSeen(seen)
	entry.storeRunning(msg.RunningJobs)

	// Persist asynchronously.
	go func() {
		snap := entry.snapshot(r.staleAfter)
		if err := r.persister.SaveNode(context.Background(), snap); err != nil {
			r.log.Error("registry: persist on heartbeat",
				slog.String("node_id", msg.NodeId), slog.Any("err", err))
		}
	}()

	return nil
}

// PruneStaleNodes marks nodes unhealthy if no heartbeat for >= staleAfter.
// No lock is held during any I/O or sleep — only a brief RLock to snapshot
// entry pointers. Returns the nodeIDs of stale nodes found.
func (r *Registry) PruneStaleNodes(ctx context.Context) []string {
	now := time.Now()

	// Snapshot entry pointers under RLock.
	r.mu.RLock()
	entries := make([]*nodeEntry, 0, len(r.nodes))
	for _, e := range r.nodes {
		entries = append(entries, e)
	}
	r.mu.RUnlock()

	var stale []string
	for _, e := range entries {
		ls := e.loadLastSeen()
		if ls.IsZero() || now.Sub(ls) >= r.staleAfter {
			stale = append(stale, e.nodeID)
			snap := e.snapshot(r.staleAfter) // Healthy=false because ls is old/zero

			r.log.Warn("registry: node stale",
				slog.String("node_id", e.nodeID),
				slog.String("address", e.loadAddress()),
				slog.Duration("since_last_heartbeat",
					now.Sub(ls).Round(time.Second)),
			)

			go func(n *cpb.Node) {
				if err := r.persister.SaveNode(ctx, n); err != nil {
					r.log.Error("registry: persist stale node",
						slog.String("node_id", n.NodeID), slog.Any("err", err))
				}
				_ = r.persister.AppendAudit(ctx,
					"node.stale", "coordinator", n.NodeID,
					fmt.Sprintf("no heartbeat for >%v", r.staleAfter))
			}(snap)
		}
	}
	return stale
}
