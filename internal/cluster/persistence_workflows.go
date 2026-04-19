// internal/cluster/persistence_workflows.go
//
// BadgerJSONPersister workflow methods (satisfies WorkflowPersister interface).

package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/principal"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// SaveWorkflow writes a Workflow record under workflows/{id}.
// Workflow entries have no TTL — they persist until explicitly deleted.
func (p *BadgerJSONPersister) SaveWorkflow(_ context.Context, w *cpb.Workflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("SaveWorkflow marshal: %w", err)
	}
	key := []byte("workflows/" + w.ID)
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// LoadAllWorkflows reads all workflows/ entries for crash-recovery on startup.
func (p *BadgerJSONPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error) {
	var workflows []*cpb.Workflow
	prefix := []byte("workflows/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var w cpb.Workflow
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &w)
			}); err != nil {
				return fmt.Errorf("LoadAllWorkflows unmarshal %q: %w", it.Item().Key(), err)
			}
			// Feature 36 — backfill OwnerPrincipal for workflows
			// that predate the field. Workflows have no pre-36
			// submitter proxy (SubmittedBy never existed on the
			// Workflow struct), so every legacy record lands on
			// the fail-closed LegacyOwnerID sentinel.
			if w.OwnerPrincipal == "" {
				w.OwnerPrincipal = principal.OwnerFromLegacy("")
			}
			workflows = append(workflows, &w)
		}
		return nil
	})
	return workflows, err
}
