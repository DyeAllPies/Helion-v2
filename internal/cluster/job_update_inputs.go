// internal/cluster/job_update_inputs.go
//
// Step-3 helper: persist a dispatch-time rewrite of a job's Inputs
// slice. The workflow resolver ([workflow_resolve.go]) rewrites each
// `from: <upstream>.<output>` reference into a concrete URI drawn
// from the upstream's ResolvedOutputs. Without this helper the
// rewritten URIs would live only on the in-memory copy that's sent
// to the node — the persisted Job record would still show URI=""
// on every From input, and `GET /api/jobs/{id}` would be blind to
// what actually got dispatched.

package cluster

import (
	"context"
	"fmt"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// UpdateResolvedInputs replaces the persisted Inputs slice of the job
// with inputs. Intended for the dispatch path after ResolveJobInputs
// has filled in URIs on entries that came in with From references —
// the From field is preserved on each entry so lineage stays visible.
//
// Failure modes:
//   - ErrJobNotFound if jobID is unknown
//   - Any persister.SaveJob error is wrapped and returned; the
//     in-memory state is rolled back so callers never observe an
//     update that didn't survive to disk.
//
// The method takes the store's write lock — callers must not hold it.
// Safe to call on a terminal job, though there is no reason to:
// dispatch never re-resolves terminal jobs.
func (s *JobStore) UpdateResolvedInputs(ctx context.Context, jobID string, inputs []cpb.ArtifactBinding) error {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	// Swap in a defensive copy so a caller mutating the slice after
	// the call cannot poison the persisted record.
	newInputs := append([]cpb.ArtifactBinding(nil), inputs...)
	previous := j.Inputs
	j.Inputs = newInputs

	if err := s.persister.SaveJob(ctx, j); err != nil {
		// Roll back the in-memory mutation so the store and the
		// persister cannot disagree.
		j.Inputs = previous
		s.mu.Unlock()
		return fmt.Errorf("JobStore.UpdateResolvedInputs persist: %w", err)
	}
	s.mu.Unlock()
	return nil
}
