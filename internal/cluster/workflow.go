// internal/cluster/workflow.go
//
// WorkflowStore — errors, interfaces, WorkflowStore type.
//
// Workflow lifecycle manages multi-job DAG execution:
//   pending → running → completed / failed / cancelled
//
// Concurrency
// ───────────
// WorkflowStore is protected by a sync.RWMutex, following the same pattern as
// JobStore. Every mutation is persisted before returning.
//
// Relationship to JobStore
// ────────────────────────
// A Workflow contains WorkflowJob definitions (the DAG template). When a
// workflow is submitted, each WorkflowJob creates a real Job in the JobStore
// with a WorkflowID link. The WorkflowStore tracks which workflow-level jobs
// map to which JobStore entries via WorkflowJob.JobID.
//
// File layout
// ───────────
//   workflow.go           — errors, interfaces, WorkflowStore type
//   workflow_submit.go    — Submit, Start
//   workflow_lifecycle.go — EligibleJobs, OnJobCompleted, Cancel
//   workflow_read.go      — Get, List, RunningWorkflowIDs, Restore
//   mem_workflow_persister.go — MemWorkflowPersister (test helper)

package cluster

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/DyeAllPies/Helion-v2/internal/events"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrWorkflowNotFound        = errors.New("workflow: not found")
	ErrWorkflowExists          = errors.New("workflow: id already exists")
	ErrWorkflowAlreadyTerminal = errors.New("workflow: already in terminal state")
	ErrWorkflowEmpty           = errors.New("workflow: must contain at least one job")
)

// ── WorkflowPersister ────────────────────────────────────────────────────────

// WorkflowPersister is the narrow storage interface used by WorkflowStore.
type WorkflowPersister interface {
	SaveWorkflow(ctx context.Context, w *cpb.Workflow) error
	LoadAllWorkflows(ctx context.Context) ([]*cpb.Workflow, error)
	AppendAudit(ctx context.Context, eventType, actor, target, detail string) error
}

// ── WorkflowStore ────────────────────────────────────────────────────────────

// WorkflowStore is the coordinator's in-memory index of all workflows.
type WorkflowStore struct {
	mu        sync.RWMutex
	workflows map[string]*cpb.Workflow
	persister WorkflowPersister
	log       *slog.Logger
	eventBus  *events.Bus // nil when event emission is not wired
}

// NewWorkflowStore creates a WorkflowStore backed by the given persister.
func NewWorkflowStore(p WorkflowPersister, log *slog.Logger) *WorkflowStore {
	if log == nil {
		log = slog.Default()
	}
	return &WorkflowStore{
		workflows: make(map[string]*cpb.Workflow),
		persister: p,
		log:       log,
	}
}

// SetEventBus attaches an event bus so workflow lifecycle transitions emit
// workflow.completed / workflow.failed events. Optional — if unset, the
// store works as before but downstream consumers (analytics sink, WebSocket
// feed) will not see workflow events.
func (s *WorkflowStore) SetEventBus(bus *events.Bus) {
	s.eventBus = bus
}
