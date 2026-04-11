// internal/cluster/registry_revoke.go
//
// Registry.RevokeNode marks a node as revoked, closes its active heartbeat
// stream, clears its cert pin, and writes an audit event.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
)

// RevokeNode marks a node as revoked and forcibly closes its active heartbeat
// stream (if a StreamRevoker is wired in).  The node must re-register with a
// new node ID to reconnect.
func (r *Registry) RevokeNode(ctx context.Context, nodeID, reason string) error {
	r.revokedMu.Lock()
	r.revoked[nodeID] = struct{}{}
	r.revokedMu.Unlock()

	// Remove from active nodes map so it won't be scheduled.
	r.mu.Lock()
	delete(r.nodes, nodeID)
	r.mu.Unlock()

	// Forcibly close the node's active heartbeat stream so it receives
	// codes.Unauthenticated without waiting for the next heartbeat interval.
	r.streamRevokerMu.RLock()
	sr := r.streamRevoker
	r.streamRevokerMu.RUnlock()
	if sr != nil {
		sr.CancelStream(nodeID)
	}

	// Clear the cert pin so a new cert can be pinned on fresh registration.
	r.certPinnerMu.RLock()
	cp := r.certPinner
	r.certPinnerMu.RUnlock()
	if cp != nil {
		if err := cp.DeletePin(ctx, nodeID); err != nil {
			r.log.Warn("registry: delete cert pin failed",
				slog.String("node_id", nodeID), slog.Any("err", err))
		}
	}

	r.log.Warn("registry: node revoked",
		slog.String("node_id", nodeID),
		slog.String("reason", reason))

	r.appendAuditAsync("node.revoked", "coordinator", nodeID,
		fmt.Sprintf("reason=%s", reason))
	return nil
}

// IsRevoked reports whether nodeID has been revoked.
// Safe for concurrent use; called on every incoming gRPC RPC.
func (r *Registry) IsRevoked(nodeID string) bool {
	r.revokedMu.RLock()
	_, ok := r.revoked[nodeID]
	r.revokedMu.RUnlock()
	return ok
}
