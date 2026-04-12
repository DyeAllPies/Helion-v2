package cluster_test

import (
	"errors"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── ValidateDAG ──────────────────────────────────────────────────────────────

func TestValidateDAG_EmptyJobs(t *testing.T) {
	if err := cluster.ValidateDAG(nil); err != nil {
		t.Fatalf("expected nil error for empty jobs, got %v", err)
	}
}

func TestValidateDAG_SingleJob(t *testing.T) {
	jobs := []cpb.WorkflowJob{{Name: "build", Command: "make"}}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateDAG_LinearChain(t *testing.T) {
	// A → B → C
	jobs := []cpb.WorkflowJob{
		{Name: "a", Command: "echo"},
		{Name: "b", Command: "echo", DependsOn: []string{"a"}},
		{Name: "c", Command: "echo", DependsOn: []string{"b"}},
	}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateDAG_DiamondShape(t *testing.T) {
	//     build
	//    /     \
	//  test    lint
	//    \     /
	//    deploy
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "test", Command: "make", DependsOn: []string{"build"}},
		{Name: "lint", Command: "make", DependsOn: []string{"build"}},
		{Name: "deploy", Command: "make", DependsOn: []string{"test", "lint"}},
	}
	if err := cluster.ValidateDAG(jobs); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateDAG_EmptyName(t *testing.T) {
	jobs := []cpb.WorkflowJob{{Name: "", Command: "echo"}}
	if !errors.Is(cluster.ValidateDAG(jobs), cluster.ErrDAGEmptyName) {
		t.Fatal("expected ErrDAGEmptyName")
	}
}

func TestValidateDAG_DuplicateName(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "build", Command: "make"},
	}
	if !errors.Is(cluster.ValidateDAG(jobs), cluster.ErrDAGDuplicateName) {
		t.Fatal("expected ErrDAGDuplicateName")
	}
}

func TestValidateDAG_UnknownDependency(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "test", Command: "make", DependsOn: []string{"nonexistent"}},
	}
	if !errors.Is(cluster.ValidateDAG(jobs), cluster.ErrDAGUnknownDep) {
		t.Fatal("expected ErrDAGUnknownDep")
	}
}

func TestValidateDAG_SelfDependency(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make", DependsOn: []string{"build"}},
	}
	if !errors.Is(cluster.ValidateDAG(jobs), cluster.ErrDAGSelfDep) {
		t.Fatal("expected ErrDAGSelfDep")
	}
}

func TestValidateDAG_DirectCycle(t *testing.T) {
	// A → B → A
	jobs := []cpb.WorkflowJob{
		{Name: "a", Command: "echo", DependsOn: []string{"b"}},
		{Name: "b", Command: "echo", DependsOn: []string{"a"}},
	}
	if !errors.Is(cluster.ValidateDAG(jobs), cluster.ErrDAGCycle) {
		t.Fatal("expected ErrDAGCycle")
	}
}

func TestValidateDAG_IndirectCycle(t *testing.T) {
	// A → B → C → A
	jobs := []cpb.WorkflowJob{
		{Name: "a", Command: "echo", DependsOn: []string{"c"}},
		{Name: "b", Command: "echo", DependsOn: []string{"a"}},
		{Name: "c", Command: "echo", DependsOn: []string{"b"}},
	}
	if !errors.Is(cluster.ValidateDAG(jobs), cluster.ErrDAGCycle) {
		t.Fatal("expected ErrDAGCycle")
	}
}

// ── TopologicalSort ──────────────────────────────────────────────────────────

func TestTopologicalSort_LinearChain(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "a", Command: "echo"},
		{Name: "b", Command: "echo", DependsOn: []string{"a"}},
		{Name: "c", Command: "echo", DependsOn: []string{"b"}},
	}
	order, err := cluster.TopologicalSort(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// a must come before b, b before c
	idx := make(map[string]int, len(order))
	for i, name := range order {
		idx[name] = i
	}
	if idx["a"] >= idx["b"] || idx["b"] >= idx["c"] {
		t.Fatalf("invalid order: %v", order)
	}
}

func TestTopologicalSort_Diamond(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "test", Command: "make", DependsOn: []string{"build"}},
		{Name: "lint", Command: "make", DependsOn: []string{"build"}},
		{Name: "deploy", Command: "make", DependsOn: []string{"test", "lint"}},
	}
	order, err := cluster.TopologicalSort(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	idx := make(map[string]int, len(order))
	for i, name := range order {
		idx[name] = i
	}
	if idx["build"] >= idx["test"] || idx["build"] >= idx["lint"] {
		t.Fatal("build must come before test and lint")
	}
	if idx["test"] >= idx["deploy"] || idx["lint"] >= idx["deploy"] {
		t.Fatal("test and lint must come before deploy")
	}
}

// ── Descendants ──────────────────────────────────────────────────────────────

func TestDescendants_Root(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "test", Command: "make", DependsOn: []string{"build"}},
		{Name: "lint", Command: "make", DependsOn: []string{"build"}},
		{Name: "deploy", Command: "make", DependsOn: []string{"test", "lint"}},
	}
	desc := cluster.Descendants(jobs, "build")
	descSet := make(map[string]bool)
	for _, d := range desc {
		descSet[d] = true
	}
	if !descSet["test"] || !descSet["lint"] || !descSet["deploy"] {
		t.Fatalf("expected test, lint, deploy as descendants of build, got %v", desc)
	}
}

func TestDescendants_Leaf(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "deploy", Command: "make", DependsOn: []string{"build"}},
	}
	desc := cluster.Descendants(jobs, "deploy")
	if len(desc) != 0 {
		t.Fatalf("expected no descendants for leaf, got %v", desc)
	}
}

// ── RootJobs ─────────────────────────────────────────────────────────────────

func TestRootJobs(t *testing.T) {
	jobs := []cpb.WorkflowJob{
		{Name: "build", Command: "make"},
		{Name: "setup", Command: "make"},
		{Name: "test", Command: "make", DependsOn: []string{"build", "setup"}},
	}
	roots := cluster.RootJobs(jobs)
	rootSet := make(map[string]bool)
	for _, r := range roots {
		rootSet[r] = true
	}
	if !rootSet["build"] || !rootSet["setup"] {
		t.Fatalf("expected build and setup as roots, got %v", roots)
	}
	if rootSet["test"] {
		t.Fatal("test should not be a root")
	}
}
