// internal/cluster/priority.go
//
// Priority calculation and priority-sorted job listing.
//
// Jobs are dispatched in order of effective priority (descending), then
// by CreatedAt (ascending, FIFO within same priority). The effective
// priority includes an age-based boost: +1 per minute pending, capped
// at 100. This prevents low-priority jobs from starving indefinitely.

package cluster

import (
	"sort"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// EffectivePriority returns the job's priority boosted by age.
// The boost is +1 per minute the job has been pending, capped at 100.
// This prevents starvation of low-priority jobs.
func EffectivePriority(job *cpb.Job) uint32 {
	base := job.Priority
	if base == 0 {
		base = 50
	}

	age := time.Since(job.CreatedAt)
	boost := uint32(age.Minutes())
	eff := base + boost
	if eff > 100 {
		return 100
	}
	return eff
}

// PendingByPriority returns all pending jobs sorted by effective priority
// (descending), then by CreatedAt (ascending, FIFO within same priority).
func (s *JobStore) PendingByPriority() []*cpb.Job {
	s.mu.RLock()
	var pending []*cpb.Job
	for _, j := range s.jobs {
		if j.Status == cpb.JobStatusPending {
			snap := *j
			pending = append(pending, &snap)
		}
	}
	s.mu.RUnlock()

	sort.Slice(pending, func(i, j int) bool {
		pi := EffectivePriority(pending[i])
		pj := EffectivePriority(pending[j])
		if pi != pj {
			return pi > pj // higher priority first
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt) // FIFO within same priority
	})

	return pending
}
