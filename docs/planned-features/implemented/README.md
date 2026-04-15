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
| 18 | [ML — Dashboard module](./18-ml-dashboard-module.md) | All four views (Datasets / Models / Services / Pipelines) shipped; three audit-pass items deferred to backlog |
