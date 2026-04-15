// internal/grpcserver/testhelpers_test.go
//
// Shared mock implementations for grpcserver tests.

package grpcserver_test

import (
	"context"
	"errors"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── mockRevocationChecker ────────────────────────────────────────────────────

type mockRevocationChecker struct {
	revoked map[string]bool
}

func (m *mockRevocationChecker) IsRevoked(nodeID string) bool {
	return m.revoked[nodeID]
}

// ── mockRateLimiter ──────────────────────────────────────────────────────────

type mockRateLimiter struct {
	rate     float64
	blocked  map[string]bool
	hitCount map[string]int
}

func (m *mockRateLimiter) Allow(_ context.Context, nodeID string) error {
	if m.hitCount == nil {
		m.hitCount = make(map[string]int)
	}
	m.hitCount[nodeID]++
	if m.blocked[nodeID] {
		return status.Errorf(codes.ResourceExhausted, "rate limit exceeded for %s (%.1f rps)", nodeID, m.rate)
	}
	return nil
}

func (m *mockRateLimiter) GetRate() float64 { return m.rate }

// ── mockAuditLogger ──────────────────────────────────────────────────────────

type mockAuditLogger struct {
	rateLimitHits      int
	securityViolations int
	serviceEvents      int
}

func (m *mockAuditLogger) LogJobSubmit(_ context.Context, _, _, _ string) error { return nil }
func (m *mockAuditLogger) LogRateLimitHit(_ context.Context, _ string, _ float64) error {
	m.rateLimitHits++
	return nil
}
func (m *mockAuditLogger) LogSecurityViolation(_ context.Context, _, _, _ string) error {
	m.securityViolations++
	return nil
}
func (m *mockAuditLogger) LogServiceEvent(_ context.Context, _, _ string, _ bool, _ uint32, _ string, _ uint32) error {
	m.serviceEvents++
	return nil
}

// ── mockJobStore ─────────────────────────────────────────────────────────────

type mockJobStore struct {
	jobs      map[string]*cpb.Job
	submitErr error
	getErr    error
	transErr  error
	// lastTransitionOpts captures the most recent Transition call's
	// options so tests can verify the handler passed attested
	// outputs (not the raw node-reported slice) down to the store.
	lastTransitionOpts cluster.TransitionOptions
}

func newMockJobStore() *mockJobStore {
	return &mockJobStore{jobs: make(map[string]*cpb.Job)}
}

func (m *mockJobStore) Submit(_ context.Context, j *cpb.Job) error {
	if m.submitErr != nil {
		return m.submitErr
	}
	m.jobs[j.ID] = j
	return nil
}

func (m *mockJobStore) Get(jobID string) (*cpb.Job, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, errors.New("not found")
	}
	return j, nil
}

func (m *mockJobStore) Transition(_ context.Context, jobID string, to cpb.JobStatus, opts cluster.TransitionOptions) error {
	if m.transErr != nil {
		return m.transErr
	}
	m.lastTransitionOpts = opts
	if j, ok := m.jobs[jobID]; ok {
		j.Status = to
		// Mirror persistence: copy ResolvedOutputs onto the in-memory
		// Job so tests can inspect it via Get().
		if len(opts.ResolvedOutputs) > 0 {
			j.ResolvedOutputs = append(j.ResolvedOutputs[:0], opts.ResolvedOutputs...)
		}
	}
	return nil
}
