> **Audience:** users
> **Scope:** Index for user-facing guides — writing workflows and submitting jobs.
> **Depth:** guide

# Guides

User-facing guides for writing workflows and submitting jobs to Helion.
For operator / engineer material, see [`../operators/`](../operators/) and
[`../architecture/`](../architecture/).

## Files in this folder

| File | Contents |
|---|---|
| [ml-pipelines.md](ml-pipelines.md) | Training → registry → serve ML pipeline walkthrough, built around the iris reference pipeline. |
| workflows.md | Workflow YAML syntax, DAG semantics, retry + priority. *Planned in feature 44 — not yet written; see [../architecture/protocols.md](../architecture/protocols.md) for the REST contract and [../planned-features/implemented/01-workflow-dag.md](../planned-features/implemented/01-workflow-dag.md) for the DAG engine design.* |
| submitting-jobs.md | CLI + REST submission patterns. *Planned in feature 44 — not yet written; until then the one-page quickstart lives in the top-level [`../README.md`](../README.md).* |
