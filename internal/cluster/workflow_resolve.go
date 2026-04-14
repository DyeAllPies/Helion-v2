// internal/cluster/workflow_resolve.go
//
// Step-3 artifact-reference resolution.
//
// A workflow job's input can declare `From: "<upstream_name>.<output_name>"`
// in place of a concrete URI. This file holds the dispatch-time
// resolver that rewrites each such reference to the actual URI the
// upstream job uploaded, read from its Job.ResolvedOutputs record.
//
// The resolver is the coordinator's trust boundary for cross-job data
// flow: it refuses to build a DispatchRequest with an unresolved From,
// so a workflow bug that skipped DAG validation cannot smuggle a
// placeholder reference onto a node. The upstream's ResolvedOutputs
// has already passed `attestOutputs` (scheme + prefix + suffix
// checks), so the resolved URI is safe to hand to the node's stager.

package cluster

import (
	"errors"
	"fmt"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// Errors surfaced by ResolveJobInputs. All three indicate a workflow
// bug: the DAG should have ruled them out at submit time.
var (
	// ErrResolveFromNotWorkflow fires when a From reference shows up
	// on a job with no WorkflowID (it should have been rejected by
	// the submit validators).
	ErrResolveFromNotWorkflow = errors.New("resolve: from on non-workflow job")

	// ErrResolveUpstreamMissing fires when the coordinator cannot
	// locate the upstream job by ID. Possible on a partially
	// recovered coordinator or a hand-edited BadgerDB.
	ErrResolveUpstreamMissing = errors.New("resolve: upstream job not found")

	// ErrResolveUpstreamNotCompleted fires when the upstream exists
	// but has not reached Completed. The scheduler's dependency gate
	// should prevent this at dispatch time; a slip through here means
	// the dependency condition was `on_failure`/`on_complete` and the
	// upstream is not ready to attest its outputs.
	ErrResolveUpstreamNotCompleted = errors.New("resolve: upstream not completed")

	// ErrResolveOutputMissing fires when the upstream completed but
	// its ResolvedOutputs does not carry an entry with the requested
	// name (either the job crashed before writing the file, or the
	// stager refused the upload, or attestOutputs dropped it).
	ErrResolveOutputMissing = errors.New("resolve: upstream output not found")
)

// JobLookup is the narrow read interface ResolveJobInputs needs from
// the JobStore. Defined as an interface so unit tests can drive it
// without a full store.
type JobLookup interface {
	Get(jobID string) (*cpb.Job, error)
}

// ResolveJobInputs rewrites every input with a non-empty `From` into
// a concrete URI drawn from the upstream job's ResolvedOutputs. The
// returned *cpb.Job is a copy — the persisted record retains the
// original From reference so the lineage is auditable across retries.
// If every input already has a URI (or the job is not a workflow
// job), the function returns the pointer unchanged.
func ResolveJobInputs(job *cpb.Job, jobs JobLookup) (*cpb.Job, error) {
	if job == nil {
		return nil, errors.New("resolve: nil job")
	}
	if !hasAnyFromRef(job.Inputs) {
		return job, nil
	}
	if job.WorkflowID == "" {
		return nil, ErrResolveFromNotWorkflow
	}

	out := *job
	out.Inputs = make([]cpb.ArtifactBinding, len(job.Inputs))
	copy(out.Inputs, job.Inputs)

	for i := range out.Inputs {
		b := &out.Inputs[i]
		if b.From == "" {
			continue
		}
		upstreamName, outputName := splitDotRef(b.From)
		if upstreamName == "" {
			return nil, fmt.Errorf(`resolve: input %q: from must be "<upstream>.<output>"`, b.Name)
		}
		upstreamID := job.WorkflowID + "/" + upstreamName
		upstream, err := jobs.Get(upstreamID)
		if err != nil {
			return nil, fmt.Errorf("%w: %s (%v)", ErrResolveUpstreamMissing, upstreamID, err)
		}
		if upstream.Status != cpb.JobStatusCompleted {
			return nil, fmt.Errorf("%w: %s is %s", ErrResolveUpstreamNotCompleted, upstreamID, upstream.Status)
		}
		found := findResolvedOutput(upstream.ResolvedOutputs, outputName)
		if found == nil {
			return nil, fmt.Errorf("%w: %s has no output %q", ErrResolveOutputMissing, upstreamID, outputName)
		}
		b.URI = found.URI
		// Copy the upstream's committed SHA-256 onto the downstream's
		// input so the node's stager can verify the download via
		// artifacts.GetAndVerify. Empty digest means "upstream never
		// committed one" — rare, but plain-URI inputs also land here
		// and stay at empty; the stager falls back to a plain Get.
		b.SHA256 = found.SHA256
		// Keep the From field: the persisted Job should carry both
		// so /api/jobs/{id} responses show the lineage. The copy we
		// dispatch to the node carries URI + SHA256 + From — the
		// proto builder forwards URI + SHA256 + LocalPath.
	}
	return &out, nil
}

// hasAnyFromRef is the cheap early-exit: most jobs don't carry any
// From references, so avoid the defensive slice copy when we can.
func hasAnyFromRef(bs []cpb.ArtifactBinding) bool {
	for _, b := range bs {
		if b.From != "" {
			return true
		}
	}
	return false
}

// findResolvedOutput returns a pointer to the first ResolvedOutput
// whose Name matches, or nil when no such entry exists. Order matters
// only if a job somehow reported two outputs with the same name,
// which attestOutputs currently allows — first-wins is the
// conservative choice. The caller reads both URI and SHA256 off the
// returned pointer, so the cross-job integrity attestation (the
// digest) travels with the URI instead of being re-fetched.
func findResolvedOutput(outs []cpb.ArtifactOutput, name string) *cpb.ArtifactOutput {
	for i := range outs {
		if outs[i].Name == name {
			return &outs[i]
		}
	}
	return nil
}
