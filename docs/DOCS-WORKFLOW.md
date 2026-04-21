# Docs Workflow — audits, planned features, deferred

Three folders under `docs/` carry the project's living
design+review process. They are meant to be read together: a feature
moves from *plan* → *implementation* → *audit*, and anything the audit
or the implementation team consciously pushes out lands in *deferred*.

```
 docs/
 ├── planned-features/    ← what we intend to build (active slices)
 │   └── deferred/        ← what we deliberately chose not to build yet
 └── audits/              ← post-implementation reviews (with IDs)
```

Every one of those three folders has the same shape:

- a `README.md` index — short, lists what's inside and points here for
  the workflow.
- a `TEMPLATE.md` — copied to start a new entry.
- numbered or dated entries — one concern per file.

## The three folders in one sentence each

| Folder | Purpose | Naming | Template |
|--------|---------|--------|----------|
| [`planned-features/`](planned-features/) | Active feature specs — in progress or queued for the next slice. Items that fully ship + pass a spec-vs-reality audit move to [`planned-features/implemented/`](planned-features/implemented/) keeping their original number. | `NN-kebab-slug.md` | [`planned-features/TEMPLATE.md`](planned-features/TEMPLATE.md) |
| [`planned-features/deferred/`](planned-features/deferred/) | Items consciously deferred during a slice or audit, with the reason preserved. Items that later get built move to [`deferred/implemented/`](planned-features/deferred/implemented/) with the same number, carrying both the deferral rationale and the landed-implementation write-up. | `NN-kebab-slug.md` | [`planned-features/deferred/TEMPLATE.md`](planned-features/deferred/TEMPLATE.md) |
| [`audits/`](audits/) | Closed security & code-quality audits, kept as a historical record. | `YYYY-MM-DD-NN.md` | [`audits/TEMPLATE.md`](audits/TEMPLATE.md) |

The `NN` prefix on the two feature folders is a stable two-digit
counter — new items take the next unused number. The `YYYY-MM-DD-NN`
on audits keeps files sortable and lets the same day hold multiple
audits.

## How a feature moves through the three folders

```
  1. idea                2. in flight              3. landed
  ─────────              ────────────              ─────────
  planned-features/      planned-features/         planned-features/
    NN-thing.md            NN-thing.md               NN-thing.md
    Status: Pending        Status: In progress       Status: Done
                                                       │
                                                       ▼
                                                  audits/
                                                    YYYY-MM-DD-NN.md
                                                    (reviews the slice)
                                                       │
                   ┌───────────────────────────────────┤
                   ▼                                   ▼
              finding fixed                   finding deferred
              (✅ in audit file)                     │
                                                     ▼
                                            planned-features/
                                                deferred/
                                              NN-deferred-thing.md
                                              back-link to audit ID
```

**Start a slice:** copy `planned-features/TEMPLATE.md` to the next
free number. Set `Status: Pending`. Fill in the spec. Commit the file
before writing any implementation code — the spec is part of the
review surface, not a trailing artifact.

**Ship a slice:** flip `Status:` to `Done` on the feature file, and
update its "Affected files" list. Do not delete the spec — it stays
as the design record that pairs with the commit history.

**Close a slice:** once a feature is fully implemented (every spec
item shipped, audit-pass deferrals filed under `deferred/` with
written rationales), `git mv` it from `planned-features/NN-slug.md`
to `planned-features/implemented/NN-slug.md`. The number is
preserved so cross-references from audits and commit messages still
resolve. Fix the relative paths inside the moved file
(sibling-feature links go from `NN-foo.md` to `../NN-foo.md`; a
`../security/README.md` reference becomes `../../security/README.md`).
Update `planned-features/README.md` to strike through the moved row.

**Audit a slice:** copy `audits/TEMPLATE.md` to
`audits/<today>-<NN>.md`, `NN` starting at `01` and incrementing for
additional audits the same day. Work each finding to **✅ Fixed** or
**→ Deferred**. A finding marked Deferred must have a matching file
under `planned-features/deferred/` that back-links to the audit ID.

**Defer an item:** copy `planned-features/deferred/TEMPLATE.md` to
the next free number. Reference the originating feature and (if
applicable) the audit ID in the frontmatter. The "Why deferred" and
"Revisit trigger" blocks are the part future readers will actually
care about — fill them honestly.

## Cross-references

- **Audit finding → deferred file.** In the audit, append
  `→ Deferred (see [deferred/NN-slug.md](../planned-features/deferred/NN-slug.md))`
  to the finding heading.
- **Deferred file → audit.** In the deferred file's frontmatter, set
  `**Audit reference:**` to a link of the form
  `[2026-04-14-01](../../audits/2026-04-14-01.md)` plus the finding IDs.
- **Code comment → audit.** Always use the full audit ID:
  `AUDIT 2026-04-14-01/M1`, never bare `M1`. Bare severity+number
  collides with every other audit's first Medium.
- **Feature → deferred item.** When a feature spec explicitly drops
  a sub-item into the deferred folder rather than building it now,
  link to the deferred file inline in the spec.

## Why three folders

Keeping plans, reviews, and deferrals separate stops them from
rotting into each other:

- **Plans are forward-looking** — they describe intent and get
  truncated when the work lands. A doc that mixes "what we plan to
  do" with "what we found after doing it" loses both.
- **Audits are immutable history** — the value is that a line-item
  from eighteen months ago is still recoverable. Editing old audits
  destroys that signal.
- **Deferred items are *promises to our future selves*** — a
  searchable record of "we saw this, we chose not to do it, here's
  why." They need to survive the feature spec that spawned them,
  so they live outside it.

## Packing the repo without these folders

`audits/` and `planned-features/` both grow over time. See
[`README.md` § Packing the repo](README.md#packing-the-repo-without-audits-and-planned-features)
for the `repomix` and `git archive` incantations that omit both while
keeping `DOCS-WORKFLOW.md` (this file) and the three TEMPLATE.md
files so a fresh checkout still explains the process.
