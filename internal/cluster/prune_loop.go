// internal/cluster/prune_loop.go
//
// RunPruneLoop runs the stale-node pruning background goroutine.
//
// Design
// ──────
// PruneStaleNodes() is the business logic — it already exists on Registry and
// is fully tested in registry_test.go.  RunPruneLoop is just the scheduler
// that calls it on a ticker.
//
// The goroutine:
//   - Ticks every heartbeatInterval (same cadence as heartbeats).
//   - Calls PruneStaleNodes on each tick.
//   - Holds NO lock during the ticker wait — the lock comment in the design
//     doc refers to this: "no lock held during sleep".
//   - Exits cleanly when ctx is cancelled.
//
// Caller contract
// ───────────────
// The coordinator's main() launches this in a goroutine after the server is
// ready to accept connections:
//
//	go registry.RunPruneLoop(ctx)
//
// When ctx is cancelled (coordinator shutdown) the goroutine exits.
// No explicit Stop() method is needed — ctx cancellation is sufficient.
//
// GC loop
// ───────
// RunPruneLoop also triggers BadgerDB value-log GC on a slower cadence
// (every gcInterval = 10 × heartbeatInterval) so the coordinator's main()
// does not need a separate GC goroutine.  The GC persister interface is
// optional — if the injected Persister does not implement GCRunner the GC
// step is silently skipped.

package cluster

import (
	"context"
	"log/slog"
	"time"
)

// GCRunner is an optional interface implemented by persisters that support
// BadgerDB value-log garbage collection.  BadgerJSONPersister implements it;
// NopPersister and MemPersister do not.
type GCRunner interface {
	RunGC(discardRatio float64) error
}

// RunPruneLoop blocks until ctx is cancelled, calling PruneStaleNodes on
// every heartbeatInterval tick.
//
// It is designed to be launched in a goroutine:
//
//	go registry.RunPruneLoop(ctx)
func (r *Registry) RunPruneLoop(ctx context.Context) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	// GC fires every gcMultiple ticks to keep BadgerDB from accumulating
	// dead value-log space without requiring a separate goroutine.
	const gcMultiple = 10
	tick := 0

	for {
		select {
		case <-ctx.Done():
			r.log.Info("registry: prune loop stopped")
			return
		case <-ticker.C:
			stale := r.PruneStaleNodes(ctx)
			if len(stale) > 0 {
				r.log.Info("registry: prune loop marked nodes stale",
					slog.Int("count", len(stale)),
				)
			}

			tick++
			if tick%gcMultiple == 0 {
				if gc, ok := r.persister.(GCRunner); ok {
					if err := gc.RunGC(0.5); err != nil {
						r.log.Error("registry: BadgerDB GC error", slog.Any("err", err))
					}
				}
			}
		}
	}
}
