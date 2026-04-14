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

	// ErrDAGUnknownFrom fires when an input's `from` reference names
	// a job that doesn't exist in the workflow.
	ErrDAGUnknownFrom = errors.New("dag: from references unknown upstream job")

	// ErrDAGFromNotAncestor fires when an input's `from` reference
	// points at a job this one does not (transitively) depend on.
	// Resolution order would not guarantee the upstream has completed.
	ErrDAGFromNotAncestor = errors.New("dag: from references non-ancestor job")

	// ErrDAGFromUnknownOutput fires when a valid upstream is named
	// but does not declare an output with the requested name.
	ErrDAGFromUnknownOutput = errors.New("dag: from references unknown output")

	// ErrDAGFromConditionUnreachable fires when a workflow job carries
	// a `from:` reference while its dependency Condition is
	// on_failure or on_complete. The stager only uploads outputs on
	// successful runs, so `from:` against anything except an
	// on_success dependency is guaranteed to fail resolution at
	// dispatch time — better to reject at submit with a clear error
	// than to let it crash in production.
	ErrDAGFromConditionUnreachable = errors.New("dag: from unreachable under non-success condition")
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
	if _, err := TopologicalSort(jobs); err != nil {
		return err
	}

	// Step-3 artifact references: every `from` must name an ancestor
	// job with a declared output of the same name. Run this last so
	// the ancestor check can assume the graph is acyclic.
	return validateFromReferences(jobs)
}

// validateFromReferences walks every input with a `from` reference and
// checks:
//
//  1. The upstream job named in the reference exists in this workflow.
//  2. The upstream is an ancestor (transitively depended on) of the
//     current job — otherwise the scheduler would race.
//  3. The upstream declares an output with the referenced name.
//
// The function is pure and runs after cycle detection, so the ancestor
// walk cannot loop.
func validateFromReferences(jobs []cpb.WorkflowJob) error {
	byName := make(map[string]*cpb.WorkflowJob, len(jobs))
	for i := range jobs {
		byName[jobs[i].Name] = &jobs[i]
	}
	// Cache ancestors per job name so repeated `from` references don't
	// re-walk the DAG.
	ancestorsOf := func(target string) map[string]struct{} {
		seen := make(map[string]struct{})
		stack := append([]string(nil), byName[target].DependsOn...)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			if up, ok := byName[n]; ok {
				stack = append(stack, up.DependsOn...)
			}
		}
		return seen
	}

	for _, j := range jobs {
		var ancestors map[string]struct{}
		for _, in := range j.Inputs {
			if in.From == "" {
				continue
			}
			// A from-reference is only reachable when the downstream
			// runs because the upstream succeeded — the stager skips
			// uploads on failure, so `from:` under on_failure or
			// on_complete is guaranteed to fail resolution at
			// dispatch time. Reject at submit instead of producing a
			// confusing ErrResolveOutputMissing later.
			if j.Condition != cpb.DependencyOnSuccess {
				return fmt.Errorf("%w: job %q input %q → %q: job's condition is %s, but from-references need on_success so the upstream's outputs exist",
					ErrDAGFromConditionUnreachable, j.Name, in.Name, in.From, j.Condition)
			}
			upstream, outName := splitDotRef(in.From)
			if upstream == "" {
				// Shape is enforced in the API validator; defend
				// against library-level callers who skipped it.
				return fmt.Errorf(`dag: job %q: input %q: from must be "<upstream>.<output>"`, j.Name, in.Name)
			}
			upJob, ok := byName[upstream]
			if !ok {
				return fmt.Errorf("%w: job %q input %q → %q", ErrDAGUnknownFrom, j.Name, in.Name, upstream)
			}
			if ancestors == nil {
				ancestors = ancestorsOf(j.Name)
			}
			if _, isAncestor := ancestors[upstream]; !isAncestor {
				return fmt.Errorf("%w: job %q input %q → %q (add it to depends_on)",
					ErrDAGFromNotAncestor, j.Name, in.Name, upstream)
			}
			foundOutput := false
			for _, out := range upJob.Outputs {
				if out.Name == outName {
					foundOutput = true
					break
				}
			}
			if !foundOutput {
				return fmt.Errorf("%w: job %q input %q → %q.%q",
					ErrDAGFromUnknownOutput, j.Name, in.Name, upstream, outName)
			}
		}
	}
	return nil
}

// splitDotRef splits "<upstream>.<output>" at the LAST '.'. Returns
// empty strings for malformed input so the caller can surface a
// shape error. Mirrors api.SplitFromRef but lives in the cluster
// package to avoid an import cycle.
func splitDotRef(ref string) (upstream, output string) {
	if ref == "" {
		return "", ""
	}
	dot := strings.LastIndexByte(ref, '.')
	if dot <= 0 || dot == len(ref)-1 {
		return "", ""
	}
	return ref[:dot], ref[dot+1:]
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
