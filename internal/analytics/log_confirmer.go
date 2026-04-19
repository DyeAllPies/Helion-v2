// internal/analytics/log_confirmer.go
//
// Feature 28 — implementation of logstore.PGLogConfirmer backed by
// the `job_log_entries` table. The logstore reconciler calls
// ConfirmLogBatch to learn which Badger entries have already been
// durably persisted to PostgreSQL so they can be freed from Badger.
//
// Kept in the analytics package (not logstore) so logstore does not
// need to import pgx; the narrow PGLogConfirmer interface sits on
// the logstore side and concrete PG types stay here.

package analytics

import (
	"context"
	"fmt"

	"github.com/DyeAllPies/Helion-v2/internal/logstore"
	"github.com/jackc/pgx/v5"
)

// queryable is the subset of *pgxpool.Pool we use. Extracted so
// tests can plug in a mock that just implements Query.
type queryable interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// LogConfirmer implements logstore.PGLogConfirmer.
type LogConfirmer struct {
	db queryable
}

// NewLogConfirmer returns a LogConfirmer ready to plug into the
// logstore reconciler.
func NewLogConfirmer(db queryable) *LogConfirmer {
	return &LogConfirmer{db: db}
}

// ConfirmLogBatch checks which (job_id, seq) pairs are present in
// job_log_entries. One SQL query per call, using UNNEST so the
// batch rides in a single round-trip.
//
// Scaling note: the batch array goes across the wire twice per
// call (once as job_id[], once as seq[]). A batch size in the low
// hundreds is well within pgx's practical limits; the reconciler
// config defaults to 200, which gives round-trip-proportional cost.
func (c *LogConfirmer) ConfirmLogBatch(ctx context.Context, candidates []logstore.LogKey) (map[logstore.LogKey]bool, error) {
	if len(candidates) == 0 {
		return map[logstore.LogKey]bool{}, nil
	}
	jobIDs := make([]string, len(candidates))
	seqs := make([]int64, len(candidates))
	for i, k := range candidates {
		jobIDs[i] = k.JobID
		seqs[i] = int64(k.Seq)
	}
	// UNNEST pairs the two arrays positionally; the join against
	// job_log_entries returns only rows whose (job_id, seq) pair
	// exists. Any input pair with no matching row is absent from
	// the return — the caller's default-false map handles it.
	rows, err := c.db.Query(ctx, `
		SELECT input.job_id, input.seq
		  FROM (
		    SELECT UNNEST($1::text[])   AS job_id,
		           UNNEST($2::bigint[]) AS seq
		  ) AS input
		  JOIN job_log_entries j
		    ON j.job_id = input.job_id AND j.seq = input.seq
	`, jobIDs, seqs)
	if err != nil {
		return nil, fmt.Errorf("ConfirmLogBatch query: %w", err)
	}
	defer rows.Close()

	out := make(map[logstore.LogKey]bool, len(candidates))
	for rows.Next() {
		var k logstore.LogKey
		var seq int64
		if err := rows.Scan(&k.JobID, &seq); err != nil {
			return nil, fmt.Errorf("ConfirmLogBatch scan: %w", err)
		}
		k.Seq = uint64(seq)
		out[k] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ConfirmLogBatch row iter: %w", err)
	}
	return out, nil
}
