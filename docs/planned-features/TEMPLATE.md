# Feature: <name>

**Priority:** P0 / P1 / P2
**Status:** Pending / In progress / Done
**Affected files:** `path/to/file.go`, `path/to/other.go`, ...
<!-- If this feature is one step of a larger umbrella slice, add: -->
<!-- **Parent slice:** [feature NN — <name>](NN-name.md) -->

<!--
    File naming: `NN-kebab-case-slug.md` where NN is the next unused two-digit
    number in this directory. Keep the slug short and descriptive; it is used as
    the URL fragment in dashboards and cross-references.

    When a feature splits into many sub-features (e.g. the ML pipeline split
    into features 11–20), the umbrella file stays as a short overview that
    points at each sub-file via the `Parent slice:` header above.
-->

## Problem

What's wrong or missing today. Lead with the user-facing symptom, then the
structural reason it's hard to fix in the current shape.

## Current state

What exists, with file references in the form
`[filename.go](../../path/to/filename.go)` or `[filename.go:42](../../path/to/filename.go#L42)`.
Reviewers should be able to jump straight to the code.

## Design

Types, algorithms, API changes, state machine changes. Code blocks are
welcome. This section is the contract other engineers will implement against;
prefer concrete names and types over prose.

## Security plan

How the feature interacts with the project's existing mTLS / JWT / audit /
rate-limit stack. If it adds a new trust boundary, say so and enumerate the
controls. If it adds a new audit-event topic, list it. Cross-reference
[`docs/SECURITY.md`](../SECURITY.md) where appropriate.

## Implementation order

Numbered steps from easiest to hardest, with a column for dependencies.

| # | Step | Depends on | Effort |
|---|------|-----------|--------|
| 1 | ... | — | Small |

## Tests

List the specific tests that will gate the slice — table-driven units, an
integration case, a Playwright E2E, whichever is applicable. "Will write tests"
is not a plan; name the tests.

## Open questions

Decisions deferred or needing input. Once resolved, remove the bullet — the
spec is not a discussion log.

## Deferred

Sub-items consciously pushed past this slice should be filed under
`deferred/` as numbered files (see
[`deferred/TEMPLATE.md`](deferred/TEMPLATE.md)), not kept here. Link each one
back from this section so the slice's scope boundary is visible at a glance.

- None yet, or: [deferred/NN-slug.md](deferred/NN-slug.md) — one-line summary.

## Implementation status

_Filled in as the slice lands._

- What was built, with file paths.
- Test counts (unit / integration / E2E).
- Any deviations from the Design section above and why.
- Links to closed audits that reviewed the slice:
  `[2026-04-14-01](../audits/2026-04-14-01.md)`.
