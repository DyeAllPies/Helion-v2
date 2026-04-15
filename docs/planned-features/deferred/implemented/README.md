# Implemented Deferred Items

Items that once lived in `docs/planned-features/deferred/` as
numbered entries and have since been built. They move here (keeping
their original number) instead of being deleted — the deferred
rationale + the "what actually landed" write-up are both useful
history for future reviewers.

## Files here

- `NN-kebab-slug.md` — one file per landed item. Keeps the original
  number so cross-references from audit files, commit messages, or
  code comments still resolve without renumbering.
- `README.md` — this index.

## Workflow

When a deferred item lands:

1. `git mv docs/planned-features/deferred/NN-slug.md docs/planned-features/deferred/implemented/NN-slug.md`
2. Rewrite the file's header from `**Status:** Deferred` to
   `**Status:** Implemented` and add an "Implemented in" line
   pointing at the commit or feature number that shipped it.
3. Keep the original deferral context as a historical section;
   add a "What actually landed" section describing the real
   implementation plus any deltas from the original plan.
4. Update `../README.md` — strike through the table row for the
   moved item and add a one-line pointer to the file here.

This preserves the link between the original "why we didn't do
this" decision and the eventual "why we ended up doing this"
outcome — both useful for judging the deferred backlog's signal.

## Archive

| # | Item | Landed with |
|---|------|-------------|
| 24 | [ML Pipelines DAG view](./24-ml-pipelines-dag-view.md) | Post-feature-18 follow-up (mermaid-rendered DAG + coordinator-side lineage join endpoint) |
