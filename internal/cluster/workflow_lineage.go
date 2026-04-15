// internal/cluster/workflow_lineage.go
//
// Feature 18 → deferred/24: coordinator-side join that powers
// GET /api/workflows/{id}/lineage. Walks a workflow's job spec,
// resolves each job's live status + resolved outputs via JobStore,
// joins outputs against the model registry by SourceJobID, and
// emits edges for both dependency arrows (DependsOn) and artifact
// flow (Inputs[i].From).
//
// Doing the join on the coordinator avoids N+1 fan-out from the
// dashboard: one HTTP request per workflow view instead of one
// per-job plus one per-output.
//
// This file is the data-shaping layer only. The HTTP handler in
// internal/api/handlers_workflows_lineage.go wraps it with auth +
// response marshalling; the frontend in dashboard/features/ml/
// renders it with mermaid.

package cluster

import (
	"context"
	"errors"
	"strings"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/registry"
)

// ── Response types ───────────────────────────────────────────────────────────

// WorkflowLineage is the response shape. IDs and names are
// coordinator-native strings; callers render them however they like.
type WorkflowLineage struct {
	WorkflowID   string          `json:"workflow_id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	Jobs         []LineageJob    `json:"jobs"`
	ArtifactEdges []ArtifactEdge `json:"artifact_edges"`
}

// LineageJob captures one job in the workflow plus its live status
// and the registered models (if any) that point back at it via
// SourceJobID. DependsOn uses the workflow-local job name (not the
// generated JobID), matching how workflow specs declare edges.
type LineageJob struct {
	Name             string                `json:"name"`
	JobID            string                `json:"job_id,omitempty"` // generated, empty before Start()
	Status           string                `json:"status"`
	Command          string                `json:"command,omitempty"`
	DependsOn        []string              `json:"depends_on,omitempty"`
	Outputs          []LineageOutput       `json:"outputs,omitempty"`
	ModelsProduced   []LineageModelRef     `json:"models_produced,omitempty"`
}

// LineageOutput is a trimmed ArtifactOutput — URI / size / sha256
// are the bits the dashboard shows on edge tooltips.
type LineageOutput struct {
	Name   string `json:"name"`
	URI    string `json:"uri"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// LineageModelRef is a pointer to a registered model that was
// produced by this job. Bare (name, version) — the frontend links
// back into the /ml/models view.
type LineageModelRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ArtifactEdge represents one `from: upstream.OUTPUT` reference —
// distinct from the DependsOn edge which is a pure ordering arrow.
// An artifact edge implies a DependsOn edge but carries the
// additional "this bytes went from A to B" semantics the dashboard
// surfaces on hover.
type ArtifactEdge struct {
	FromJob    string `json:"from_job"`
	FromOutput string `json:"from_output"`
	ToJob      string `json:"to_job"`
	ToInput    string `json:"to_input"`
}

// ── Errors ───────────────────────────────────────────────────────────────────

var (
	// ErrWorkflowLineageNotFound surfaces to the HTTP handler as 404.
	ErrWorkflowLineageNotFound = errors.New("workflow lineage: not found")
)

// ── Interfaces (narrow; tests can inject fakes) ──────────────────────────────

// LineageWorkflowReader is the subset of WorkflowStore the builder
// needs — just Get. Kept narrow so tests don't need a full store.
type LineageWorkflowReader interface {
	Get(workflowID string) (*cpb.Workflow, error)
}

// LineageJobReader is the subset of JobStore the builder needs.
// JobStore already exposes Get; this interface exists so the same
// type can be satisfied by a test stub.
type LineageJobReader interface {
	Get(jobID string) (*cpb.Job, error)
}

// LineageModelReader is the subset of registry.ModelStore the
// builder needs: ListBySourceJob, for the produced-model join.
type LineageModelReader interface {
	ListBySourceJob(ctx context.Context, sourceJobID string) ([]*registry.Model, error)
}

// ── Builder ──────────────────────────────────────────────────────────────────

// BuildWorkflowLineage joins a workflow's jobs against the job store
// and the model registry. Returns ErrWorkflowLineageNotFound when
// the workflow does not exist; unwraps model-store errors verbatim
// because the coordinator's handler turns them into 500s.
//
// Missing per-job data (job not yet created, or its ResolvedOutputs
// empty because the job hasn't completed) is rendered as a job with
// status but no outputs. The lineage view is a snapshot; a running
// workflow's mid-flight state is a valid render.
func BuildWorkflowLineage(
	ctx context.Context,
	workflowID string,
	workflows LineageWorkflowReader,
	jobs LineageJobReader,
	models LineageModelReader,
) (*WorkflowLineage, error) {
	wf, err := workflows.Get(workflowID)
	if err != nil || wf == nil {
		return nil, ErrWorkflowLineageNotFound
	}

	lineage := &WorkflowLineage{
		WorkflowID:    wf.ID,
		Name:          wf.Name,
		Status:        string(wf.Status),
		Jobs:          make([]LineageJob, 0, len(wf.Jobs)),
		ArtifactEdges: nil,
	}

	// First pass: build per-job records from the workflow spec.
	// JobStore lookups are best-effort — a workflow whose Start()
	// hasn't run yet has empty JobIDs, which is a valid "pending"
	// render.
	for _, wfJob := range wf.Jobs {
		lj := LineageJob{
			Name:      wfJob.Name,
			JobID:     wfJob.JobID,
			Command:   wfJob.Command,
			DependsOn: append([]string(nil), wfJob.DependsOn...),
			// Matches cpb.JobStatus.String() casing so the dashboard
			// can compare this field against all the status strings it
			// already knows without a parallel-casing branch.
			Status: "pending", // overwritten below if the job exists
		}

		if wfJob.JobID != "" {
			if job, jerr := jobs.Get(wfJob.JobID); jerr == nil && job != nil {
				lj.Status = job.Status.String()
				for _, out := range job.ResolvedOutputs {
					lj.Outputs = append(lj.Outputs, LineageOutput{
						Name: out.Name, URI: out.URI, Size: out.Size, SHA256: out.SHA256,
					})
				}
				// Join produced models. Skip on registry wiring-
				// absent (models == nil) so a coordinator without
				// the registry still serves lineage with just the
				// job view.
				if models != nil {
					produced, merr := models.ListBySourceJob(ctx, wfJob.JobID)
					if merr != nil {
						return nil, merr
					}
					for _, m := range produced {
						lj.ModelsProduced = append(lj.ModelsProduced, LineageModelRef{
							Name: m.Name, Version: m.Version,
						})
					}
				}
			}
		}

		lineage.Jobs = append(lineage.Jobs, lj)
	}

	// Second pass: artifact edges. Walk each job's declared Inputs
	// and split any `From: "<upstream>.<name>"` reference into an
	// ArtifactEdge. This mirrors firstFromRef's split rule in
	// dispatch.go so the two stay consistent.
	for _, wfJob := range wf.Jobs {
		for _, in := range wfJob.Inputs {
			if in.From == "" {
				continue
			}
			upstream, outputName := splitFromRef(in.From)
			if upstream == "" {
				continue
			}
			lineage.ArtifactEdges = append(lineage.ArtifactEdges, ArtifactEdge{
				FromJob: upstream, FromOutput: outputName,
				ToJob:   wfJob.Name, ToInput: in.Name,
			})
		}
	}

	return lineage, nil
}

// splitFromRef splits "<upstream>.<output>" into its two halves.
// Mirrors firstFromRef in dispatch.go; kept separate so a rename
// or format change in one doesn't silently desync the other.
func splitFromRef(ref string) (upstream, outputName string) {
	idx := strings.IndexByte(ref, '.')
	if idx < 0 {
		return ref, ""
	}
	return ref[:idx], ref[idx+1:]
}
