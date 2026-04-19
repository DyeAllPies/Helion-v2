// internal/logstore/reconciler.go
//
// Feature 28 — Badger→Postgres log reconciler.
//
// Goal (per user direction after the original feature-28 commit):
//
//   "badger produces logs, then we save logs into postgres analytics,
//    then delete badger logs (the postgres are better, as long as we
//    dont lose any data from badger)."
//
// This file owns the "delete badger logs that are confirmed in
// postgres" half of that workflow. The "save into postgres" half is
// the existing `job.log` event → analytics sink path (feature 28
// commit). The two together flow every log chunk from Badger into
// PostgreSQL and free the Badger copy once the PG copy is durable.
//
// Safety properties:
//
//   1. We never delete a Badger entry that PG doesn't confirm. The
//      query is `SELECT 1 FROM job_log_entries WHERE job_id = $1
//      AND seq = $2` — a miss means "don't delete".
//
//   2. A PG query error for one entry does NOT cascade into
//      deletions on other entries. The reconciler logs and
//      continues; the next tick retries.
//
//   3. A minimum-age gate keeps us from racing the sink's batched
//      flush. Entries younger than the gate are skipped even when
//      confirmed, because "confirmed" in the next poll is cheap
//      and losing a just-landed chunk is expensive.
//
//   4. The whole reconciler is OPT-IN via HELION_LOGSTORE_RECONCILE.
//      Operators who prefer the belt-and-braces "dual copy forever"
//      model leave it off; the Badger TTL (default 7 days) still
//      frees space eventually, the PG copy remains permanent.

package logstore

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ReconcilerConfig tunes the background loop. Defaults are applied
// in NewReconciler when fields are zero-valued.
type ReconcilerConfig struct {
	// Interval between reconciliation sweeps. Default 10 minutes.
	// Short enough that a busy cluster doesn't build up a huge
	// Badger backlog; long enough that the PG query load stays a
	// minor fraction of the cluster's analytics pressure.
	Interval time.Duration

	// MinAge is the safety margin: log entries whose Timestamp is
	// newer than MinAge are not considered for deletion, even when
	// PG confirms them. Default: 2 × sink flush interval (~1 s) is
	// enough, but we pick a much more conservative 5 minutes by
	// default so a transient PG sink outage doesn't cause immediate
	// deletion of not-yet-persisted chunks.
	MinAge time.Duration

	// BatchSize is the PG rows-per-query batch when checking
	// confirmation. The reconciler groups Badger entries by jobID
	// and runs one query per group; batching reduces round-trips.
	// Default 200.
	BatchSize int
}

// PGLogConfirmer is the narrow query surface the reconciler needs.
// Satisfied by *pgxpool.Pool in production; tests plug in a mock.
type PGLogConfirmer interface {
	// ConfirmLogBatch returns the set of (job_id, seq) pairs
	// present in `job_log_entries` out of the `candidates` map the
	// caller provides. The return set is keyed the same way as
	// `candidates`; any pair not in the return map is treated by
	// the caller as "not confirmed".
	//
	// Implementations should execute ONE SQL query (ANY/IN) per
	// call; per-entry queries would make the reconciler an N+1
	// nightmare on a busy cluster.
	ConfirmLogBatch(ctx context.Context, candidates []LogKey) (confirmed map[LogKey]bool, err error)
}

// LogKey identifies a single Badger log entry. Exported so mock
// confirmers can enumerate it.
type LogKey struct {
	JobID string
	Seq   uint64
}

