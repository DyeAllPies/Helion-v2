# Feature: Workflow / DAG Support

**Priority:** P0
**Status:** Implemented
**Affected files:** `internal/proto/coordinatorpb/types.go`, `internal/cluster/workflow*.go`, `internal/cluster/dag.go`, `internal/api/handlers_workflows.go`, `internal/cluster/dispatch.go`

## Problem

Jobs are independent atomic units today. There is no way to express "run B after A completes" or fan-out/fan-in patterns. Users must poll job status externally and submit downstream jobs manually. This makes Helion unsuitable for multi-step workloads (build pipelines, ETL, ML training).

## Current state

- `Job` proto has no `depends_on`, `workflow_id`, or similar fields
- `DispatchLoop` treats all pending jobs equally (no ordering guarantees)
- No topological sort or dependency resolution exists anywhere

## Design

### Core concepts

```
Workflow
  ├── id: string (unique)
  ├── name: string
  ├── jobs: []WorkflowJob
  └── status: pending | running | completed | failed

WorkflowJob
  ├── name: string (unique within workflow, e.g. "build")
  ├── command, args, env, timeout_seconds, runtime
  ├── depends_on: []string (names of upstream jobs)
  └── retry_policy: (see 02-retry-failure-policies.md)
```

### Dependency resolution

1. On `POST /workflows`, validate the DAG:
   - Build adjacency list from `depends_on` references
   - Run cycle detection (Kahn's algorithm or DFS)
   - Reject with 400 if cycles found or unknown references exist
2. Persist the workflow and all its jobs (status: `pending`)
3. Jobs with empty `depends_on` are immediately eligible for dispatch

### Dispatch integration

The existing `DispatchLoop` gains a new check before dispatching a pending job:

```
for each pending job:
  if job belongs to a workflow:
    if any upstream job is not in terminal-success state:
      skip (not yet eligible)
    if any upstream job failed and no retry remaining:
      mark this job and all downstream as "skipped"
      continue
  dispatch as normal
```

This keeps the dispatch loop simple — it still polls pending jobs on a timer, but filters by dependency readiness.

### Conditional execution

Support basic conditions on edges:

- `on_success` (default) — run downstream only if upstream succeeded
- `on_failure` — run downstream only if upstream failed (cleanup jobs)
- `on_complete` — run downstream regardless of upstream result

### Fan-out / fan-in

A job with multiple `depends_on` entries waits for ALL of them (AND semantics). Fan-out is implicit: multiple jobs can list the same upstream dependency.

```yaml
# Example: build → [test, lint] → deploy
jobs:
  - name: build
  - name: test
    depends_on: [build]
  - name: lint
    depends_on: [build]
  - name: deploy
    depends_on: [test, lint]
```

## Proto changes

```protobuf
message Workflow {
  string id = 1;
  string name = 2;
  repeated WorkflowJob jobs = 3;
  WorkflowStatus status = 4;
  google.protobuf.Timestamp created_at = 5;
  google.protobuf.Timestamp finished_at = 6;
}

message WorkflowJob {
  string name = 1;
  string command = 2;
  repeated string args = 3;
  map<string, string> env = 4;
  uint32 timeout_seconds = 5;
  string runtime = 6;
  repeated string depends_on = 7;
  DependencyCondition condition = 8;
  RetryPolicy retry_policy = 9;
}

enum WorkflowStatus {
  WORKFLOW_STATUS_UNSPECIFIED = 0;
  WORKFLOW_STATUS_PENDING = 1;
  WORKFLOW_STATUS_RUNNING = 2;
  WORKFLOW_STATUS_COMPLETED = 3;
  WORKFLOW_STATUS_FAILED = 4;
}

enum DependencyCondition {
  DEPENDENCY_ON_SUCCESS = 0;
  DEPENDENCY_ON_FAILURE = 1;
  DEPENDENCY_ON_COMPLETE = 2;
}
```

## API changes

| Method | Path | Description |
|--------|------|-------------|
| POST | `/workflows` | Submit a new workflow (validates DAG) |
| GET | `/workflows/{id}` | Get workflow status + all job statuses |
| GET | `/workflows` | List workflows (paginated) |
| DELETE | `/workflows/{id}` | Cancel a running workflow |

## New internal packages

### `internal/cluster/workflow.go`

- `WorkflowStore` — CRUD for workflows in BadgerDB (key prefix: `workflow:`)
- `ValidateDAG(jobs)` — cycle detection, reference validation
- `EligibleJobs(workflowID)` — returns jobs whose dependencies are satisfied

### `internal/cluster/dag.go`

- `Graph` struct with adjacency list
- `TopologicalSort()` — returns execution order
- `DetectCycles()` — returns error with cycle path
- `Descendants(jobName)` — for cascading skip/cancel

## Implementation order

1. Proto definitions + `dag.go` with cycle detection (pure logic, easy to test)
2. `WorkflowStore` with BadgerDB persistence
3. API endpoints for workflow CRUD
4. Dispatch loop integration (eligibility check)
5. Cascading failure/skip logic
6. Dashboard workflow visualization

## Open questions

- Should workflows support parameters/templating? (Defer — adds complexity without core value)
- Should a workflow be re-runnable (re-trigger from a failed step)? (Yes, deferred)
- Max workflow size? 100 jobs per workflow (enforced in API validation)

## Implementation status

All items from the implementation order have been completed:

1. **Types + DAG validation** — `internal/proto/coordinatorpb/types.go` (Workflow, WorkflowJob, WorkflowStatus, DependencyCondition), `internal/cluster/dag.go` (ValidateDAG, TopologicalSort, Descendants, RootJobs)
2. **WorkflowStore** — `internal/cluster/workflow.go`, `workflow_submit.go`, `workflow_lifecycle.go`, `workflow_read.go`
3. **Persistence** — `BadgerJSONPersister.SaveWorkflow/LoadAllWorkflows` in `internal/cluster/persistence.go`
4. **API endpoints** — `internal/api/handlers_workflows.go` (POST/GET/DELETE /workflows)
5. **Dispatch loop integration** — `internal/cluster/dispatch.go` (dependency eligibility gating)
6. **Cascading failure** — `WorkflowStore.OnJobCompleted` with `Descendants()` traversal
7. **gRPC callback** — `grpcserver.WithJobCompletionCallback` for workflow notification
8. **Dashboard** — `WorkflowListComponent`, `WorkflowDetailComponent`, API service methods, routing, nav
9. **Tests** — 15 DAG unit tests, 16 workflow lifecycle tests, 6 E2E integration tests
