// internal/cluster/owner_principal_test.go
//
// Feature 36 — resource-ownership invariants.
//
// The preserve-owner invariant is the load-bearing safety property of
// this slice: every state transition, retry, and cancel path for a
// persisted resource must keep OwnerPrincipal pinned to the value
// stamped at creation. The legacy backfill contract is the other
// half: records persisted before feature 36 shipped must receive a
// non-empty OwnerPrincipal on load (either a synthesised
// `user:<sub>` from SubmittedBy or the `legacy:` sentinel) so the
// feature-37 policy evaluator never sees an ownerless record.
//
// Test inventory
// ──────────────
//   TestOwnerPrincipal_JobSubmitPersistsAndSurvivesTransitions
//   TestOwnerPrincipal_JobCancelPreservesOwner
//   TestOwnerPrincipal_WorkflowStartInheritsOnChildJobs
//   TestOwnerPrincipal_LegacyBackfill_SubmittedBySynthesisesUser
//   TestOwnerPrincipal_LegacyBackfill_MissingFieldsYieldsSentinel
//   TestOwnerPrincipal_WorkflowLegacyBackfill_MissingFieldsYieldsSentinel
//
// Every test drives the real JobStore / WorkflowStore with an
// in-memory persister — no mocks of the invariant's collaborators.

package cluster_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/principal"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Happy path: submit stamps, transitions preserve ────────────────────────

func TestOwnerPrincipal_JobSubmitPersistsAndSurvivesTransitions(t *testing.T) {
	s := newJobStore(t)
	ctx := context.Background()

	j := newJob("owner-life-1")
	j.OwnerPrincipal = "user:alice"
	j.SubmittedBy = "alice"

	if err := s.Submit(ctx, j); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Walk the full lifecycle; OwnerPrincipal must not change.
	steps := []struct {
		to   cpb.JobStatus
		opts cluster.TransitionOptions
	}{
		{cpb.JobStatusDispatching, cluster.TransitionOptions{NodeID: "node-1"}},
		{cpb.JobStatusRunning, cluster.TransitionOptions{}},
		{cpb.JobStatusCompleted, cluster.TransitionOptions{ExitCode: 0}},
	}
	for _, step := range steps {
		if err := s.Transition(ctx, j.ID, step.to, step.opts); err != nil {
			t.Fatalf("Transition → %s: %v", step.to, err)
		}
		got, err := s.Get(j.ID)
		if err != nil {
			t.Fatalf("Get after %s: %v", step.to, err)
		}
		if got.OwnerPrincipal != "user:alice" {
			t.Fatalf("after transition to %s: OwnerPrincipal = %q; want %q",
				step.to, got.OwnerPrincipal, "user:alice")
		}
	}
}

// ── Cancel preserves owner ─────────────────────────────────────────────────

func TestOwnerPrincipal_JobCancelPreservesOwner(t *testing.T) {
	s := newJobStore(t)
	ctx := context.Background()

	j := newJob("owner-cancel-1")
	j.OwnerPrincipal = "operator:alice@ops"
	if err := s.Submit(ctx, j); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := s.CancelJob(ctx, j.ID, "user requested"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	got, err := s.Get(j.ID)
	if err != nil {
		t.Fatalf("Get after cancel: %v", err)
	}
	if got.OwnerPrincipal != "operator:alice@ops" {
		t.Fatalf("after cancel: OwnerPrincipal = %q; want %q",
			got.OwnerPrincipal, "operator:alice@ops")
	}
}

// ── Workflow owner propagates to materialised child jobs ───────────────────

func TestOwnerPrincipal_WorkflowStartInheritsOnChildJobs(t *testing.T) {
	ctx := context.Background()
	jobStore := newJobStore(t)
	wfStore := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)

	wf := &cpb.Workflow{
		ID:             "wf-owner-1",
		Name:           "owner-test",
		OwnerPrincipal: "user:carol",
		Jobs: []cpb.WorkflowJob{
			{Name: "a", Command: "echo", Args: []string{"a"}},
			{Name: "b", Command: "echo", Args: []string{"b"}, DependsOn: []string{"a"}},
		},
	}
	if err := wfStore.Submit(ctx, wf); err != nil {
		t.Fatalf("wfStore.Submit: %v", err)
	}
	if err := wfStore.Start(ctx, wf.ID, jobStore); err != nil {
		t.Fatalf("wfStore.Start: %v", err)
	}

	// Every materialised child job inherits the workflow owner.
	got, err := wfStore.Get(wf.ID)
	if err != nil {
		t.Fatalf("wfStore.Get: %v", err)
	}
	if got.OwnerPrincipal != "user:carol" {
		t.Fatalf("workflow owner mutated: got %q; want %q",
			got.OwnerPrincipal, "user:carol")
	}
	for _, wj := range got.Jobs {
		child, err := jobStore.Get(wj.JobID)
		if err != nil {
			t.Fatalf("child %s Get: %v", wj.Name, err)
		}
		if child.OwnerPrincipal != "user:carol" {
			t.Fatalf("child %s OwnerPrincipal = %q; want %q",
				wj.Name, child.OwnerPrincipal, "user:carol")
		}
		// SubmittedBy is the bare-subject alias for back-compat with the
		// pre-feature-36 AUDIT L1 RBAC check.
		if child.SubmittedBy != "carol" {
			t.Fatalf("child %s SubmittedBy = %q; want %q (legacy alias)",
				wj.Name, child.SubmittedBy, "carol")
		}
	}
}

