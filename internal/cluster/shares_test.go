// internal/cluster/shares_test.go
//
// Coverage for JobStore.UpdateShares / WorkflowStore.UpdateShares
// + MemWorkflowPersister.AppendAudit + ServiceRegistry.Snapshot.
// These are feature-38 / feature-17 primitives that the broader
// integration tests exercise implicitly; direct unit coverage
// here protects the contract if the ownership-preservation
// invariant ever regresses.

package cluster_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/authz"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── JobStore.UpdateShares ────────────────────────────────────

func TestJobStore_UpdateShares_ReplacesShares(t *testing.T) {
	store := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ctx := context.Background()

	_ = store.Submit(ctx, &cpb.Job{ID: "j1", Command: "echo", Status: cpb.JobStatusPending, OwnerPrincipal: "user:alice"})

	shares := []authz.Share{
		{Grantee: "user:bob", Actions: []authz.Action{authz.ActionRead}},
	}
	if err := store.UpdateShares(ctx, "j1", shares); err != nil {
		t.Fatalf("UpdateShares: %v", err)
	}

	got, err := store.Get("j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Shares) != 1 || got.Shares[0].Grantee != "user:bob" {
		t.Errorf("shares not persisted: %+v", got.Shares)
	}
}

func TestJobStore_UpdateShares_EmptyIsValid(t *testing.T) {
	store := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ctx := context.Background()
	_ = store.Submit(ctx, &cpb.Job{ID: "j1", Command: "echo", OwnerPrincipal: "user:alice",
		Shares: []authz.Share{{Grantee: "user:bob", Actions: []authz.Action{authz.ActionRead}}}})

	if err := store.UpdateShares(ctx, "j1", nil); err != nil {
		t.Fatalf("UpdateShares(nil): %v", err)
	}
	got, _ := store.Get("j1")
	if len(got.Shares) != 0 {
		t.Errorf("shares not cleared: %+v", got.Shares)
	}
}

func TestJobStore_UpdateShares_MissingJob_ErrJobNotFound(t *testing.T) {
	store := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	err := store.UpdateShares(context.Background(), "nope", nil)
	if !errors.Is(err, cluster.ErrJobNotFound) {
		t.Errorf("want ErrJobNotFound, got %v", err)
	}
}

func TestJobStore_UpdateShares_DefensiveCopy(t *testing.T) {
	// Caller mutating the slice after UpdateShares must not
	// bleed into the stored Shares — that's the defensive-copy
	// invariant the handler chain relies on.
	store := cluster.NewJobStore(cluster.NewMemJobPersister(), nil)
	ctx := context.Background()
	_ = store.Submit(ctx, &cpb.Job{ID: "j1", Command: "echo", OwnerPrincipal: "user:alice"})

	shares := []authz.Share{
		{Grantee: "user:bob", Actions: []authz.Action{authz.ActionRead}},
	}
	_ = store.UpdateShares(ctx, "j1", shares)
	shares[0].Grantee = "user:MUTATED"

	got, _ := store.Get("j1")
	if got.Shares[0].Grantee != "user:bob" {
		t.Errorf("defensive copy broken: %q", got.Shares[0].Grantee)
	}
}

// ── WorkflowStore.UpdateShares ───────────────────────────────

func TestWorkflowStore_UpdateShares_ReplacesShares(t *testing.T) {
	store := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	ctx := context.Background()

	err := store.Submit(ctx, &cpb.Workflow{
		ID:             "wf1",
		Name:           "my-wf",
		OwnerPrincipal: "user:alice",
		Jobs: []cpb.WorkflowJob{
			{Name: "job1", Command: "echo"},
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	shares := []authz.Share{
		{Grantee: "group:ml-team", Actions: []authz.Action{authz.ActionRead, authz.ActionWrite}},
	}
	if err := store.UpdateShares(ctx, "wf1", shares); err != nil {
		t.Fatalf("UpdateShares: %v", err)
	}

	got, err := store.Get("wf1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Shares) != 1 || got.Shares[0].Grantee != "group:ml-team" {
		t.Errorf("shares not persisted: %+v", got.Shares)
	}
}

func TestWorkflowStore_UpdateShares_MissingWorkflow_ErrWorkflowNotFound(t *testing.T) {
	store := cluster.NewWorkflowStore(cluster.NewMemWorkflowPersister(), nil)
	err := store.UpdateShares(context.Background(), "nope", nil)
	if !errors.Is(err, cluster.ErrWorkflowNotFound) {
		t.Errorf("want ErrWorkflowNotFound, got %v", err)
	}
}

// ── MemWorkflowPersister.AppendAudit ─────────────────────────

func TestMemWorkflowPersister_AppendAudit_RecordsEntry(t *testing.T) {
	p := cluster.NewMemWorkflowPersister()
	err := p.AppendAudit(context.Background(),
		"workflow.cancelled", "alice", "wf-1", "cancel reason: operator")
	if err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if len(p.Audits) != 1 {
		t.Errorf("Audits: got %d, want 1", len(p.Audits))
	}
}

// ── ServiceRegistry.Snapshot ─────────────────────────────────

func TestServiceRegistry_Snapshot_Empty_ReturnsEmpty(t *testing.T) {
	r := cluster.NewServiceRegistry()
	got := r.Snapshot()
	if got == nil {
		t.Error("Snapshot on empty must return empty slice, not nil")
	}
	if len(got) != 0 {
		t.Errorf("Snapshot: got %d, want 0", len(got))
	}
}

func TestServiceRegistry_Snapshot_Populated_ReturnsCopy(t *testing.T) {
	r := cluster.NewServiceRegistry()
	r.Upsert(cpb.ServiceEndpoint{JobID: "j1", NodeAddress: "n1:8080", Port: 8080})
	r.Upsert(cpb.ServiceEndpoint{JobID: "j2", NodeAddress: "n2:8080", Port: 8080})

	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot: got %d, want 2", len(got))
	}

	// Mutating the returned slice must not change the registry
	// state — defensive copy contract.
	got[0].NodeAddress = "MUTATED"
	ep, ok := r.Get(got[0].JobID)
	if !ok {
		t.Fatal("registry lost entry after Snapshot mutation")
	}
	if ep.NodeAddress == "MUTATED" {
		t.Error("Snapshot returned shared slice; defensive copy broken")
	}
}
