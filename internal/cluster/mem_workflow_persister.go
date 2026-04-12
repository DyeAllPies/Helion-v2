// internal/cluster/mem_workflow_persister.go
//
// MemWorkflowPersister is an in-memory WorkflowPersister used by tests.

package cluster

import (
	"context"
	"fmt"
	"sync"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// MemWorkflowPersister is an in-memory WorkflowPersister for tests.
type MemWorkflowPersister struct {
	mu        sync.Mutex
	Workflows map[string]*cpb.Workflow
	Audits    []string
}

// NewMemWorkflowPersister returns an initialised MemWorkflowPersister.
func NewMemWorkflowPersister() *MemWorkflowPersister {
	return &MemWorkflowPersister{Workflows: make(map[string]*cpb.Workflow)}
}

func (m *MemWorkflowPersister) SaveWorkflow(_ context.Context, w *cpb.Workflow) error {
	cp := *w
	cpJobs := make([]cpb.WorkflowJob, len(w.Jobs))
	copy(cpJobs, w.Jobs)
	cp.Jobs = cpJobs
	m.mu.Lock()
	m.Workflows[w.ID] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemWorkflowPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error) {
	m.mu.Lock()
	out := make([]*cpb.Workflow, 0, len(m.Workflows))
	for _, w := range m.Workflows {
		cp := *w
		cpJobs := make([]cpb.WorkflowJob, len(w.Jobs))
		copy(cpJobs, w.Jobs)
		cp.Jobs = cpJobs
		out = append(out, &cp)
	}
	m.mu.Unlock()
	return out, nil
}

func (m *MemWorkflowPersister) AppendAudit(_ context.Context, eventType, _, target, detail string) error {
	m.mu.Lock()
	m.Audits = append(m.Audits, fmt.Sprintf("%s target=%s %s", eventType, target, detail))
	m.mu.Unlock()
	return nil
}