// ── Legacy backfill: SubmittedBy → user:<sub> ──────────────────────────────

func TestOwnerPrincipal_LegacyBackfill_SubmittedBySynthesisesUser(t *testing.T) {
	p := newTempBadgerPersister(t)

	// Write a pre-feature-36 job directly into the underlying
	// BadgerDB: SubmittedBy set, OwnerPrincipal empty. A
	// LoadAllJobs-driven restart must synthesise "user:alice".
	legacy := &cpb.Job{
		ID:          "legacy-with-submitter",
		Command:     "echo",
		SubmittedBy: "alice",
		Status:      cpb.JobStatusCompleted,
		// Deliberately empty: OwnerPrincipal.
	}
	writeRaw(t, p.DB(), "jobs/"+legacy.ID, legacy)

	jobs, err := p.LoadAllJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if got := jobs[0].OwnerPrincipal; got != "user:alice" {
		t.Fatalf("backfill: OwnerPrincipal = %q; want %q", got, "user:alice")
	}
}

// ── Legacy backfill: empty both → legacy: sentinel ─────────────────────────

func TestOwnerPrincipal_LegacyBackfill_MissingFieldsYieldsSentinel(t *testing.T) {
	p := newTempBadgerPersister(t)

	legacy := &cpb.Job{
		ID:      "legacy-ownerless",
		Command: "echo",
		Status:  cpb.JobStatusCompleted,
	}
	writeRaw(t, p.DB(), "jobs/"+legacy.ID, legacy)

	jobs, err := p.LoadAllJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if got := jobs[0].OwnerPrincipal; got != principal.LegacyOwnerID {
		t.Fatalf("backfill: OwnerPrincipal = %q; want %q", got, principal.LegacyOwnerID)
	}
}

// ── Legacy workflow backfill ───────────────────────────────────────────────

func TestOwnerPrincipal_WorkflowLegacyBackfill_MissingFieldsYieldsSentinel(t *testing.T) {
	p := newTempBadgerPersister(t)

	legacy := &cpb.Workflow{
		ID:     "wf-legacy-ownerless",
		Name:   "legacy",
		Status: cpb.WorkflowStatusPending,
		Jobs:   []cpb.WorkflowJob{{Name: "a", Command: "echo"}},
	}
	writeRaw(t, p.DB(), "workflows/"+legacy.ID, legacy)

	wfs, err := p.LoadAllWorkflows(context.Background())
	if err != nil {
		t.Fatalf("LoadAllWorkflows: %v", err)
	}
	if len(wfs) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(wfs))
	}
	if got := wfs[0].OwnerPrincipal; got != principal.LegacyOwnerID {
		t.Fatalf("backfill: OwnerPrincipal = %q; want %q", got, principal.LegacyOwnerID)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func newTempBadgerPersister(t *testing.T) *cluster.BadgerJSONPersister {
	t.Helper()
	p, err := cluster.NewBadgerJSONPersister(t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("NewBadgerJSONPersister: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func writeRaw(t *testing.T, db *badger.DB, key string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if err := db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), raw)
	}); err != nil {
		t.Fatalf("raw Set %s: %v", key, err)
	}
}
