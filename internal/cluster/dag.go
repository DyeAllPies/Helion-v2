// internal/cluster/dag.go
//
// DAG validation for workflow job dependencies.
//
// Provides cycle detection (Kahn's algorithm), reference validation, and
// topological sort. All functions are pure — no side effects, no I/O.

package cluster

import (
	"errors"
	"fmt"
	"strings"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrDAGCycle         = errors.New("dag: cycle detected")
	ErrDAGUnknownDep    = errors.New("dag: unknown dependency")
	ErrDAGDuplicateName = errors.New("dag: duplicate job name")
	ErrDAGEmptyName     = errors.New("dag: job name must not be empty")
	ErrDAGSelfDep       = errors.New("dag: job depends on itself")
)

// ── ValidateDAG ──────────────────────────────────────────────────────────────

// ValidateDAG checks that the workflow jobs form a valid directed acyclic graph.
//
// It validates:
//   - All job names are non-empty and unique
//   - All depends_on references point to existing job names
//   - No job depends on itself
//   - The graph contains no cycles
//
// Returns nil if the DAG is valid.
func ValidateDAG(jobs []cpb.WorkflowJob) error {
	if len(jobs) == 0 {
		return nil
	}

	// Build name set and check for duplicates / empty names.
	names := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		if j.Name == "" {
			return ErrDAGEmptyName
		}
		if names[j.Name] {
			return fmt.Errorf("%w: %q", ErrDAGDuplicateName, j.Name)
		}
		names[j.Name] = true
	}

	// Validate all dependency references exist and no self-deps.
	for _, j := range jobs {
		for _, dep := range j.DependsOn {
			if dep == j.Name {
				return fmt.Errorf("%w: %q", ErrDAGSelfDep, j.Name)
			}
			if !names[dep] {
				return fmt.Errorf("%w: %q references %q", ErrDAGUnknownDep, j.Name, dep)
			}
		}
	}

	// Cycle detection via Kahn's algorithm (topological sort).
	_, err := TopologicalSort(jobs)
	return err
}

// ── TopologicalSort ──────────────────────────────────────────────────────────

// TopologicalSort returns the job names in an order such that every job appears
// after all of its dependencies. Returns ErrDAGCycle if the graph contains a
// cycle.
//
// Uses Kahn's algorithm: repeatedly remove nodes with no incoming edges.
func TopologicalSort(jobs []cpb.WorkflowJob) ([]string, error) {
	// Build adjacency list and in-degree map.
	inDegree := make(map[string]int, len(jobs))
	downstream := make(map[string][]string, len(jobs))

	for _, j := range jobs {
		if _, ok := inDegree[j.Name]; !ok {
			inDegree[j.Name] = 0
		}
		for _, dep := range j.DependsOn {
			downstream[dep] = append(downstream[dep], j.Name)
			inDegree[j.Name]++
		}
	}

	// Seed queue with nodes that have no dependencies.
	queue := make([]string, 0)
	for _, j := range jobs {
		if inDegree[j.Name] == 0 {
			queue = append(queue, j.Name)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, name)

		for _, child := range downstream[name] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if len(sorted) != len(jobs) {
		// Find the cycle members for a useful error message.
		var cycleNodes []string
		for name, deg := range inDegree {
			if deg > 0 {
				cycleNodes = append(cycleNodes, name)
			}
		}
		return nil, fmt.Errorf("%w: involves %s", ErrDAGCycle, strings.Join(cycleNodes, ", "))
	}

	return sorted, nil
}

// ── Descendants ──────────────────────────────────────────────────────────────

// Descendants returns all job names that are transitively downstream of the
// given job name (not including the job itself). Used for cascading skip/cancel.
func Descendants(jobs []cpb.WorkflowJob, name string) []string {
	// Build downstream adjacency list.
	downstream := make(map[string][]string, len(jobs))
	for _, j := range jobs {
		for _, dep := range j.DependsOn {
			downstream[dep] = append(downstream[dep], j.Name)
		}
	}

	// BFS from the given name.
	visited := make(map[string]bool)
	queue := []string{name}
	var result []string

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, child := range downstream[curr] {
			if !visited[child] {
				visited[child] = true
				result = append(result, child)
				queue = append(queue, child)
			}
		}
	}

	return result
}

// ── RootJobs ─────────────────────────────────────────────────────────────────

// RootJobs returns the names of jobs that have no dependencies (DAG roots).
// These are the jobs eligible for immediate dispatch when a workflow starts.
func RootJobs(jobs []cpb.WorkflowJob) []string {
	var roots []string
	for _, j := range jobs {
		if len(j.DependsOn) == 0 {
			roots = append(roots, j.Name)
		}
	}
	return roots
}
