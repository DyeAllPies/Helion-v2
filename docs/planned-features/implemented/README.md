# Implemented Features

Feature specs that have fully shipped, including any audit-pass
follow-ups and deferred-item closures. They move here (keeping their
original number) instead of being deleted — the original spec, the
"what shipped vs what was promised" reconciliation, and the pointers
to any items spun off into `../deferred/` are all useful history for
future reviewers.

## Files here

- `NN-kebab-slug.md` — one file per landed feature. Keeps the
  original number so cross-references from audit files, commit
  messages, or code comments still resolve without renumbering.
- `README.md` — this index.

## Workflow

When a feature reaches the "fully implemented" bar:

1. Audit the original spec line-by-line. Every promised item is
   either shipped, fixed inline, or filed under `../deferred/` with
   a written rationale + revisit trigger.
2. Update the feature file's "Status:" line to **Done** and add a
   short "What shipped" / "Audit-pass deferrals" section so the
   reconciliation against the original spec is visible at a glance.
3. `git mv docs/planned-features/NN-slug.md docs/planned-features/implemented/NN-slug.md`
4. Fix the relative paths inside the moved file (sibling-feature
   links go from `NN-foo.md` to `../NN-foo.md`; the `../SECURITY.md`
   reference becomes `../../SECURITY.md`; etc.).
5. Update `../README.md` — strike the row for the moved feature in
   the active table and add a one-line pointer here.

This mirrors the deferred → deferred/implemented pattern; the two
folders together let a reviewer see the full lifecycle of any
slice — "what we planned" → "what we cut" → "what we shipped."

## Archive

| #  | Feature | Implemented |
|---:|---------|-------------|
| 11 | [ML — Artifact store abstraction](./11-ml-artifact-store.md) | `internal/artifacts/` Store interface with Local + S3 backends, unit + integration tests green |
| 12 | [ML — Job I/O staging](./12-ml-job-io-staging.md) | `SubmitRequest` inputs/outputs/working_dir; node-side Stager uploads outputs only on success, cleans workdir unconditionally |
| 13 | [ML — Workflow artifact passing](./13-ml-workflow-artifact-passing.md) | `from: upstream.OUTPUT` references resolved at dispatch time; fail-closed on resolver errors with transition to Failed |
| 14 | [ML — Node labels and selectors](./14-ml-node-labels-and-selectors.md) | Node-side `HELION_LABEL_*` env + auto-detection; scheduler filter; `job.unschedulable` event with debounce |
| 15 | [ML — GPU as a first-class resource](./15-ml-gpu-first-class-resource.md) | Whole-GPU scheduling + per-node allocator + `CUDA_VISIBLE_DEVICES` pinning; hardware attestation tracked as deferred |
| 16 | [ML — Dataset and model registry](./16-ml-dataset-model-registry.md) | `/api/datasets` + `/api/models` with lineage metadata; audit 2026-04-14-01 deferrals filed as M3/M4/L1/L3 |
| 17 | [ML — Inference jobs](./17-ml-inference-jobs.md) | `ServiceSpec` long-running jobs with readiness probes (Go runtime); audit 2026-04-14-02 landed; Rust parity deferred/20 |
| 18 | [ML — Dashboard module](./18-ml-dashboard-module.md) | All four views (Datasets / Models / Services / Pipelines) shipped; three audit-pass items deferred to backlog |
