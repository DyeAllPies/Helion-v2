# 08 — Deferred Enhancements (moved)

**This file is a breadcrumb.** The deferred-enhancements backlog that
used to live here has been promoted to its own subdirectory so it can
grow without colliding with the numbered active-slice specs, and so
new deferrals can be filed against specific features without having to
edit a monolithic 08 doc.

## Where the content went

➡ **[`deferred/README.md`](deferred/README.md)** — the full backlog,
reorganised by the feature each item came from, with an updated
priority table. Everything that was in `08-deferred-enhancements.md`
is still there; the `GPU / accelerator resources` entry has been
replaced with a pointer to [feature 10](10-minimal-ml-pipeline.md)
§ Step 5 because it's now active work, not a deferral.

## Why the move

- **Append-only growth.** Later features (09 analytics, 10 ML
  pipeline) surfaced their own deferrals; stuffing them into a
  single `08-*` file meant the doc's title kept lagging reality.
  A `deferred/` folder can host the main backlog now and spin out
  per-feature supplementary files later without renumbering.
- **Numbered slots are for *active* slices.** When a reader scans
  the `planned-features/` index, every numbered entry should be
  something someone is shipping or has just shipped. A "not started,
  P3 backlog" doc at slot 08 was visually misleading — it sat
  between `07-observability` (done) and `09-analytics-pipeline`
  (done) as if it were a peer.
- **Historical refs.** Commits, audits, and old review comments
  that point at `08-deferred-enhancements.md` land here and get
  redirected cleanly to the new location. Don't delete this file.

## Also updated

- [`README.md`](README.md) in this directory now links to
  [`deferred/`](deferred/) under a "Backlog" heading and drops
  the 08 row from the feature index table.
- [`10-minimal-ml-pipeline.md`](10-minimal-ml-pipeline.md) §§ Step 5
  + "What this does NOT include" now link the backlog via the
  new path.

## Filing new deferrals

Don't add items here. File them as an entry in
[`deferred/README.md`](deferred/README.md) under the feature they came
from (e.g. "## ML Pipeline (from feature 10)" got a new
`### Hardware attestation of node labels` entry from the step-4
audit pass). New *active* slices still take a new numbered file
under `docs/planned-features/`.
