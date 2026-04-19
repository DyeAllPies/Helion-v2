// internal/cluster/persistence_jobs.go
//
// BadgerJSONPersister job methods (satisfies JobPersister interface).

package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// SaveJob writes a Job record under jobs/{id} in a single read-write
// transaction. Job entries have no TTL — they are immutable once terminal and
// are the source of truth for crash recovery.
func (p *BadgerJSONPersister) SaveJob(_ context.Context, j *cpb.Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("SaveJob marshal: %w", err)
	}
	key := []byte("jobs/" + j.ID)
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// LoadAllJobs reads all jobs/ entries for crash-recovery on startup.
// It returns every job regardless of status; the caller (JobStore.Restore)
// filters for non-terminal jobs to build the retry queue.
func (p *BadgerJSONPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error) {
	var jobs []*cpb.Job
	prefix := []byte("jobs/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var j cpb.Job
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &j)
			}); err != nil {
				return fmt.Errorf("LoadAllJobs unmarshal %q: %w", it.Item().Key(), err)
			}
			// Feature 36 — backfill OwnerPrincipal for records
			// that predate the field. SubmittedBy is the
			// authoritative pre-feature-36 owner proxy (set by
			// the API layer on submit). If that's also empty,
			// fall through to the legacy sentinel so feature
			// 37's policy evaluator fails closed. The backfill
			// runs in-memory only — we do NOT rewrite the
			// Badger entry here; the next state transition
			// (dispatch, finish, retry, cancel) will persist
			// the synthesised value as a side effect of the
			// existing SaveJob call path.
			if j.OwnerPrincipal == "" {
				j.OwnerPrincipal = principal.OwnerFromLegacy(j.SubmittedBy)
			}
			jobs = append(jobs, &j)
		}
		return nil
	})
	return jobs, err
}
