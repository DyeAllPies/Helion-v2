# Audits

Security & code-quality audits, one file per run.

## Files here

- [`TEMPLATE.md`](TEMPLATE.md) — copy this when starting a new audit.
  Instructions at the top.
- `YYYY-MM-DD-NN.md` — closed audits, kept as a historical record of
  both problems and fixes. Never renamed, never deleted.

## Archive

| Audit ID | Headline |
|----------|----------|
| [2026-04-11-01](2026-04-11-01.md) | Initial full-system audit — closed all 10 findings |
| [2026-04-12-01](2026-04-12-01.md) | Second-pass audit after dispatch hardening — closed all 10 findings |
| [2026-04-14-01](2026-04-14-01.md) | ML registry slice audit — M1/M2 fixed, M3/M4/L1/L3 deferred |
| [2026-04-14-02](2026-04-14-02.md) | Inference-jobs slice audit — M2/M3/L2/L3/T2 fixed, M1/L1/T1 deferred |
| [2026-04-15-01](2026-04-15-01.md) | Feature-11 exhaustive coverage audit — 6 test gaps + 2 Medium + 2 Low all fixed; no production-code defects |

## Workflow

1. Copy `TEMPLATE.md` to `docs/audits/<date>-<NN>.md`. `NN` is a
   two-digit counter starting at `01` for the first audit that day;
   use `02`, `03` … for additional audits on the same date.
2. Fill in the Date, Auditor, and Overall Risk Level fields.
3. Work each finding to close (`✅ Fixed`) or **defer** (move into
   `docs/planned-features/deferred/` as a new numbered file — see
   `deferred/TEMPLATE.md`).
4. Update `docs/audits/README.md` (this file) with the new audit ID
   and a one-line headline.
5. Commit. Audit findings referenced outside this folder always use
   the full `AUDIT <audit-id>/<severity-letter><n>` form, e.g.
   `AUDIT 2026-04-11-01/H2`.

See the top-level [`docs/DOCS-WORKFLOW.md`](../DOCS-WORKFLOW.md) for
how audits connect to the planned-features and deferred folders.
