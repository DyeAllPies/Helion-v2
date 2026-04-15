# Deferred: Service jobs in workflow specs

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 17 — ML inference jobs](../implemented/17-ml-inference-jobs.md)
**Audit reference:** [2026-04-14-02](../../audits/2026-04-14-02.md) finding M1

## Context

Feature 17 added `ServiceSpec` to `SubmitRequest` (plain `POST /jobs`) but did **not** plumb it through the workflow job spec (`internal/api/handlers_workflows.go:WorkflowJobSpec`). A user who wants to express a `train → register → serve` DAG inline must today submit the `serve` job separately with `POST /jobs` after the workflow completes — awkward, and it breaks the "one workflow owns the whole pipeline" story the feature-10 overview promises.

The plumbing change is small (three fields + a matching `Job.Service` assignment in the workflow job-creation path, mirroring how `WorkingDir` / `Inputs` / `Outputs` / `NodeSelector` are already handled). The reason to defer is a design question, not an implementation one.

## Why deferred

A service job never reaches a terminal state on its own — that's the point. A workflow DAG with a service-job node will therefore:

1. Never trigger downstream jobs that `depends_on` the service.
2. Never mark the workflow as `Completed`. Workflow lifetime becomes indistinguishable from service lifetime, which is probably not what the user wants — workflows are scoped to "pipeline runs," services are scoped to "until I cancel them."

There are three plausible resolutions:

1. **Reject service jobs in workflow specs at validate time.** Consistent but forces the `train → serve` pattern into two submits.
2. **Treat a ready-and-healthy service as "Completed" for DAG purposes.** Matches the user's mental model ("the serve step is done once the service is up") but requires new workflow state machine transitions (`ServeReady`?) and makes workflow-cancel semantics fuzzy (does cancelling a completed workflow stop its services?).
3. **Allow services as workflow leaves only.** Equivalent to (2) but restricted: a service node may not have any downstream dependents. Simpler to implement.

None of the three is obviously correct, and the feature-10 iris demo (step 9) can work fine with two separate submits. Revisit when either an operator asks, or when step 19's end-to-end demo gets written and shows the awkwardness in practice.

## Revisit trigger

- An operator files a "I wanted `services` in my workflow YAML and it silently got ignored" issue (the current behaviour is not ideal — JSON schema accepts unknown fields, so `service` in a workflow silently drops today).
- Or: step 19 (iris end-to-end demo) tries to drive the whole pipeline from a single `workflows.yaml` and the two-submit workaround becomes the thing the example spends half its prose explaining.

When either trigger fires, option (3) — services allowed as workflow leaves only — is the likely implementation shape.