// Reconciler drives periodic reconciliation. Start kicks off the
// loop; Stop cancels and waits.
type Reconciler struct {
	store     Reconcilable
	confirmer PGLogConfirmer
	cfg       ReconcilerConfig
	log       *slog.Logger

	cancelMu sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewReconciler returns a reconciler ready for Start. Both args are
// required. Nil log defaults to slog.Default(). Zero-valued cfg
// fields get defaults applied.
func NewReconciler(store Reconcilable, confirmer PGLogConfirmer, cfg ReconcilerConfig, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.MinAge <= 0 {
		cfg.MinAge = 5 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	return &Reconciler{
		store:     store,
		confirmer: confirmer,
		cfg:       cfg,
		log:       log,
		done:      make(chan struct{}),
	}
}

// Start launches the reconciler goroutine. Idempotent: a second
// Start before Stop is a no-op.
func (r *Reconciler) Start(ctx context.Context) {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.loop(ctx)
}

// Stop cancels and waits for the loop to drain. Idempotent.
func (r *Reconciler) Stop() {
	r.cancelMu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.cancelMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-r.done
}

func (r *Reconciler) loop(ctx context.Context) {
	defer close(r.done)
	// Initial sweep on Start — if the coordinator restarts with a
	// big Badger backlog AND a healthy PG, we don't wait a full
	// Interval before the first cleanup.
	r.sweep(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// sweep runs one reconciliation pass. Never propagates an error —
// a failing sweep is logged and the loop carries on.
func (r *Reconciler) sweep(ctx context.Context) {
	// Buffer `candidates` up to BatchSize, flush to the confirmer,
	// record confirmations, then let ReconcileConfirmed's
	// per-entry callback consult the cached result.
	//
	// Implementation wrinkle: ReconcileConfirmed runs the callback
	// synchronously per entry. A naive implementation would fire
	// one PG query per entry (N+1). Instead we pre-batch: first
	// pass scans Badger to build the candidate list; second pass
	// calls ReconcileConfirmed with a cached-map callback.
	//
	// Simpler pragmatic approach: do TWO invocations of
	// ReconcileConfirmed — the first with a candidate-gathering
	// callback (always returns false, so nothing deletes), the
	// second with a cached-map callback that returns true for
	// entries we already confirmed in the intervening PG query.
	//
	// In practice at realistic volumes (≤100 k chunks in Badger
	// at any time) the overhead of two scans is fine; the PG
	// query is one round-trip regardless.
	var candidates []LogKey
	_, scanned, err := r.store.ReconcileConfirmed(ctx, r.cfg.MinAge,
		func(jobID string, seq uint64) (bool, error) {
			candidates = append(candidates, LogKey{JobID: jobID, Seq: seq})
			return false, nil // never delete on the gathering pass
		})
	if err != nil {
		r.log.Warn("logstore reconciler: gathering pass failed", slog.Any("err", err))
		return
	}
	if scanned == 0 {
		return
	}
	if len(candidates) == 0 {
		// Everything scanned was younger than MinAge — defer.
		return
	}

	confirmed, err := r.runConfirmBatches(ctx, candidates)
	if err != nil {
		r.log.Warn("logstore reconciler: PG confirm batch failed",
			slog.Any("err", err), slog.Int("candidates", len(candidates)))
		return
	}

	// Second pass: delete confirmed entries. We do NOT re-filter by
	// MinAge here — the first pass already did. Callback returns
	// the cached answer.
	deleted, _, err := r.store.ReconcileConfirmed(ctx, r.cfg.MinAge,
		func(jobID string, seq uint64) (bool, error) {
			return confirmed[LogKey{JobID: jobID, Seq: seq}], nil
		})
	if err != nil {
		r.log.Warn("logstore reconciler: delete pass errored (partial)",
			slog.Any("err", err))
		// Fall through — some deletions may have succeeded.
	}
	if deleted > 0 {
		r.log.Info("logstore reconciler: freed Badger entries",
			slog.Int("deleted", deleted),
			slog.Int("candidates", len(candidates)),
			slog.Int("scanned", scanned))
	}
}

// runConfirmBatches splits the candidate list into BatchSize chunks
// and unions the PG confirmations.
func (r *Reconciler) runConfirmBatches(ctx context.Context, candidates []LogKey) (map[LogKey]bool, error) {
	out := make(map[LogKey]bool, len(candidates))
	for i := 0; i < len(candidates); i += r.cfg.BatchSize {
		end := i + r.cfg.BatchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[i:end]
		conf, err := r.confirmer.ConfirmLogBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		for k, v := range conf {
			if v {
				out[k] = true
			}
		}
	}
	return out, nil
}
