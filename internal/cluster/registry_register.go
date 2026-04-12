// internal/cluster/registry_register.go
//
// Registry.Register handles node registration including ID validation,
// revocation check, ML-DSA signature verification, and cert pinning.

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// Register handles the gRPC Register RPC.
// req is *proto.RegisterRequest from coordinator.pb.go:
//
//	NodeId      string
//	Address     string
//	Certificate []byte  (DER; used in Phase 4)
func (r *Registry) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	// Validate node ID format before any further processing.
	if err := validateNodeID(req.NodeId); err != nil {
		r.log.Warn("registry: rejected registration with invalid node ID",
			slog.String("node_id", req.NodeId), slog.Any("err", err))
		return nil, err
	}

	// Phase 4: reject registration from a revoked node.
	if r.IsRevoked(req.NodeId) {
		r.log.Warn("registry: revoked node attempted re-registration",
			slog.String("node_id", req.NodeId))
		return nil, fmt.Errorf("node %s is revoked: re-register with a new certificate", req.NodeId)
	}

	// Phase 4: certificate checks (pinning + ML-DSA) when the request carries
	// DER cert bytes.
	if len(req.Certificate) > 0 {
		// ML-DSA out-of-band signature verification: ensures the cert was
		// issued by this coordinator's CA (not a rogue CA with a stolen ECDSA key).
		r.certVerifierMu.RLock()
		cv := r.certVerifier
		r.certVerifierMu.RUnlock()

		if cv != nil {
			if err := cv.VerifyNodeCertMLDSA(req.Certificate); err != nil {
				r.log.Warn("registry: ML-DSA verification failed — rejecting registration",
					slog.String("node_id", req.NodeId), slog.Any("err", err))
				return nil, fmt.Errorf("node %s: ML-DSA signature verification failed", req.NodeId)
			}
		}

		// Certificate pinning: enforce fingerprint consistency across
		// re-registrations so a newly-issued cert can't silently replace the old one.
		r.certPinnerMu.RLock()
		cp := r.certPinner
		r.certPinnerMu.RUnlock()

		if cp != nil {
			fp := CertFingerprint(req.Certificate)
			stored, err := cp.GetPin(ctx, req.NodeId)
			if err != nil {
				// No pin stored yet — record it for future registrations.
				if perr := cp.SetPin(ctx, req.NodeId, fp); perr != nil {
					r.log.Error("registry: store cert pin failed",
						slog.String("node_id", req.NodeId), slog.Any("err", perr))
				} else {
					r.log.Info("registry: cert pin stored",
						slog.String("node_id", req.NodeId))
				}
			} else if stored != fp {
				r.log.Warn("registry: cert fingerprint mismatch — rejecting registration",
					slog.String("node_id", req.NodeId))
				return nil, ErrCertFingerprintMismatch
			}
		}
	}

	now := time.Now()

	r.mu.Lock()
	entry, exists := r.nodes[req.NodeId]
	if !exists {
		entry = &nodeEntry{
			nodeID:       req.NodeId,
			registeredAt: now,
		}
		entry.storeAddress(req.Address)
		r.nodes[req.NodeId] = entry
	} else {
		// Node restarted — update address in case port changed.
		entry.storeAddress(req.Address)
	}
	entry.storeLastSeen(now)
	r.mu.Unlock()

	r.log.Info("node registered",
		slog.String("node_id", req.NodeId),
		slog.String("address", req.Address),
		slog.Bool("new", !exists),
	)

	// Persist and audit asynchronously — RPC response does not wait for disk.
	// Both writes run under a bounded timeout and are tracked by auditWG so
	// Close can drain them during shutdown (AUDIT 2026-04-11/M1).
	snap := entry.snapshot(r.staleAfter)
	r.persistNodeAsync(snap)
	r.appendAuditAsync("node.registered", req.NodeId, req.NodeId,
		fmt.Sprintf("address=%s new=%v", req.Address, !exists))

	// AUDIT 2026-04-12/H1: issue a coordinator-signed certificate so the node
	// can present it on its gRPC server. This allows the coordinator to verify
	// node certs during dispatch instead of using InsecureSkipVerify.
	resp := &pb.RegisterResponse{NodeId: req.NodeId}
	r.certIssuerMu.RLock()
	ci := r.certIssuer
	r.certIssuerMu.RUnlock()
	if ci != nil {
		certPEM, keyPEM, err := ci.IssueNodeCert(req.NodeId)
		if err != nil {
			r.log.Error("registry: issue node cert failed",
				slog.String("node_id", req.NodeId), slog.Any("err", err))
		} else {
			// Bundle cert + key into a single PEM payload. The node splits
			// them by PEM block type ("CERTIFICATE" vs "EC PRIVATE KEY").
			resp.SignedCertificate = append(certPEM, keyPEM...)
		}
	}

	return resp, nil
}
