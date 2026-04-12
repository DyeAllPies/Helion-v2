// internal/cluster/persistence_test_helpers.go
//
// NopPersister — test no-op (satisfies Persister, JobPersister, WorkflowPersister).
// MemPersister — test in-memory node/workflow/audit store (satisfies Persister + WorkflowPersister).

package cluster

import (
	"context"
	"sync"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── NopPersister ──────────────────────────────────────────────────────────────

// NopPersister satisfies Persister, JobPersister, and WorkflowPersister — for
// unit tests that do not need to inspect persisted state.
type NopPersister struct{}

func (NopPersister) SaveNode(_ context.Context, _ *cpb.Node) error              { return nil }
func (NopPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error)         { return nil, nil }
func (NopPersister) SaveJob(_ context.Context, _ *cpb.Job) error                 { return nil }
func (NopPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error)           { return nil, nil }
func (NopPersister) SaveWorkflow(_ context.Context, _ *cpb.Workflow) error       { return nil }
func (NopPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error) { return nil, nil }
func (NopPersister) AppendAudit(_ context.Context, _, _, _, _ string) error      { return nil }

// ── MemPersister ──────────────────────────────────────────────────────────────

// MemPersister is an in-memory Persister (node + workflow side) for tests that
// need to inspect what was persisted without a real database.
//
// For the job side, use MemJobPersister (defined in mem_job_persister.go).
type MemPersister struct {
	mu        sync.Mutex
	Nodes     map[string]*cpb.Node
	Workflows map[string]*cpb.Workflow
	Audits    []map[string]string
}

// NewMemPersister returns an initialised MemPersister.
func NewMemPersister() *MemPersister {
	return &MemPersister{
		Nodes:     make(map[string]*cpb.Node),
		Workflows: make(map[string]*cpb.Workflow),
	}
}

// Mu locks the MemPersister for direct field inspection in tests.
func (m *MemPersister) Mu() { m.mu.Lock() }

// MuUnlock releases the lock acquired by Mu.
func (m *MemPersister) MuUnlock() { m.mu.Unlock() }

func (m *MemPersister) SaveNode(_ context.Context, n *cpb.Node) error {
	cp := *n
	m.mu.Lock()
	m.Nodes[n.Address] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error) {
	m.mu.Lock()
	nodes := make([]*cpb.Node, 0, len(m.Nodes))
	for _, n := range m.Nodes {
		cp := *n
		nodes = append(nodes, &cp)
	}
	m.mu.Unlock()
	return nodes, nil
}

func (m *MemPersister) SaveWorkflow(_ context.Context, w *cpb.Workflow) error {
	cp := *w
	cpJobs := make([]cpb.WorkflowJob, len(w.Jobs))
	copy(cpJobs, w.Jobs)
	cp.Jobs = cpJobs
	m.mu.Lock()
	m.Workflows[w.ID] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemPersister) LoadAllWorkflows(_ context.Context) ([]*cpb.Workflow, error) {
	m.mu.Lock()
	workflows := make([]*cpb.Workflow, 0, len(m.Workflows))
	for _, w := range m.Workflows {
		cp := *w
		cpJobs := make([]cpb.WorkflowJob, len(w.Jobs))
		copy(cpJobs, w.Jobs)
		cp.Jobs = cpJobs
		workflows = append(workflows, &cp)
	}
	m.mu.Unlock()
	return workflows, nil
}

func (m *MemPersister) AppendAudit(_ context.Context, eventType, actor, target, detail string) error {
	m.mu.Lock()
	m.Audits = append(m.Audits, map[string]string{
		"event_type": eventType,
		"actor":      actor,
		"target":     target,
		"detail":     detail,
	})
	m.mu.Unlock()
	return nil
}
