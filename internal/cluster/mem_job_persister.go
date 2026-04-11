// internal/cluster/mem_job_persister.go
//
// MemJobPersister is an in-memory JobPersister used by tests. Production
// code uses BadgerJobPersister.

package cluster

import (
	"context"
	"fmt"
	"sync"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// MemJobPersister is an in-memory JobPersister for tests.
// All operations are safe for concurrent use.
type MemJobPersister struct {
	mu     sync.Mutex
	Jobs   map[string]*cpb.Job
	Audits []string
}

// NewMemJobPersister returns an initialised MemJobPersister.
func NewMemJobPersister() *MemJobPersister {
	return &MemJobPersister{Jobs: make(map[string]*cpb.Job)}
}

func (m *MemJobPersister) SaveJob(_ context.Context, j *cpb.Job) error {
	cp := *j
	m.mu.Lock()
	m.Jobs[j.ID] = &cp
	m.mu.Unlock()
	return nil
}

func (m *MemJobPersister) LoadAllJobs(_ context.Context) ([]*cpb.Job, error) {
	m.mu.Lock()
	out := make([]*cpb.Job, 0, len(m.Jobs))
	for _, j := range m.Jobs {
		cp := *j
		out = append(out, &cp)
	}
	m.mu.Unlock()
	return out, nil
}

func (m *MemJobPersister) AppendAudit(_ context.Context, eventType, _, target, detail string) error {
	m.mu.Lock()
	m.Audits = append(m.Audits, fmt.Sprintf("%s target=%s %s", eventType, target, detail))
	m.mu.Unlock()
	return nil
}

// AllJobs returns a point-in-time copy of all persisted jobs (for assertions).
func (m *MemJobPersister) AllJobs() map[string]*cpb.Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*cpb.Job, len(m.Jobs))
	for k, v := range m.Jobs {
		cp := *v
		out[k] = &cp
	}
	return out
}
